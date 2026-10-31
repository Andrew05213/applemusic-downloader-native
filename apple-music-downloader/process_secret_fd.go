package main

import (
	"errors"
	"fmt"
	"io"
	"strings"

	"github.com/Andrew05213/applemusic_downloader/internal/secretfd"
)

// processCredentialStdinKey is a non-secret transport marker used only by an
// isolated Windows job child. Windows os/exec cannot inherit ExtraFiles, so
// the credential document is delivered over the child's anonymous stdin
// pipe. The marker contains no credential material and is removed as soon as
// the child starts.
const processCredentialStdinKey = "AMDL_SECRET_STDIN"

// withoutEnvironmentKeys removes keys case-insensitively while preserving all
// unrelated entries and their order.  Secret-bearing variables are scrubbed
// before any appliance child is started.
func withoutEnvironmentKeys(environment []string, keys ...string) []string {
	blocked := make(map[string]struct{}, len(keys))
	for _, key := range keys {
		blocked[strings.ToUpper(key)] = struct{}{}
	}
	filtered := make([]string, 0, len(environment))
	for _, entry := range environment {
		key, _, found := strings.Cut(entry, "=")
		if found {
			if _, remove := blocked[strings.ToUpper(key)]; remove || isSensitiveAMDLChildEnvironmentKey(key) {
				continue
			}
		}
		filtered = append(filtered, entry)
	}
	return filtered
}

func isSensitiveAMDLChildEnvironmentKey(key string) bool {
	key = strings.ToUpper(strings.TrimSpace(key))
	if !strings.HasPrefix(key, "AMDL_") {
		return false
	}
	return strings.HasSuffix(key, "TOKEN") || strings.HasSuffix(key, "PASSWORD") || strings.HasSuffix(key, "SECRET")
}

// credentialDeliveryOutcome is a closed set of fixed tokens describing how the
// one-shot credential document reached, or failed to reach, a child process.
// Exact byte counts remain internal for correctness checks; public diagnostics
// expose only this token and a coarse progress bucket because the JSON document
// length can reveal the combined length of user credentials.
type credentialDeliveryOutcome string

const (
	credentialDeliveryDelivered   credentialDeliveryOutcome = "delivered"
	credentialDeliveryShortWrite  credentialDeliveryOutcome = "short_write"
	credentialDeliveryPipeClosed  credentialDeliveryOutcome = "pipe_closed"
	credentialDeliveryWriteError  credentialDeliveryOutcome = "write_error"
	credentialDeliveryCloseFailed credentialDeliveryOutcome = "close_failed"
)

// credentialDeliveryReport is emitted once per delivery attempt, after the
// document has been wiped and the write end closed.
type credentialDeliveryReport struct {
	Outcome       credentialDeliveryOutcome
	WrittenBytes  int
	DocumentBytes int
	// Err is retained for the caller's own error handling. It must never be
	// formatted into a log line; use formatCredentialDeliveryLog instead.
	Err error
}

func (r credentialDeliveryReport) delivered() bool {
	return r.Outcome == credentialDeliveryDelivered
}

// credentialDeliveryObserver receives one report per delivery attempt.
type credentialDeliveryObserver func(credentialDeliveryReport)

// credentialDeliveryHandle lets a caller that still owns the document wait for
// the one-shot writer to finish with it. The write must happen after exec so the
// child can drain the pipe, which means the buffer is briefly shared with a
// goroutine; without this handle a caller's own wipe races both that goroutine's
// Write and its wipe of the same bytes.
type credentialDeliveryHandle struct {
	done   chan struct{}
	report credentialDeliveryReport
}

func newCredentialDeliveryHandle() *credentialDeliveryHandle {
	return &credentialDeliveryHandle{done: make(chan struct{})}
}

// finish publishes the report and releases every waiter. The channel close is
// the happens-before edge that makes report visible to Wait.
func (h *credentialDeliveryHandle) finish(report credentialDeliveryReport) {
	if h == nil {
		return
	}
	h.report = report
	close(h.done)
}

// Wait blocks until delivery has completed and the document has been wiped. It
// is idempotent, and safe on a nil handle so a caller need not special-case the
// plain (no credential) start path.
func (h *credentialDeliveryHandle) Wait() credentialDeliveryReport {
	if h == nil {
		return credentialDeliveryReport{}
	}
	<-h.done
	return h.report
}

