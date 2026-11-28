package nativeruntime

import (
	"unsafe"

	"golang.org/x/sys/windows"
)

var whvGetCapability = windows.NewLazySystemDLL("WinHvPlatform.dll").NewProc("WHvGetCapability")

func hostSupportsWHPX() bool {
	if err := whvGetCapability.Find(); err != nil {
		return false
	}
	// WHvCapabilityCodeHypervisorPresent returns a Win32 BOOL. The Windows
	// API recommends a buffer large enough for a 64-bit capability value.
	const hypervisorPresent = 0
	var present [2]uint32
	var written uint32
	result, _, _ := whvGetCapability.Call(hypervisorPresent,
		uintptr(unsafe.Pointer(&present[0])), unsafe.Sizeof(present), uintptr(unsafe.Pointer(&written)))
	return result == 0 && written >= 4 && present[0] != 0
}
