//go:build !windows || !amd64

package nativeruntime

func hostSupportsWHPX() bool { return false }