// writeCredentialDocument writes the whole document to the write end of a
// one-shot credential pipe, then wipes the buffer and closes the pipe on every
// path: partial write, peer that exited before reading, and panic inside the
// writer. A child blocked in read-to-EOF is the failure this guards. Dropping
// progress entirely would make a failed hand-off indistinguishable from a silent
// stall until an initialization watchdog fires; callers therefore publish only
// a coarse bucket, never these exact counts.
func writeCredentialDocument(writer io.WriteCloser, document []byte, observe credentialDeliveryObserver) (report credentialDeliveryReport) {
	report = credentialDeliveryReport{DocumentBytes: len(document)}
	defer func() {
		// Wipe before closing so the buffer cannot outlive delivery even if Close
		// blocks or panics.
		secretfd.Wipe(document)
		if recovered := recover(); recovered != nil && report.Outcome == "" {
			report.Outcome = credentialDeliveryWriteError
			report.Err = fmt.Errorf("credential writer panicked: %v", recovered)
		}
		// recover() above has already run, so a panic raised from here on would
		// escape this goroutine and take the process down on a diagnostic path.
		// Close and the observer are therefore each contained separately.
		if err := closeCredentialPipe(writer); err != nil && report.delivered() {
			report.Outcome = credentialDeliveryCloseFailed
			report.Err = err
		}
		if report.Outcome == "" {
			report.Outcome = credentialDeliveryWriteError
			report.Err = errCredentialDeliveryIncomplete
		}
		notifyCredentialDeliveryObserver(observe, report)
	}()
	for report.WrittenBytes < len(document) {
		count, err := writer.Write(document[report.WrittenBytes:])
		if count > 0 {
			report.WrittenBytes += count
		}
		if err != nil {
			report.Outcome = credentialDeliveryWriteError
			if isCredentialPipeClosed(err) {
				report.Outcome = credentialDeliveryPipeClosed
			}
			report.Err = err
			return
		}
		if count <= 0 {
			// A writer reporting neither progress nor an error would otherwise spin
			// this goroutine for the lifetime of the process.
			report.Outcome = credentialDeliveryShortWrite
			report.Err = fmt.Errorf("credential pipe accepted %d of %d bytes", report.WrittenBytes, len(document))
			return
		}
	}
	report.Outcome = credentialDeliveryDelivered
	return report
}

var errCredentialDeliveryIncomplete = errors.New("credential delivery did not complete")

// closeCredentialPipe closes the write end and converts a panicking Close into
// an ordinary error, so the descriptor is always released exactly once.
func closeCredentialPipe(writer io.Closer) (err error) {
	defer func() {
		if recovered := recover(); recovered != nil {
			err = fmt.Errorf("credential pipe close panicked: %v", recovered)
		}
	}()
	return writer.Close()
}

// notifyCredentialDeliveryObserver delivers the report. An observer is a
// diagnostic sink, so a panic inside one must never propagate into the writer.
func notifyCredentialDeliveryObserver(observe credentialDeliveryObserver, report credentialDeliveryReport) {
	if observe == nil {
		return
	}
	defer func() { _ = recover() }()
	observe(report)
}

// formatCredentialDeliveryLog renders a delivery report as one sanitized line.
// Exact byte counts and report.Err never cross this diagnostic boundary.
func formatCredentialDeliveryLog(channel string, report credentialDeliveryReport) string {
	outcome := report.Outcome
	switch outcome {
	case credentialDeliveryDelivered,
		credentialDeliveryShortWrite,
		credentialDeliveryPipeClosed,
		credentialDeliveryWriteError,
		credentialDeliveryCloseFailed:
	default:
		outcome = credentialDeliveryWriteError
	}
	progress := "partial"
	switch {
	case report.DocumentBytes > 0 && report.WrittenBytes == report.DocumentBytes:
		progress = "complete"
	case report.WrittenBytes <= 0:
		progress = "none"
	}
	return fmt.Sprintf("[credential-fd] %s document delivery outcome=%s progress=%s\n",
		channel, outcome, progress)
}
