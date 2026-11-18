package nativeruntime

import (
	"bytes"
	"strconv"
	"sync"
)

// This is the wire vocabulary from native-runtime/init. No other console text
// is returned to the caller, written to disk, or forwarded to the renderer.
var guestBootEvents = map[string]bool{
	"init_start": false, "modules_ready": false, "network_ready": false,
	"state_mounted": false, "service_start": false,
	"dev_mount_failed": true, "proc_mount_failed": true, "sys_mount_failed": true,
	"run_mount_failed": true, "module_load_failed": true, "loopback_failed": true,
	"network_device_missing": true, "network_address_failed": true, "network_route_failed": true,
	"state_device_missing": true, "data_mount_failed": true, "ca_bundle_missing": true,
	"control_capability_missing": true, "control_capability_copy_failed": true,
	"control_capability_permissions_failed": true, "service_exited": true,
}

type bootLog struct {
	mu       sync.Mutex
	line     [128]byte
	n        int
	oversize bool
	event    string
	terminal bool
}

func (b *bootLog) Write(data []byte) (int, error) {
	b.mu.Lock()
	defer b.mu.Unlock()
	for _, ch := range data {
		if b.terminal {
			break
		}
		if ch == '\n' {
			if !b.oversize {
				b.consume(bytes.TrimSuffix(b.line[:b.n], []byte{'\r'}))
			}
			clear(b.line[:b.n])
			b.n, b.oversize = 0, false
		} else if !b.oversize {
			if b.n == len(b.line) {
				b.oversize = true
			} else {
				b.line[b.n] = ch
				b.n++
			}
		}
	}
	return len(data), nil
}

func (b *bootLog) consume(line []byte) {
	value, ok := bytes.CutPrefix(line, []byte("[amdl-guest] "))
	if !ok {
		return
	}
	if code, legacy := bytes.CutPrefix(value, []byte("service_exit=")); legacy {
		// Older guests report the shell's numeric exit status. It is not
		// the QEMU process status; record only a fixed event for either form.
		if _, err := strconv.ParseUint(string(code), 10, 8); err != nil {
			return
		}
		value = []byte("service_exited")
	}
	if terminal, ok := guestBootEvents[string(value)]; ok {
		b.event, b.terminal = string(value), terminal
	}
}

func (b *bootLog) result() string {
	if b == nil {
		return ""
	}
	b.mu.Lock()
	defer b.mu.Unlock()
	return b.event
}
