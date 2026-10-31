//go:build linux

package appliance

import (
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"strings"

	"golang.org/x/sys/unix"
)

func prepareDownloadPath(source, target string) error {
	probeConfig := Config{SchemaVersion: 1, Port: DefaultPort, DownloadPath: source}
	if err := probeConfig.Validate(); err != nil {
		return err
	}
	if err := verifyWindowsDrvFSMount(source); err != nil {
		return err
	}
	if err := os.MkdirAll(source, 0o755); err != nil {
		return fmt.Errorf("create Windows download directory: %w", err)
	}
	probe, err := os.CreateTemp(source, ".amdl-atomic-probe-*")
	if err != nil {
		return fmt.Errorf("create Windows download probe: %w", err)
	}
	probePath := probe.Name()
	renamedPath := probePath + ".renamed"
	defer os.Remove(probePath)
	defer os.Remove(renamedPath)
	if _, err := probe.Write([]byte("amdl")); err != nil {
		_ = probe.Close()
		return fmt.Errorf("write Windows download probe: %w", err)
	}
	if err := probe.Sync(); err != nil {
		_ = probe.Close()
		return fmt.Errorf("sync Windows download probe: %w", err)
	}
	if err := probe.Close(); err != nil {
		return fmt.Errorf("close Windows download probe: %w", err)
	}
	if err := os.Rename(probePath, renamedPath); err != nil {
		return fmt.Errorf("atomically rename Windows download probe: %w", err)
	}
	if err := os.Remove(renamedPath); err != nil {
		return fmt.Errorf("remove Windows download probe: %w", err)
	}
	if err := os.MkdirAll(target, 0o755); err != nil {
		return fmt.Errorf("create appliance download mount: %w", err)
	}
	return bindDownloadPath(source, target)
}

// verifyWindowsDrvFSMount proves that the configured /mnt/<drive> path is
// backed by WSL's Windows DrvFS mount before any mkdir/write operation.  A
// missing automount must fail closed: creating /mnt/e on the appliance's ext4
// root would otherwise make a local directory look like the user's Windows
// volume and could silently divert downloads away from the selected disk.
func verifyWindowsDrvFSMount(source string) error {
	mountInfo, err := os.ReadFile("/proc/self/mountinfo")
	if err != nil {
		return fmt.Errorf("inspect Windows DrvFS mount: %w", err)
	}
	if !isWindowsDrvFSMountForPath(source, mountInfo) {
		return errors.New("Windows download path is not backed by a WSL DrvFS mount")
	}
	return nil
}

// isWindowsDrvFSMountForPath is kept pure so the mount identity contract can
// be tested without changing the host's WSL state.  WSL2 commonly exposes
// DrvFS as a 9p mount whose source or super options contain "drvfs"; older
// builds may report drvfs directly.
func isWindowsDrvFSMountForPath(source string, mountInfo []byte) bool {
	target := filepath.Clean(source)
	if !filepath.IsAbs(target) || target == string(filepath.Separator) {
		return false
	}
	bestLength := -1
	matched := false
	for _, line := range strings.Split(string(mountInfo), "\n") {
		separator := strings.Index(line, " - ")
		if separator < 0 {
			continue
		}
		left := strings.Fields(line[:separator])
		right := strings.Fields(line[separator+3:])
		if len(left) < 5 || len(right) < 3 {
			continue
		}
		mountPoint, ok := unescapeMountInfoPath(left[4])
		if !ok {
			continue
		}
		mountPoint = filepath.Clean(mountPoint)
		// Only a concrete WSL drive mount may authorize a Windows download
		// path.  Treating a DrvFS mount at /mnt itself as sufficient would
		// allow a missing /mnt/e automount to fall back to an unrelated
		// filesystem while still appearing to be Windows-backed.
		if !isWindowsDriveMountPoint(mountPoint) {
			continue
		}
		if target != mountPoint && !strings.HasPrefix(target, strings.TrimRight(mountPoint, string(filepath.Separator))+string(filepath.Separator)) {
			continue
		}
		if len(mountPoint) < bestLength {
			continue
		}
		bestLength = len(mountPoint)
		matched = isWindowsDrvFSMountFields(right)
	}
	return matched
}

func isWindowsDriveMountPoint(value string) bool {
	return len(value) >= len("/mnt/a") &&
		strings.HasPrefix(value, "/mnt/") &&
		value[5] >= 'a' && value[5] <= 'z' &&
		(len(value) == len("/mnt/a") || value[6] == '/')
}

