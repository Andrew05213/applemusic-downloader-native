package appliance

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"os/exec"
	"path/filepath"
	"time"

	"github.com/Andrew05213/applemusic_downloader/internal/buildinfo"
)

func probeActivatedSupervisor(activation *ReleaseActivation) error {
	if activation == nil || activation.finalPath == "" {
		// Empty paths are used only by injected unit-test activators.
		return nil
	}
	binary := filepath.Join(activation.finalPath, "amdl-appliance")
	ctx, cancel := context.WithTimeout(context.Background(), 15*time.Second)
	defer cancel()
	command := exec.CommandContext(ctx, binary, OperationProbe)
	command.Dir = activation.finalPath
	command.Env = []string{"PATH=/usr/bin:/bin", "AMDL_RUNTIME=wsl-appliance"}
	output := &boundedBuffer{maximum: 64 * 1024}
	command.Stdout = output
	command.Stderr = &boundedBuffer{maximum: 64 * 1024}
	if err := command.Run(); err != nil {
		return errors.New("new appliance supervisor probe failed")
	}
	if output.truncated {
		return errors.New("new appliance supervisor probe output is too large")
	}
	var status Status
	if err := json.Unmarshal(output.buffer.Bytes(), &status); err != nil {
		return errors.New("new appliance supervisor probe returned invalid JSON")
	}
	if status.Service != buildinfo.Service || status.Runtime != buildinfo.RuntimeWSLAppliance || status.Version != filepath.Base(activation.finalPath) {
		return errors.New("new appliance supervisor probe identity is invalid")
	}
	return nil
}

type boundedBuffer struct {
	buffer    bytes.Buffer
	maximum   int
	truncated bool
}

func (b *boundedBuffer) Write(data []byte) (int, error) {
	if b.maximum <= 0 || b.buffer.Len() >= b.maximum {
		b.truncated = true
		return len(data), nil
	}
	remaining := b.maximum - b.buffer.Len()
	if len(data) > remaining {
		b.truncated = true
		_, _ = b.buffer.Write(data[:remaining])
		return len(data), nil
	}
	_, _ = b.buffer.Write(data)
	return len(data), nil
}
