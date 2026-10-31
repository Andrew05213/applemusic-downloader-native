//go:build linux

package appliance

import (
	"errors"
	"os"
	"testing"

	"golang.org/x/sys/unix"
)

func TestWindowsDrvFSMountIdentityUsesTheLongestMountPoint(t *testing.T) {
	mountInfo := []byte("\n" +
		"100 1 0:100 / /mnt/e rw,relatime - 9p drvfs rw,aname=drvfs;path=E:\\;\n" +
		"101 1 0:101 / /mnt/e/overlay rw,relatime - ext4 /dev/vdb rw\n")
	if !isWindowsDrvFSMountForPath("/mnt/e/Music/Downloads", mountInfo[:]) {
		t.Fatal("a path below the Windows DrvFS mount was rejected")
	}
	if isWindowsDrvFSMountForPath("/mnt/e/overlay/Music", mountInfo[:]) {
		t.Fatal("a path below a non-DrvFS nested mount was accepted")
	}
}

func TestWindowsDrvFSMountIdentityAcceptsEscapedMountPoint(t *testing.T) {
	mountInfo := []byte("102 1 0:102 / /mnt/e/My\\040Drive rw,relatime - 9p drvfs rw,aname=drvfs;path=E:\\;\n")
	if !isWindowsDrvFSMountForPath("/mnt/e/My Drive/Downloads", mountInfo) {
		t.Fatal("escaped DrvFS mount point was not decoded")
	}
}

func TestWindowsDrvFSMountIdentityRejectsMissingOrNonDrvFSMount(t *testing.T) {
	tests := []struct {
		name      string
		mountInfo string
	}{
		{name: "missing mount", mountInfo: "103 1 0:103 / /mnt/c rw - ext4 /dev/vda rw\n"},
		{name: "unrelated nine-p mount", mountInfo: "104 1 0:104 / /mnt/i rw - 9p server rw\n"},
		{name: "non drive path", mountInfo: "105 1 0:105 / /mnt rw - 9p drvfs rw,aname=drvfs\n"},
	}
	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			if isWindowsDrvFSMountForPath("/mnt/e/Music", []byte(test.mountInfo)) {
				t.Fatal("non-DrvFS path was accepted")
			}
		})
	}
}

func TestWindowsDrvFSMountIdentityRejectsMalformedMountInfo(t *testing.T) {
	for _, mountInfo := range [][]byte{
		[]byte("not mountinfo\n"),
		[]byte("106 1 0:106 / /mnt/e\\999 rw - 9p drvfs rw\n"),
		[]byte("107 1 0:107 / /mnt/e rw - 9p\n"),
	} {
		if isWindowsDrvFSMountForPath("/mnt/e/Music", mountInfo) {
			t.Fatalf("malformed mount info was accepted: %q", mountInfo)
		}
	}
}

