package main

// Windows does not expose a portable fsync operation for directory handles.
// The file itself is flushed before the same-volume atomic rename; WSL/DrvFS
// uses the Unix implementation above.
func syncOutputDirectory(string) error { return nil }
