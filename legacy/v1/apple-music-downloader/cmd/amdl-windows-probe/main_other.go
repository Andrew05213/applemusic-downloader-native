//go:build !windows

package main

import (
	"encoding/json"
	"os"
)

func main() {
	_ = json.NewEncoder(os.Stdout).Encode(map[string]any{
		"version": 1,
		"supported": false,
		"reason": "windows_only",
	})
}