func TestBindDownloadPathReplacesOnlyOwnedDrvFSLayers(t *testing.T) {
	source := t.TempDir()
	target := applianceDownloadMountPath
	other := t.TempDir()
	sourceInfo, err := os.Stat(source)
	if err != nil {
		t.Fatal(err)
	}
	targetInfo, err := os.Stat(other)
	if err != nil {
		t.Fatal(err)
	}
	mountInfo := []byte("100 1 0:100 / /srv/applemusic-downloader/downloads rw - 9p drvfs rw,aname=drvfs;path=C:\\\\;\n" +
		"101 1 0:101 / /srv/applemusic-downloader/downloads rw - 9p drvfs rw,aname=drvfs;path=I:\\\\;\n")
	var unmountFlags []int
	mountCalls := 0
	mounted := false
	ops := downloadMountOps{
		readMountInfo: func() ([]byte, error) {
			if len(mountInfo) == 0 {
				return []byte(""), nil
			}
			return mountInfo, nil
		},
		unmount: func(path string, flags int) error {
			if path != target {
				t.Fatalf("unmount path = %q", path)
			}
			unmountFlags = append(unmountFlags, flags)
			if len(unmountFlags) == 1 {
				mountInfo = []byte("101 1 0:101 / /srv/applemusic-downloader/downloads rw - 9p drvfs rw,aname=drvfs;path=I:\\\\;\n")
			} else {
				mountInfo = nil
			}
			return nil
		},
		mount: func(sourcePath, targetPath, fileSystem string, flags uintptr, data string) error {
			if sourcePath != source || targetPath != target || flags != unix.MS_BIND || fileSystem != "" || data != "" {
				t.Fatalf("unexpected mount call: %q %q %q %d %q", sourcePath, targetPath, fileSystem, flags, data)
			}
			mountCalls++
			mounted = true
			return nil
		},
		stat: func(path string) (os.FileInfo, error) {
			switch path {
			case source:
				return sourceInfo, nil
			case target:
				if mounted {
					return sourceInfo, nil
				}
				return targetInfo, nil
			default:
				return nil, errors.New("unexpected stat path")
			}
		},
	}
	if err := bindDownloadPathWithOps(source, target, ops); err != nil {
		t.Fatalf("bindDownloadPathWithOps: %v", err)
	}
	if mountCalls != 1 {
		t.Fatalf("mount calls = %d, want 1", mountCalls)
	}
	if len(unmountFlags) != 2 || unmountFlags[0] != 0 || unmountFlags[1] != 0 {
		t.Fatalf("unmount flags = %#v, want two normal unmounts", unmountFlags)
	}
}

func TestBindDownloadPathRejectsForeignMountWithoutUnmount(t *testing.T) {
	source := t.TempDir()
	mountCalls := 0
	unmountCalls := 0
	err := bindDownloadPathWithOps(source, applianceDownloadMountPath, downloadMountOps{
		readMountInfo: func() ([]byte, error) {
			return []byte("100 1 0:100 / /srv/applemusic-downloader/downloads rw - ext4 /dev/vdb rw\n"), nil
		},
		unmount: func(string, int) error { unmountCalls++; return nil },
		mount:   func(string, string, string, uintptr, string) error { mountCalls++; return nil },
		stat: func(path string) (os.FileInfo, error) {
			return os.Stat(path)
		},
	})
	if err == nil || err.Error() != "appliance download mount is occupied by an unknown filesystem" {
		t.Fatalf("error = %v", err)
	}
	if mountCalls != 0 || unmountCalls != 0 {
		t.Fatalf("foreign mount was touched: mount=%d unmount=%d", mountCalls, unmountCalls)
	}
}

func TestBindDownloadPathFailsClosedWhenUnmountIsBusy(t *testing.T) {
	source := t.TempDir()
	mountCalls := 0
	err := bindDownloadPathWithOps(source, applianceDownloadMountPath, downloadMountOps{
		readMountInfo: func() ([]byte, error) {
			return []byte("100 1 0:100 / /srv/applemusic-downloader/downloads rw - 9p drvfs rw,aname=drvfs\n"), nil
		},
		unmount: func(string, int) error { return errors.New("busy") },
		mount:   func(string, string, string, uintptr, string) error { mountCalls++; return nil },
		stat: func(path string) (os.FileInfo, error) {
			return os.Stat(path)
		},
	})
	if err == nil || err.Error() != "remove existing appliance download mount" {
		t.Fatalf("error = %v", err)
	}
	if mountCalls != 0 {
		t.Fatalf("new mount was attempted after unmount failure")
	}
}

func TestBindDownloadPathRejectsNonApplianceTarget(t *testing.T) {
	called := false
	err := bindDownloadPathWithOps("/mnt/e/Music", "/tmp/downloads", downloadMountOps{
		readMountInfo: func() ([]byte, error) { called = true; return nil, nil },
		unmount:       func(string, int) error { called = true; return nil },
		mount:         func(string, string, string, uintptr, string) error { called = true; return nil },
		stat:          func(string) (os.FileInfo, error) { called = true; return nil, nil },
	})
	if err == nil || err.Error() != "appliance download mount target is invalid" {
		t.Fatalf("error = %v", err)
	}
	if called {
		t.Fatal("invalid target invoked mount operations")
	}
}
