//go:build windows

package main

import (
	"os"
	"path/filepath"
	"testing"

	"golang.org/x/sys/windows"
)

func TestLoadServerAPICapabilityRejectsWindowsPreplantedWritableDirectory(t *testing.T) {
	t.Setenv(apiAccessTokenEnvironment, "")
	directory := filepath.Join(t.TempDir(), "private-data")
	_, source, err := loadServerAPICapability(directory)
	if err != nil {
		t.Fatal(err)
	}
	if err := os.Remove(source); err != nil {
		t.Fatal(err)
	}
	if err := setWindowsTestACL(directory, windows.GENERIC_ALL, true); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(source, []byte(testAPICapability+"\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	if _, _, err := loadServerAPICapability(directory); err == nil {
		t.Fatal("shared-writable data directory with a preplanted capability was accepted")
	}
}

func TestLoadServerAPICapabilityRejectsWindowsCapabilityReadableByEveryone(t *testing.T) {
	t.Setenv(apiAccessTokenEnvironment, "")
	directory := filepath.Join(t.TempDir(), "private-data")
	_, source, err := loadServerAPICapability(directory)
	if err != nil {
		t.Fatal(err)
	}
	if err := setWindowsTestACL(source, windows.GENERIC_READ, false); err != nil {
		t.Fatal(err)
	}
	if _, _, err := loadServerAPICapability(directory); err == nil {
		t.Fatal("API capability readable by Everyone was accepted")
	}
}

func setWindowsTestACL(path string, everyoneRights windows.ACCESS_MASK, inherit bool) error {
	trusted, err := trustedWindowsSIDs()
	if err != nil {
		return err
	}
	everyone, err := windows.CreateWellKnownSid(windows.WinWorldSid)
	if err != nil {
		return err
	}
	inheritance := uint32(windows.NO_INHERITANCE)
	if inherit {
		inheritance = windows.SUB_CONTAINERS_AND_OBJECTS_INHERIT
	}
	entries := []windows.EXPLICIT_ACCESS{
		fullControlEntry(trusted[0], windows.TRUSTEE_IS_USER, inheritance),
		fullControlEntry(trusted[1], windows.TRUSTEE_IS_WELL_KNOWN_GROUP, inheritance),
		fullControlEntry(trusted[2], windows.TRUSTEE_IS_WELL_KNOWN_GROUP, inheritance),
		{
			AccessPermissions: everyoneRights,
			AccessMode:        windows.GRANT_ACCESS,
			Inheritance:       inheritance,
			Trustee: windows.TRUSTEE{
				TrusteeForm:  windows.TRUSTEE_IS_SID,
				TrusteeType:  windows.TRUSTEE_IS_WELL_KNOWN_GROUP,
				TrusteeValue: windows.TrusteeValueFromSID(everyone),
			},
		},
	}
	acl, err := windows.ACLFromEntries(entries, nil)
	if err != nil {
		return err
	}
	return windows.SetNamedSecurityInfo(
		path,
		windows.SE_FILE_OBJECT,
		windows.DACL_SECURITY_INFORMATION|windows.PROTECTED_DACL_SECURITY_INFORMATION,
		nil,
		nil,
		acl,
		nil,
	)
}

func makeServerDataDirectoryUnsafe(t testing.TB, directory string) {
	t.Helper()
	if err := setWindowsTestACL(directory, windows.GENERIC_ALL, true); err != nil {
		t.Fatal(err)
	}
}
