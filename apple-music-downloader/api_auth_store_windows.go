//go:build windows

package main

import (
	"crypto/rand"
	"encoding/hex"
	"errors"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"unsafe"

	"golang.org/x/sys/windows"
)

var windowsCapabilityDirectoryInitialization sync.Mutex

type windowsFileIdentity struct {
	volume    uint32
	indexHigh uint32
	indexLow  uint32
}

type secureAPICapabilityStore struct {
	directory         string
	directoryHandle   windows.Handle
	directoryIdentity windowsFileIdentity
	lockFile          *os.File
	lockOverlapped    windows.Overlapped
	corruptFile       *windowsFileIdentity
}

func openSecureAPICapabilityStore(directory string) (*secureAPICapabilityStore, error) {
	windowsCapabilityDirectoryInitialization.Lock()
	defer windowsCapabilityDirectoryInitialization.Unlock()

	absolute, err := filepath.Abs(directory)
	if err != nil {
		return nil, fmt.Errorf("resolve data directory: %w", err)
	}
	absolute = filepath.Clean(absolute)
	if strings.HasPrefix(absolute, `\\`) || strings.HasPrefix(absolute, `\\?\`) || strings.HasPrefix(absolute, `\\.\`) {
		return nil, errors.New("API capability data directory must be on a local non-device path")
	}

	created := false
	info, err := os.Lstat(absolute)
	if errors.Is(err, os.ErrNotExist) {
		if err := os.MkdirAll(absolute, 0o700); err != nil {
			return nil, fmt.Errorf("create data directory: %w", err)
		}
		created = true
	} else if err != nil {
		return nil, fmt.Errorf("inspect data directory: %w", err)
	} else if !info.IsDir() || info.Mode()&os.ModeSymlink != 0 {
		return nil, errors.New("data directory is not a real directory")
	}
	if err := rejectWindowsReparseComponents(absolute); err != nil {
		return nil, err
	}

	directoryHandle, err := openWindowsDirectoryHandle(absolute)
	if err != nil {
		return nil, err
	}
	store := &secureAPICapabilityStore{directory: absolute, directoryHandle: directoryHandle}
	cleanup := true
	defer func() {
		if cleanup {
			_ = store.close()
		}
	}()

	directoryInfo, err := windowsHandleInformation(directoryHandle)
	if err != nil {
		return nil, fmt.Errorf("inspect opened data directory: %w", err)
	}
	if directoryInfo.FileAttributes&windows.FILE_ATTRIBUTE_DIRECTORY == 0 || directoryInfo.FileAttributes&windows.FILE_ATTRIBUTE_REPARSE_POINT != 0 {
		return nil, errors.New("opened data path is not a non-reparse directory")
	}
	store.directoryIdentity = identityFromWindowsInfo(directoryInfo)
	if !created {
		if err := validateWindowsOwnerAndACL(directoryHandle, "data directory", false, false); err != nil {
			artifactsPresent, inspectErr := windowsDirectoryContainsEntries(absolute)
			if inspectErr != nil {
				return nil, inspectErr
			}
			if artifactsPresent {
				return nil, err
			}
			// An existing, current-user-owned but empty directory can be made
			// private safely. This also closes the first-start race where another
			// process created the directory but has not yet applied its DACL. Once
			// a token or lock exists, an unsafe DACL is always rejected instead.
			if ownerErr := validateWindowsCurrentOwner(directoryHandle, "data directory"); ownerErr != nil {
				return nil, ownerErr
			}
		}
	}
	if err := setWindowsPrivateACL(directoryHandle, true); err != nil {
		return nil, fmt.Errorf("make data directory private: %w", err)
	}
	if err := validateWindowsOwnerAndACL(directoryHandle, "data directory", true, true); err != nil {
		return nil, err
	}
	if err := store.verifyDirectoryIdentity(); err != nil {
		return nil, err
	}

	lockFile, err := openWindowsStoreLock(filepath.Join(absolute, apiCapabilityLockFilename))
	if err != nil {
		return nil, err
	}
	store.lockFile = lockFile
	if err := windows.LockFileEx(windows.Handle(lockFile.Fd()), windows.LOCKFILE_EXCLUSIVE_LOCK, 0, 1, 0, &store.lockOverlapped); err != nil {
		return nil, fmt.Errorf("lock API capability store: %w", err)
	}
	if err := store.verifyDirectoryIdentity(); err != nil {
		return nil, err
	}

	cleanup = false
	return store, nil
}

func windowsDirectoryContainsEntries(directory string) (bool, error) {
	entries, err := os.ReadDir(directory)
	if err != nil {
		return false, fmt.Errorf("inspect existing data directory contents: %w", err)
	}
	return len(entries) != 0, nil
}

func openWindowsDirectoryHandle(path string) (windows.Handle, error) {
	pathPointer, err := windows.UTF16PtrFromString(path)
	if err != nil {
		return windows.InvalidHandle, err
	}
	handle, err := windows.CreateFile(
		pathPointer,
		windows.GENERIC_READ|windows.READ_CONTROL|windows.WRITE_DAC,
		windows.FILE_SHARE_READ|windows.FILE_SHARE_WRITE|windows.FILE_SHARE_DELETE,
		nil,
		windows.OPEN_EXISTING,
		windows.FILE_FLAG_BACKUP_SEMANTICS|windows.FILE_FLAG_OPEN_REPARSE_POINT,
		0,
	)
	if err != nil {
		return windows.InvalidHandle, fmt.Errorf("open data directory without following reparse points: %w", err)
	}
	return handle, nil
}

func rejectWindowsReparseComponents(path string) error {
	volume := filepath.VolumeName(path)
	if volume == "" {
		return errors.New("API capability data directory must be absolute")
	}
	current := volume + string(os.PathSeparator)
	remainder := strings.TrimPrefix(path, current)
	for _, component := range strings.Split(remainder, string(os.PathSeparator)) {
		if component == "" {
			continue
		}
		current = filepath.Join(current, component)
		pointer, err := windows.UTF16PtrFromString(current)
		if err != nil {
			return err
		}
		attributes, err := windows.GetFileAttributes(pointer)
		if err != nil {
			return fmt.Errorf("inspect data directory component %q: %w", current, err)
		}
		if attributes&windows.FILE_ATTRIBUTE_REPARSE_POINT != 0 {
			return fmt.Errorf("data directory component %q is a reparse point", current)
		}
	}
	return nil
}

func windowsHandleInformation(handle windows.Handle) (windows.ByHandleFileInformation, error) {
	var info windows.ByHandleFileInformation
	return info, windows.GetFileInformationByHandle(handle, &info)
}

func identityFromWindowsInfo(info windows.ByHandleFileInformation) windowsFileIdentity {
	return windowsFileIdentity{volume: info.VolumeSerialNumber, indexHigh: info.FileIndexHigh, indexLow: info.FileIndexLow}
}

func sameWindowsIdentity(left, right windowsFileIdentity) bool {
	return left == right
}

func currentWindowsUserSID() (*windows.SID, error) {
	user, err := windows.GetCurrentProcessToken().GetTokenUser()
	if err != nil {
		return nil, err
	}
	return user.User.Sid, nil
}

func validateWindowsCurrentOwner(handle windows.Handle, label string) error {
	userSID, err := currentWindowsUserSID()
	if err != nil {
		return fmt.Errorf("resolve current Windows identity: %w", err)
	}
	securityDescriptor, err := windows.GetSecurityInfo(handle, windows.SE_FILE_OBJECT, windows.OWNER_SECURITY_INFORMATION)
	if err != nil {
		return fmt.Errorf("inspect %s owner: %w", label, err)
	}
	owner, _, err := securityDescriptor.Owner()
	if err != nil || owner == nil || !owner.Equals(userSID) {
		return fmt.Errorf("%s is not owned by the current Windows user", label)
	}
	return nil
}

func trustedWindowsSIDs() ([]*windows.SID, error) {
	userSID, err := currentWindowsUserSID()
	if err != nil {
		return nil, err
	}
	systemSID, err := windows.CreateWellKnownSid(windows.WinLocalSystemSid)
	if err != nil {
		return nil, err
	}
	administratorsSID, err := windows.CreateWellKnownSid(windows.WinBuiltinAdministratorsSid)
	if err != nil {
		return nil, err
	}
	return []*windows.SID{userSID, systemSID, administratorsSID}, nil
}

func validateWindowsOwnerAndACL(handle windows.Handle, label string, strict bool, requireProtected bool) error {
	trusted, err := trustedWindowsSIDs()
	if err != nil {
		return fmt.Errorf("resolve trusted Windows identities: %w", err)
	}
	securityDescriptor, err := windows.GetSecurityInfo(
		handle,
		windows.SE_FILE_OBJECT,
		windows.OWNER_SECURITY_INFORMATION|windows.DACL_SECURITY_INFORMATION,
	)
	if err != nil {
		return fmt.Errorf("inspect %s ACL: %w", label, err)
	}
	owner, _, err := securityDescriptor.Owner()
	if err != nil || owner == nil || !owner.Equals(trusted[0]) {
		return fmt.Errorf("%s is not owned by the current Windows user", label)
	}
	control, _, err := securityDescriptor.Control()
	if err != nil {
		return fmt.Errorf("inspect %s ACL control flags: %w", label, err)
	}
	if control&windows.SE_DACL_PRESENT == 0 {
		return fmt.Errorf("%s has no DACL", label)
	}
	if requireProtected && control&windows.SE_DACL_PROTECTED == 0 {
		return fmt.Errorf("%s inherits an unprotected DACL", label)
	}
	dacl, _, err := securityDescriptor.DACL()
	if err != nil || dacl == nil {
		return fmt.Errorf("%s has an invalid or null DACL", label)
	}
	for index := uint16(0); index < dacl.AceCount; index++ {
		var ace *windows.ACCESS_ALLOWED_ACE
		if err := windows.GetAce(dacl, uint32(index), &ace); err != nil {
			return fmt.Errorf("inspect %s ACL entry %d: %w", label, index, err)
		}
		if ace == nil || ace.Header.AceSize < uint16(unsafe.Sizeof(windows.ACCESS_ALLOWED_ACE{})) {
			return fmt.Errorf("%s has a malformed ACL entry", label)
		}
		if ace.Header.AceType == windows.ACCESS_DENIED_ACE_TYPE {
			continue
		}
		if ace.Header.AceType != windows.ACCESS_ALLOWED_ACE_TYPE {
			return fmt.Errorf("%s has an unsupported ACL entry type", label)
		}
		aceSID := (*windows.SID)(unsafe.Pointer(&ace.SidStart))
		isTrusted := false
		for _, trustedSID := range trusted {
			if aceSID.Equals(trustedSID) {
				isTrusted = true
				break
			}
		}
		if isTrusted || ace.Header.AceFlags&windows.INHERIT_ONLY_ACE != 0 {
			continue
		}
		if strict || windowsACLGrantsMutation(ace.Mask) {
			return fmt.Errorf("%s grants unsafe access to an untrusted Windows identity", label)
		}
	}
	return nil
}

func windowsACLGrantsMutation(mask windows.ACCESS_MASK) bool {
	const fileMutationRights windows.ACCESS_MASK = 0x00000002 | 0x00000004 | 0x00000010 | 0x00000020 | 0x00000040 | 0x00000100
	return mask&(windows.GENERIC_ALL|windows.GENERIC_WRITE|windows.WRITE_DAC|windows.WRITE_OWNER|windows.DELETE|fileMutationRights) != 0
}

func setWindowsPrivateACL(handle windows.Handle, inherit bool) error {
	trusted, err := trustedWindowsSIDs()
	if err != nil {
		return err
	}
	inheritance := uint32(windows.NO_INHERITANCE)
	if inherit {
		inheritance = windows.SUB_CONTAINERS_AND_OBJECTS_INHERIT
	}
	entries := make([]windows.EXPLICIT_ACCESS, 0, len(trusted))
	for index, sid := range trusted {
		trusteeType := windows.TRUSTEE_TYPE(windows.TRUSTEE_IS_WELL_KNOWN_GROUP)
		if index == 0 {
			trusteeType = windows.TRUSTEE_IS_USER
		}
		entries = append(entries, fullControlEntry(sid, trusteeType, inheritance))
	}
	acl, err := windows.ACLFromEntries(entries, nil)
	if err != nil {
		return err
	}
	return windows.SetSecurityInfo(
		handle,
		windows.SE_FILE_OBJECT,
		windows.DACL_SECURITY_INFORMATION|windows.PROTECTED_DACL_SECURITY_INFORMATION,
		nil,
		nil,
		acl,
		nil,
	)
}

func fullControlEntry(sid *windows.SID, trusteeType windows.TRUSTEE_TYPE, inheritance uint32) windows.EXPLICIT_ACCESS {
	return windows.EXPLICIT_ACCESS{
		AccessPermissions: windows.GENERIC_ALL,
		AccessMode:        windows.GRANT_ACCESS,
		Inheritance:       inheritance,
		Trustee: windows.TRUSTEE{
			TrusteeForm:  windows.TRUSTEE_IS_SID,
			TrusteeType:  trusteeType,
			TrusteeValue: windows.TrusteeValueFromSID(sid),
		},
	}
}

func privateWindowsSecurityAttributes() (*windows.SecurityAttributes, error) {
	userSID, err := currentWindowsUserSID()
	if err != nil {
		return nil, err
	}
	sddl := "D:P(A;;FA;;;" + userSID.String() + ")(A;;FA;;;SY)(A;;FA;;;BA)"
	securityDescriptor, err := windows.SecurityDescriptorFromString(sddl)
	if err != nil {
		return nil, err
	}
	return &windows.SecurityAttributes{
		Length:             uint32(unsafe.Sizeof(windows.SecurityAttributes{})),
		SecurityDescriptor: securityDescriptor,
	}, nil
}

func openWindowsStoreLock(path string) (*os.File, error) {
	securityAttributes, err := privateWindowsSecurityAttributes()
	if err != nil {
		return nil, fmt.Errorf("construct private API capability lock ACL: %w", err)
	}
	pointer, err := windows.UTF16PtrFromString(path)
	if err != nil {
		return nil, err
	}
	access := uint32(windows.GENERIC_READ | windows.GENERIC_WRITE | windows.READ_CONTROL | windows.WRITE_DAC)
	sharing := uint32(windows.FILE_SHARE_READ | windows.FILE_SHARE_WRITE)
	handle, err := windows.CreateFile(pointer, access, sharing, securityAttributes, windows.CREATE_NEW, windows.FILE_ATTRIBUTE_NORMAL|windows.FILE_FLAG_OPEN_REPARSE_POINT, 0)
	if errors.Is(err, windows.ERROR_FILE_EXISTS) || errors.Is(err, windows.ERROR_ALREADY_EXISTS) {
		handle, err = windows.CreateFile(pointer, access, sharing, nil, windows.OPEN_EXISTING, windows.FILE_ATTRIBUTE_NORMAL|windows.FILE_FLAG_OPEN_REPARSE_POINT, 0)
	}
	if err != nil {
		return nil, fmt.Errorf("open API capability lock: %w", err)
	}
	if err := validateWindowsPrivateFile(handle, "API capability lock"); err != nil {
		_ = windows.CloseHandle(handle)
		return nil, err
	}
	return os.NewFile(uintptr(handle), path), nil
}

func validateWindowsPrivateFile(handle windows.Handle, label string) error {
	info, err := windowsHandleInformation(handle)
	if err != nil {
		return fmt.Errorf("inspect %s: %w", label, err)
	}
	if info.FileAttributes&windows.FILE_ATTRIBUTE_DIRECTORY != 0 || info.FileAttributes&windows.FILE_ATTRIBUTE_REPARSE_POINT != 0 {
		return fmt.Errorf("%s is not a regular non-reparse file", label)
	}
	if info.NumberOfLinks != 1 {
		return fmt.Errorf("%s has %d hard links, expected one", label, info.NumberOfLinks)
	}
	return validateWindowsOwnerAndACL(handle, label, true, true)
}

func (s *secureAPICapabilityStore) directoryPath() string {
	return s.directory
}

func (s *secureAPICapabilityStore) verifyDirectoryIdentity() error {
	currentHandle, err := openWindowsDirectoryHandle(s.directory)
	if err != nil {
		return fmt.Errorf("reopen data directory for identity check: %w", err)
	}
	defer windows.CloseHandle(currentHandle)
	info, err := windowsHandleInformation(currentHandle)
	if err != nil {
		return err
	}
	if info.FileAttributes&windows.FILE_ATTRIBUTE_REPARSE_POINT != 0 || !sameWindowsIdentity(s.directoryIdentity, identityFromWindowsInfo(info)) {
		return errors.New("data directory path changed after it was opened")
	}
	return validateWindowsOwnerAndACL(s.directoryHandle, "data directory", true, true)
}

func openWindowsPrivateCapability(path string, access uint32, sharing uint32, creation uint32, securityAttributes *windows.SecurityAttributes) (windows.Handle, error) {
	pointer, err := windows.UTF16PtrFromString(path)
	if err != nil {
		return windows.InvalidHandle, err
	}
	return windows.CreateFile(
		pointer,
		access,
		sharing,
		securityAttributes,
		creation,
		windows.FILE_ATTRIBUTE_NORMAL|windows.FILE_FLAG_OPEN_REPARSE_POINT,
		0,
	)
}

func (s *secureAPICapabilityStore) readCapability() (string, error) {
	if err := s.verifyDirectoryIdentity(); err != nil {
		return "", err
	}
	path := filepath.Join(s.directory, apiAccessTokenFilename)
	handle, err := openWindowsPrivateCapability(path, windows.GENERIC_READ|windows.READ_CONTROL, windows.FILE_SHARE_READ, windows.OPEN_EXISTING, nil)
	if err != nil {
		return "", err
	}
	file := os.NewFile(uintptr(handle), path)
	defer file.Close()
	if err := validateWindowsPrivateFile(handle, "API capability file"); err != nil {
		return "", err
	}
	info, err := windowsHandleInformation(handle)
	if err != nil {
		return "", err
	}
	identity := identityFromWindowsInfo(info)
	data, err := io.ReadAll(io.LimitReader(file, apiCapabilityMaximumFileLen+1))
	if err != nil {
		return "", fmt.Errorf("read API capability: %w", err)
	}
	capability, err := parseAPICapabilityFile(data)
	if errors.Is(err, errAPICapabilityCorrupt) {
		copyOfIdentity := identity
		s.corruptFile = &copyOfIdentity
	} else if err == nil {
		s.corruptFile = nil
	}
	return capability, err
}

func (s *secureAPICapabilityStore) removeCorruptCapability() error {
	if s.corruptFile == nil {
		return errors.New("no verified corrupt API capability is pending recovery")
	}
	if err := s.verifyDirectoryIdentity(); err != nil {
		return err
	}
	path := filepath.Join(s.directory, apiAccessTokenFilename)
	handle, err := openWindowsPrivateCapability(path, windows.GENERIC_READ|windows.READ_CONTROL|windows.DELETE, 0, windows.OPEN_EXISTING, nil)
	if err != nil {
		return err
	}
	file := os.NewFile(uintptr(handle), path)
	defer file.Close()
	if err := validateWindowsPrivateFile(handle, "corrupt API capability file"); err != nil {
		return err
	}
	info, err := windowsHandleInformation(handle)
	if err != nil {
		return err
	}
	if !sameWindowsIdentity(*s.corruptFile, identityFromWindowsInfo(info)) {
		return errors.New("corrupt API capability changed before recovery")
	}
	deleteFile := byte(1)
	if err := windows.SetFileInformationByHandle(handle, windows.FileDispositionInfo, &deleteFile, 1); err != nil {
		return fmt.Errorf("mark corrupt API capability for deletion: %w", err)
	}
	if err := file.Close(); err != nil {
		return err
	}
	s.corruptFile = nil
	return nil
}

func (s *secureAPICapabilityStore) publishCapability(capability string) (bool, error) {
	if err := s.verifyDirectoryIdentity(); err != nil {
		return false, err
	}
	temporaryName, err := randomWindowsCapabilityTempName()
	if err != nil {
		return false, err
	}
	temporaryPath := filepath.Join(s.directory, temporaryName)
	securityAttributes, err := privateWindowsSecurityAttributes()
	if err != nil {
		return false, err
	}
	handle, err := openWindowsPrivateCapability(
		temporaryPath,
		windows.GENERIC_READ|windows.GENERIC_WRITE|windows.READ_CONTROL|windows.WRITE_DAC,
		0,
		windows.CREATE_NEW,
		securityAttributes,
	)
	if err != nil {
		return false, fmt.Errorf("create private API capability temporary file: %w", err)
	}
	temporaryFile := os.NewFile(uintptr(handle), temporaryPath)
	removeTemporary := true
	defer func() {
		_ = temporaryFile.Close()
		if removeTemporary {
			_ = os.Remove(temporaryPath)
		}
	}()
	if err := validateWindowsPrivateFile(handle, "API capability temporary file"); err != nil {
		return false, err
	}
	serialized := []byte(capability + "\n")
	_, writeErr := temporaryFile.Write(serialized)
	for index := range serialized {
		serialized[index] = 0
	}
	if writeErr != nil {
		return false, writeErr
	}
	if err := temporaryFile.Sync(); err != nil {
		return false, err
	}
	if err := temporaryFile.Close(); err != nil {
		return false, err
	}
	if err := s.verifyDirectoryIdentity(); err != nil {
		return false, err
	}
	from, err := windows.UTF16PtrFromString(temporaryPath)
	if err != nil {
		return false, err
	}
	to, err := windows.UTF16PtrFromString(filepath.Join(s.directory, apiAccessTokenFilename))
	if err != nil {
		return false, err
	}
	err = windows.MoveFileEx(from, to, windows.MOVEFILE_WRITE_THROUGH)
	if errors.Is(err, windows.ERROR_FILE_EXISTS) || errors.Is(err, windows.ERROR_ALREADY_EXISTS) {
		return false, nil
	}
	if err != nil {
		return false, err
	}
	removeTemporary = false
	stored, err := s.readCapability()
	if err != nil {
		return false, fmt.Errorf("verify published API capability: %w", err)
	}
	if !constantTimeStringEqual(stored, capability) {
		return false, errors.New("published API capability did not match generated value")
	}
	return true, nil
}

func randomWindowsCapabilityTempName() (string, error) {
	buffer := make([]byte, 16)
	if _, err := rand.Read(buffer); err != nil {
		return "", fmt.Errorf("generate API capability temporary name: %w", err)
	}
	name := ".api-access-token." + hex.EncodeToString(buffer) + ".tmp"
	for index := range buffer {
		buffer[index] = 0
	}
	return name, nil
}

func (s *secureAPICapabilityStore) close() error {
	var errs []error
	if s.lockFile != nil {
		if err := windows.UnlockFileEx(windows.Handle(s.lockFile.Fd()), 0, 1, 0, &s.lockOverlapped); err != nil {
			errs = append(errs, err)
		}
		if err := s.lockFile.Close(); err != nil {
			errs = append(errs, err)
		}
		s.lockFile = nil
	}
	if s.directoryHandle != 0 && s.directoryHandle != windows.InvalidHandle {
		if err := windows.CloseHandle(s.directoryHandle); err != nil {
			errs = append(errs, err)
		}
		s.directoryHandle = windows.InvalidHandle
	}
	return errors.Join(errs...)
}
