package main

import (
	"os"
	"path/filepath"
)

// The shared Web UI has its own source tree. Never use the native desktop's
// renderer directory as an implicit HTTP file server root.
func resolveWebDirectory(explicit string) string {
	for _, candidate := range []string{explicit, "web", "../web"} {
		if candidate == "" {
			continue
		}
		absolute, err := filepath.Abs(candidate)
		if err != nil {
			continue
		}
		if info, err := os.Stat(absolute); err == nil && info.IsDir() {
			return absolute
		}
	}
	return ""
}