func unescapeMountInfoPath(value string) (string, bool) {
	var builder strings.Builder
	for index := 0; index < len(value); index++ {
		if value[index] != '\\' {
			builder.WriteByte(value[index])
			continue
		}
		if index+3 >= len(value) || value[index+1] < '0' || value[index+1] > '7' || value[index+2] < '0' || value[index+2] > '7' || value[index+3] < '0' || value[index+3] > '7' {
			return "", false
		}
		decoded := (value[index+1]-'0')*64 + (value[index+2]-'0')*8 + (value[index+3] - '0')
		builder.WriteByte(decoded)
		index += 3
	}
	return builder.String(), true
}

func isWindowsDrvFSMountFields(fields []string) bool {
	if len(fields) < 3 {
		return false
	}
	fileSystem := strings.ToLower(fields[0])
	if fileSystem == "drvfs" || strings.HasSuffix(fileSystem, ".drvfs") {
		return true
	}
	if fileSystem != "9p" {
		return false
	}
	source := strings.ToLower(fields[1])
	superOptions := strings.ToLower(strings.Join(fields[2:], " "))
	return source == "drvfs" || strings.Contains(superOptions, "aname=drvfs")
}

const (
	applianceDownloadMountPath = "/srv/applemusic-downloader/downloads"
	maxDownloadMountLayers     = 16
)

type downloadMountLayer struct {
	mountPoint   string
	fileSystem   string
	mountSource  string
	superOptions string
}

func parseDownloadMountInfo(mountInfo []byte) ([]downloadMountLayer, error) {
	layers := make([]downloadMountLayer, 0)
	for _, line := range strings.Split(string(mountInfo), "\n") {
		if strings.TrimSpace(line) == "" {
			continue
		}
		separator := strings.Index(line, " - ")
		if separator < 0 {
			return nil, errors.New("mountinfo separator is missing")
		}
		left := strings.Fields(line[:separator])
		right := strings.Fields(line[separator+3:])
		if len(left) < 5 || len(right) < 3 {
			return nil, errors.New("mountinfo fields are incomplete")
		}
		mountPoint, ok := unescapeMountInfoPath(left[4])
		if !ok {
			return nil, errors.New("mountinfo path escaping is invalid")
		}
		layers = append(layers, downloadMountLayer{
			mountPoint:   filepath.Clean(mountPoint),
			fileSystem:   right[0],
			mountSource:  right[1],
			superOptions: strings.Join(right[2:], " "),
		})
	}
	return layers, nil
}

func downloadMountLayersAt(mountInfo []byte, target string) ([]downloadMountLayer, bool, error) {
	cleanTarget := filepath.Clean(target)
	if cleanTarget != applianceDownloadMountPath {
		return nil, false, nil
	}
	parsed, err := parseDownloadMountInfo(mountInfo)
	if err != nil {
		return nil, true, err
	}
	matched := make([]downloadMountLayer, 0)
	for _, layer := range parsed {
		if layer.mountPoint == cleanTarget {
			matched = append(matched, layer)
		}
	}
	return matched, true, nil
}

func ownedDownloadMountLayers(mountInfo []byte, target string) ([]downloadMountLayer, error) {
	layers, validTarget, parseErr := downloadMountLayersAt(mountInfo, target)
	if !validTarget {
		return nil, errors.New("appliance download mount target is invalid")
	}
	if parseErr != nil {
		return nil, errors.New("inspect appliance download mount")
	}
	if len(layers) > maxDownloadMountLayers {
		return nil, errors.New("too many existing appliance download mount layers")
	}
	for _, layer := range layers {
		if !isWindowsDrvFSMountFields([]string{layer.fileSystem, layer.mountSource, layer.superOptions}) {
			return nil, errors.New("appliance download mount is occupied by an unknown filesystem")
		}
	}
	return layers, nil
}

type downloadMountOps struct {
	readMountInfo func() ([]byte, error)
	unmount       func(string, int) error
	mount         func(string, string, string, uintptr, string) error
	stat          func(string) (os.FileInfo, error)
}

func bindDownloadPath(source, target string) error {
	return bindDownloadPathWithOps(source, target, downloadMountOps{
		readMountInfo: func() ([]byte, error) { return os.ReadFile("/proc/self/mountinfo") },
		unmount:       unix.Unmount,
		mount:         unix.Mount,
		stat:          os.Stat,
	})
}

