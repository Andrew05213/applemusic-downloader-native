//go:build windows

package appliance

// Windows does not support fsync on directory handles.  The Appliance runs
// only on Linux/WSL; this no-op keeps cross-platform unit tests buildable.
func syncDirectory(string) error { return nil }
