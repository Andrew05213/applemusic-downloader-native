package main

import (
	"bufio"
	"errors"
	"io"
	"os"
	"strconv"
)

// This environment value is only an inherited pipe descriptor/handle. It
// contains no credentials and is consumed before starting any worker.
const jobProtocolDescriptorEnvironment = "AMDL_JOB_EVENTS_FD"

// Protected by legacyProtocolOutputMu. Production initializes this once in
// the isolated child, before any download workers start.
var legacyJobProtocolWriter io.Writer
var legacyJobProtocolWriteError error

func openChildJobProtocolOutput() (*os.File, error) {
	raw := os.Getenv(jobProtocolDescriptorEnvironment)
	_ = os.Unsetenv(jobProtocolDescriptorEnvironment)
	if raw == "" {
		return nil, nil
	}
	if os.Getenv("AMDL_JOB_CHILD") != "1" {
		return nil, errors.New("job event transport requires an isolated child")
	}
	value, err := strconv.ParseUint(raw, 10, strconv.IntSize)
	if err != nil || value < 3 || strconv.FormatUint(value, 10) != raw {
		return nil, errors.New("invalid job event transport")
	}
	file := os.NewFile(uintptr(value), "job-events")
	if file == nil {
		return nil, errors.New("invalid job event pipe")
	}
	info, err := file.Stat()
	if err != nil || info.Mode()&os.ModeNamedPipe == 0 {
		_ = file.Close()
		return nil, errors.New("invalid job event pipe")
	}
	if err := preventJobProtocolInheritance(file); err != nil {
		_ = file.Close()
		return nil, errors.New("cannot isolate job event pipe")
	}
	return file, nil
}

func writeLegacyJobProtocolLine(fallback io.Writer, line string) error {
	legacyProtocolOutputMu.Lock()
	defer legacyProtocolOutputMu.Unlock()
	writer := legacyJobProtocolWriter
	if writer == nil {
		writer = fallback
	}
	if legacyJobProtocolWriteError != nil {
		return legacyJobProtocolWriteError
	}
	if len(line)+1 > maxJobOutputLineBytes {
		legacyJobProtocolWriteError = errors.New("job event record exceeds limit")
		return legacyJobProtocolWriteError
	}
	n, err := io.WriteString(writer, line+"\n")
	if err == nil && n != len(line)+1 {
		err = io.ErrShortWrite
	}
	if err != nil {
		legacyJobProtocolWriteError = errors.New("job event delivery failed")
	}
	return legacyJobProtocolWriteError
}

// The receipt stream has no progress throttling, ANSI stripping or textual
// redaction that can corrupt JSON. Validate records before passing them to the
// executor's existing typed item/file and safe-summary boundaries. Malformed
// data is drained but never echoed, and makes the runner fail closed.
func streamJobProtocolOutput(reader io.Reader, emit JobEventSink) error {
	scanner := bufio.NewScanner(reader)
	scanner.Buffer(make([]byte, 4096), maxJobOutputLineBytes)
	var failure error
	for scanner.Scan() {
		line := scanner.Text()
		_, item := parseLegacyJobItem(line)
		_, output := parseLegacyJobOutput(line)
		_, summary := parseLegacyJobSummary(line)
		_, metadata := parseLegacyJobMetadata(line)
		if !item && !output && !summary && !metadata {
			failure = errors.New("invalid job event record")
			continue
		}
		// No raw unknown text crosses this private boundary. Recognized records
		// are consumed by safeEmit rather than persisted as public log messages.
		emit(JobEventInput{Type: EventTypeLog, Data: map[string]string{"message": line, "level": "info"}})
	}
	if scanner.Err() != nil {
		_, _ = io.Copy(io.Discard, reader)
		return errors.New("job event stream read failed")
	}
	return failure
}

func jobProtocolEnvironment(environment []string, descriptor uintptr) []string {
	filtered := withoutEnvironmentKeys(environment, jobProtocolDescriptorEnvironment)
	return append(filtered, jobProtocolDescriptorEnvironment+"="+strconv.FormatUint(uint64(descriptor), 10))
}