// bindDownloadPathWithOps replaces only the exact Appliance download mount.
// Existing layers are accepted solely when every layer is a WSL DrvFS mount;
// normal unmounts (never MNT_DETACH) are used so a busy/foreign mount fails
// closed instead of being hidden beneath a new binding.
func bindDownloadPathWithOps(source, target string, ops downloadMountOps) error {
	if filepath.Clean(target) != applianceDownloadMountPath {
		return errors.New("appliance download mount target is invalid")
	}
	if ops.readMountInfo == nil || ops.unmount == nil || ops.mount == nil || ops.stat == nil {
		return errors.New("appliance download mount operations are unavailable")
	}
	sourceInfo, sourceErr := ops.stat(source)
	targetInfo, targetErr := ops.stat(target)
	if sourceErr == nil && targetErr == nil && os.SameFile(sourceInfo, targetInfo) {
		return nil
	}

	removed := 0
	for {
		mountInfo, err := ops.readMountInfo()
		if err != nil {
			return errors.New("inspect appliance download mount")
		}
		layers, err := ownedDownloadMountLayers(mountInfo, target)
		if err != nil {
			return err
		}
		if len(layers) == 0 {
			break
		}
		if removed >= maxDownloadMountLayers {
			return errors.New("too many existing appliance download mount layers")
		}
		if err := ops.unmount(target, 0); err != nil {
			return errors.New("remove existing appliance download mount")
		}
		removed++
	}

	if err := ops.mount(source, target, "", unix.MS_BIND, ""); err != nil {
		if errors.Is(err, unix.EBUSY) {
			return errors.New("download mount is already occupied by another path")
		}
		return fmt.Errorf("bind Windows download directory: %w", err)
	}
	sourceInfo, sourceErr = ops.stat(source)
	targetInfo, targetErr = ops.stat(target)
	if sourceErr != nil || targetErr != nil || !os.SameFile(sourceInfo, targetInfo) {
		_ = ops.unmount(target, 0)
		return errors.New("download bind mount identity verification failed")
	}
	if relative, err := filepath.Rel(target, filepath.Join(target, ".")); err != nil || relative != "." {
		_ = ops.unmount(target, 0)
		return errors.New("download mount path verification failed")
	}
	return nil
}

func prepareWrapperStatePath(source, target string) error {
	if err := prepareWrapperStateSource(source); err != nil {
		return err
	}
	if err := os.MkdirAll(target, 0o700); err != nil {
		return fmt.Errorf("create Wrapper data mount: %w", err)
	}
	return bindPath(source, target)
}

func prepareWrapperStateDirectory(source string) error {
	return prepareWrapperStateSource(source)
}

func prepareWrapperStateSource(source string) error {
	return prepareWrapperStateSourceForOwner(source, os.Geteuid(), os.Getegid())
}

func prepareWrapperStateSourceForOwner(source string, expectedUID, expectedGID int) error {
	if expectedUID < 0 || expectedGID < 0 {
		return errors.New("persistent Wrapper state owner must be non-negative")
	}

	cleanSource := filepath.Clean(source)
	if !filepath.IsAbs(cleanSource) || cleanSource == string(filepath.Separator) {
		return fmt.Errorf("persistent Wrapper state path %q must be an absolute non-root path", source)
	}

	const directoryOpenFlags = unix.O_RDONLY | unix.O_DIRECTORY | unix.O_NOFOLLOW | unix.O_CLOEXEC
	currentFD, err := unix.Open(string(filepath.Separator), directoryOpenFlags, 0)
	if err != nil {
		return fmt.Errorf("open persistent Wrapper state root: %w", err)
	}
	defer func() { _ = unix.Close(currentFD) }()

	if err := verifyWrapperStateDirectoryOwner(currentFD, string(filepath.Separator), expectedUID, expectedGID, false); err != nil {
		return err
	}

	components := strings.Split(strings.TrimPrefix(cleanSource, string(filepath.Separator)), string(filepath.Separator))
	walkedPath := string(filepath.Separator)
	for index, component := range components {
		nextPath := filepath.Join(walkedPath, component)
		nextFD, err := openOrCreateWrapperStateDirectoryAt(currentFD, component, nextPath)
		if err != nil {
			return err
		}
		if err := unix.Close(currentFD); err != nil {
			_ = unix.Close(nextFD)
			return fmt.Errorf("close persistent Wrapper state ancestor %q: %w", walkedPath, err)
		}
		currentFD = nextFD
		walkedPath = nextPath

		private := index == len(components)-1
		if err := verifyWrapperStateDirectoryOwner(currentFD, walkedPath, expectedUID, expectedGID, private); err != nil {
			return err
		}
		if private {
			if err := secureWrapperStateDirectory(currentFD, walkedPath); err != nil {
				return err
			}
		}
	}

	for _, component := range []string{"data", "com.apple.android.music", "files", "mpl_db"} {
		nextPath := filepath.Join(walkedPath, component)
		nextFD, err := openOrCreateWrapperStateDirectoryAt(currentFD, component, nextPath)
		if err != nil {
			return err
		}
		if err := unix.Close(currentFD); err != nil {
			_ = unix.Close(nextFD)
			return fmt.Errorf("close persistent Wrapper state directory %q: %w", walkedPath, err)
		}
		currentFD = nextFD
		walkedPath = nextPath

		if err := verifyWrapperStateDirectoryOwner(currentFD, walkedPath, expectedUID, expectedGID, true); err != nil {
			return err
		}
		if err := secureWrapperStateDirectory(currentFD, walkedPath); err != nil {
			return err
		}
	}

	return nil
}

func openOrCreateWrapperStateDirectoryAt(parentFD int, name, path string) (int, error) {
	const directoryOpenFlags = unix.O_RDONLY | unix.O_DIRECTORY | unix.O_NOFOLLOW | unix.O_CLOEXEC
	directoryFD, err := unix.Openat(parentFD, name, directoryOpenFlags, 0)
	if errors.Is(err, unix.ENOENT) {
		if mkdirErr := unix.Mkdirat(parentFD, name, 0o700); mkdirErr != nil && !errors.Is(mkdirErr, unix.EEXIST) {
			return -1, fmt.Errorf("create persistent Wrapper state directory %q: %w", path, mkdirErr)
		}
		directoryFD, err = unix.Openat(parentFD, name, directoryOpenFlags, 0)
	}
	if err != nil {
		if errors.Is(err, unix.ELOOP) || errors.Is(err, unix.ENOTDIR) {
			return -1, fmt.Errorf("persistent Wrapper state path %q must be a real directory", path)
		}
		return -1, fmt.Errorf("open persistent Wrapper state directory %q: %w", path, err)
	}
	return directoryFD, nil
}

func verifyWrapperStateDirectoryOwner(directoryFD int, path string, expectedUID, expectedGID int, private bool) error {
	var stat unix.Stat_t
	if err := unix.Fstat(directoryFD, &stat); err != nil {
		return fmt.Errorf("inspect persistent Wrapper state directory %q: %w", path, err)
	}
	if stat.Mode&unix.S_IFMT != unix.S_IFDIR {
		return fmt.Errorf("persistent Wrapper state path %q must be a real directory", path)
	}

	hasExpectedOwner := stat.Uid == uint32(expectedUID) && stat.Gid == uint32(expectedGID)
	hasRootOwner := stat.Uid == 0 && stat.Gid == 0
	if (private && !hasExpectedOwner) || (!private && !hasExpectedOwner && !hasRootOwner) {
		ownerAlternative := ""
		if !private {
			ownerAlternative = " or 0:0"
		}
		return fmt.Errorf(
			"persistent Wrapper state directory %q has owner %d:%d, want %d:%d%s",
			path,
			stat.Uid,
			stat.Gid,
			expectedUID,
			expectedGID,
			ownerAlternative,
		)
	}
	return nil
}

func secureWrapperStateDirectory(directoryFD int, path string) error {
	if err := unix.Fchmod(directoryFD, 0o700); err != nil {
		return fmt.Errorf("secure persistent Wrapper state directory %q: %w", path, err)
	}
	return nil
}

func bindPath(source, target string) error {
	sourceInfo, sourceErr := os.Stat(source)
	targetInfo, targetErr := os.Stat(target)
	if sourceErr == nil && targetErr == nil && os.SameFile(sourceInfo, targetInfo) {
		return nil
	}
	if err := unix.Mount(source, target, "", unix.MS_BIND, ""); err != nil {
		if errors.Is(err, unix.EBUSY) {
			return errors.New("download mount is already occupied by another path")
		}
		return fmt.Errorf("bind Windows download directory: %w", err)
	}
	sourceInfo, sourceErr = os.Stat(source)
	targetInfo, targetErr = os.Stat(target)
	if sourceErr != nil || targetErr != nil || !os.SameFile(sourceInfo, targetInfo) {
		_ = unix.Unmount(target, unix.MNT_DETACH)
		return errors.New("download bind mount identity verification failed")
	}
	// Ensure joins beneath target remain on the verified mount.
	if relative, err := filepath.Rel(target, filepath.Join(target, ".")); err != nil || relative != "." {
		return errors.New("download mount path verification failed")
	}
	return nil
}
