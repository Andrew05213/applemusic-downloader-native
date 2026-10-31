package main

import (
	"errors"
	"fmt"
	"strings"
	"syscall"
	"testing"

	"github.com/Andrew05213/applemusic_downloader/internal/secretfd"
)

func TestWithoutEnvironmentKeysScrubsGenericAMDLSecretNames(t *testing.T) {
	environment := withoutEnvironmentKeys([]string{
		"SAFE=value",
		"AMDL_API_TOKEN=secret-sentinel",
		"amdl_worker_password=secret-sentinel",
		"AMDL_BRIDGE_SECRET=secret-sentinel",
	})
	if got := strings.Join(environment, "\x00"); got != "SAFE=value" {
		t.Fatalf("generic AMDL secret survived environment scrubber: %q", got)
	}
}

func TestSelectApplianceOutboundModeAllowsOnlyDescriptorMaintenanceWithoutNetwork(t *testing.T) {
	controlToken := strings.Repeat("a", 64)
	legacyMaintenance, err := secretfd.Decode(strings.NewReader(
		`{"controlToken":"` + controlToken + `","startInMaintenance":true}`,
	))
	if err != nil {
		t.Fatalf("decode 1.2.12 maintenance document: %v", err)
	}
	bridge := secretfd.Credentials{
		NetworkProxyURL:   "http://127.0.0.1:43123",
		NetworkGeneration: strings.Repeat("b", 64),
	}
	maintenance := legacyMaintenance

	for _, test := range []struct {
		name              string
		credentials       secretfd.Credentials
		descriptorPresent bool
		stdinPresent      bool
		filePresent       bool
		want              applianceOutboundMode
		wantError         bool
	}{
		{"current supervisor bridge", bridge, true, false, false, applianceOutboundBridge, false},
		{"legacy supervisor maintenance", maintenance, true, false, false, applianceOutboundMaintenanceDisabled, false},
		{"ordinary launch without bridge", secretfd.Credentials{ControlToken: controlToken}, true, false, false, 0, true},
		{"maintenance without descriptor", maintenance, false, false, false, 0, true},
		{"maintenance over stdin", maintenance, true, true, false, 0, true},
		{"maintenance over file", maintenance, true, false, true, 0, true},
		{"maintenance without control token", secretfd.Credentials{StartInMaintenance: true}, true, false, false, 0, true},
		{"maintenance with malformed control token", secretfd.Credentials{StartInMaintenance: true, ControlToken: strings.Repeat("g", 64)}, true, false, false, 0, true},
		{"partial proxy pair", secretfd.Credentials{StartInMaintenance: true, ControlToken: controlToken, NetworkProxyURL: bridge.NetworkProxyURL}, true, false, false, 0, true},
	} {
		t.Run(test.name, func(t *testing.T) {
			mode, err := selectApplianceOutboundMode(test.credentials, test.descriptorPresent, test.stdinPresent, test.filePresent)
			if (err != nil) != test.wantError || mode != test.want {
				t.Fatalf("mode/error = %d/%v, want %d/error=%t", mode, err, test.want, test.wantError)
			}
		})
	}
}

func TestLiteralV1212MaintenanceHealthDescriptorFailsClosedOutsideInheritedFD(t *testing.T) {
	const legacyDocument = `{"controlToken":"aaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaa","startInMaintenance":true}`
	legacyCredentials, err := secretfd.Decode(strings.NewReader(legacyDocument))
	if err != nil {
		t.Fatalf("decode literal 1.2.12 maintenance document: %v", err)
	}
	if legacyCredentials.NetworkProxyURL != "" || legacyCredentials.NetworkGeneration != "" ||
		!legacyCredentials.StartInMaintenance || !canonicalProcessControlToken(legacyCredentials.ControlToken) {
		t.Fatal("literal 1.2.12 maintenance document decoded unexpectedly")
	}

	mode, err := selectApplianceOutboundMode(legacyCredentials, true, false, false)
	if err != nil || mode != applianceOutboundMaintenanceDisabled {
		t.Fatalf("inherited 1.2.12 health descriptor mode/error = %d/%v, want disabled/no error", mode, err)
	}

	for _, source := range []struct {
		name              string
		descriptorPresent bool
		stdinPresent      bool
		filePresent       bool
	}{
		{name: "missing inherited descriptor"},
		{name: "stdin credential transport", descriptorPresent: true, stdinPresent: true},
		{name: "file credential transport", descriptorPresent: true, filePresent: true},
	} {
		t.Run(source.name, func(t *testing.T) {
			mode, err := selectApplianceOutboundMode(legacyCredentials, source.descriptorPresent, source.stdinPresent, source.filePresent)
			if err == nil || mode != 0 {
				t.Fatalf("legacy health descriptor source mode/error = %d/%v, want reject", mode, err)
			}
		})
	}

	normal := legacyCredentials
	normal.StartInMaintenance = false
	if mode, err := selectApplianceOutboundMode(normal, true, false, false); err == nil || mode != 0 {
		t.Fatalf("normal descriptor without network capability mode/error = %d/%v, want reject", mode, err)
	}
}

// recordingCredentialPipe is a deterministic stand-in for the write end of the
// one-shot credential pipe.  Every failure mode is expressed as data, so the
// tests never need a sleep or a real child process.
type recordingCredentialPipe struct {
	accepted     []byte
	chunk        int
	zeroProgress bool
	writeErr     error
	failAfter    int
	closes       int
	closeErr     error
}

func (p *recordingCredentialPipe) Write(data []byte) (int, error) {
	if p.zeroProgress {
		// Contract violation on purpose: a writer that never makes progress must
		// not be able to spin the delivery goroutine forever.
		return 0, nil
	}
	if p.writeErr != nil && len(p.accepted) >= p.failAfter {
		return 0, p.writeErr
	}
	count := len(data)
	if p.chunk > 0 && count > p.chunk {
		count = p.chunk
	}
	p.accepted = append(p.accepted, data[:count]...)
	if count < len(data) {
		return count, nil
	}
	return count, nil
}

func (p *recordingCredentialPipe) Close() error {
	p.closes++
	return p.closeErr
}

func TestCredentialDocumentDeliveryReportsShortWritesAndErrors(t *testing.T) {
	document := []byte(`{"appleEmail":"fixture@example.test","applePassword":"fixture-password"}`)
	sentinel := "fixture-password"

	t.Run("a chunked pipe is written to completion and reported once", func(t *testing.T) {
		payload := append([]byte(nil), document...)
		pipe := &recordingCredentialPipe{chunk: 7}
		var reports []credentialDeliveryReport
		writeCredentialDocument(pipe, payload, func(report credentialDeliveryReport) {
			reports = append(reports, report)
		})
		if string(pipe.accepted) != string(document) {
			t.Fatalf("child received %d of %d document bytes", len(pipe.accepted), len(document))
		}
		if len(reports) != 1 {
			t.Fatalf("delivery reports = %d, want exactly one", len(reports))
		}
		if reports[0].Outcome != credentialDeliveryDelivered {
			t.Fatalf("outcome = %q, want %q", reports[0].Outcome, credentialDeliveryDelivered)
		}
		if reports[0].WrittenBytes != len(document) || reports[0].DocumentBytes != len(document) {
			t.Fatalf("report byte counts = %d/%d, want %d/%d",
				reports[0].WrittenBytes, reports[0].DocumentBytes, len(document), len(document))
		}
		if reports[0].Err != nil {
			t.Fatalf("successful delivery reported err=%v", reports[0].Err)
		}
		assertCredentialDocumentWiped(t, payload)
		if pipe.closes != 1 {
			t.Fatalf("pipe closed %d times, want exactly once", pipe.closes)
		}
	})

	t.Run("a broken pipe is classified without leaking the document", func(t *testing.T) {
		payload := append([]byte(nil), document...)
		pipe := &recordingCredentialPipe{chunk: 9, failAfter: 9, writeErr: syscall.EPIPE}
		var reports []credentialDeliveryReport
		writeCredentialDocument(pipe, payload, func(report credentialDeliveryReport) {
			reports = append(reports, report)
		})
		if len(reports) != 1 {
			t.Fatalf("delivery reports = %d, want exactly one", len(reports))
		}
		report := reports[0]
		if report.Outcome != credentialDeliveryPipeClosed {
			t.Fatalf("outcome = %q, want %q", report.Outcome, credentialDeliveryPipeClosed)
		}
		if report.WrittenBytes != 9 || report.DocumentBytes != len(document) {
			t.Fatalf("report byte counts = %d/%d, want 9/%d", report.WrittenBytes, report.DocumentBytes, len(document))
		}
		if report.Err == nil {
			t.Fatal("a broken pipe was reported as a successful delivery")
		}
		line := formatCredentialDeliveryLog("wrapper", report)
		if strings.Contains(line, sentinel) || strings.Contains(line, "appleEmail") || strings.Contains(line, "{") {
			t.Fatalf("delivery log leaked document content: %q", line)
		}
		if line != "[credential-fd] wrapper document delivery outcome=pipe_closed progress=partial\n" {
			t.Fatalf("delivery log = %q", line)
		}
		assertCredentialDocumentWiped(t, payload)
		if pipe.closes != 1 {
			t.Fatalf("pipe closed %d times, want exactly once", pipe.closes)
		}
	})

	t.Run("a stalled writer is reported as a short write instead of spinning", func(t *testing.T) {
		payload := append([]byte(nil), document...)
		pipe := &recordingCredentialPipe{zeroProgress: true}
		var reports []credentialDeliveryReport
		writeCredentialDocument(pipe, payload, func(report credentialDeliveryReport) {
			reports = append(reports, report)
		})
		if len(reports) != 1 || reports[0].Outcome != credentialDeliveryShortWrite {
			t.Fatalf("stalled writer reports = %#v, want one short_write", reports)
		}
		if reports[0].WrittenBytes != 0 || reports[0].DocumentBytes != len(document) {
			t.Fatalf("stalled writer byte counts = %d/%d", reports[0].WrittenBytes, reports[0].DocumentBytes)
		}
		assertCredentialDocumentWiped(t, payload)
		if pipe.closes != 1 {
			t.Fatalf("pipe closed %d times, want exactly once", pipe.closes)
		}
	})

	t.Run("a close failure is surfaced after a complete write", func(t *testing.T) {
		payload := append([]byte(nil), document...)
		pipe := &recordingCredentialPipe{closeErr: errors.New("close refused")}
		var reports []credentialDeliveryReport
		writeCredentialDocument(pipe, payload, func(report credentialDeliveryReport) {
			reports = append(reports, report)
		})
		if len(reports) != 1 || reports[0].Outcome != credentialDeliveryCloseFailed {
			t.Fatalf("close failure reports = %#v, want one close_failed", reports)
		}
		if reports[0].WrittenBytes != len(document) {
			t.Fatalf("close failure lost the byte count: %d", reports[0].WrittenBytes)
		}
		assertCredentialDocumentWiped(t, payload)
	})

	t.Run("a panicking pipe still wipes and closes the document", func(t *testing.T) {
		payload := append([]byte(nil), document...)
		pipe := &panickingCredentialPipe{}
		var reports []credentialDeliveryReport
		func() {
			defer func() {
				if recovered := recover(); recovered != nil {
					t.Fatalf("delivery panic escaped the writer: %v", recovered)
				}
			}()
			writeCredentialDocument(pipe, payload, func(report credentialDeliveryReport) {
				reports = append(reports, report)
			})
		}()
		assertCredentialDocumentWiped(t, payload)
		if pipe.closes != 1 {
			t.Fatalf("panicking pipe closed %d times, want exactly once", pipe.closes)
		}
		if len(reports) != 1 || reports[0].Outcome != credentialDeliveryWriteError {
			t.Fatalf("panicking pipe reports = %#v, want one write_error", reports)
		}
	})

	t.Run("a nil observer is tolerated", func(t *testing.T) {
		payload := append([]byte(nil), document...)
		pipe := &recordingCredentialPipe{}
		writeCredentialDocument(pipe, payload, nil)
		assertCredentialDocumentWiped(t, payload)
		if pipe.closes != 1 {
			t.Fatalf("pipe closed %d times with a nil observer", pipe.closes)
		}
	})
}

func TestCredentialDeliveryLogPublishesOnlyOutcomeAndProgress(t *testing.T) {
	document := []byte(`{"applePassword":"never-log-this-sentinel"}`)
	tests := []struct {
		name     string
		report   credentialDeliveryReport
		outcome  credentialDeliveryOutcome
		progress string
	}{
		{
			name:     "no bytes accepted",
			report:   credentialDeliveryReport{Outcome: credentialDeliveryPipeClosed, WrittenBytes: 0, DocumentBytes: 137},
			outcome:  credentialDeliveryPipeClosed,
			progress: "none",
		},
		{
			name:     "partial document accepted",
			report:   credentialDeliveryReport{Outcome: credentialDeliveryWriteError, WrittenBytes: 59, DocumentBytes: 137},
			outcome:  credentialDeliveryWriteError,
			progress: "partial",
		},
		{
			name:     "complete document accepted",
			report:   credentialDeliveryReport{Outcome: credentialDeliveryCloseFailed, WrittenBytes: 137, DocumentBytes: 137},
			outcome:  credentialDeliveryCloseFailed,
			progress: "complete",
		},
		{
			name:     "unknown outcome fails closed",
			report:   credentialDeliveryReport{Outcome: credentialDeliveryOutcome("length-137-sentinel"), WrittenBytes: 59, DocumentBytes: 137},
			outcome:  credentialDeliveryWriteError,
			progress: "partial",
		},
	}
	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			report := test.report
			report.Err = fmt.Errorf("write |1: %s", document)
			line := formatCredentialDeliveryLog("downloader", report)
			want := fmt.Sprintf("[credential-fd] downloader document delivery outcome=%s progress=%s\n", test.outcome, test.progress)
			if line != want {
				t.Fatalf("delivery log = %q, want %q", line, want)
			}
			for _, forbidden := range []string{
				"never-log-this-sentinel", "applePassword", "written=", "total=", "137", "59", "length-137-sentinel",
			} {
				if strings.Contains(line, forbidden) {
					t.Fatalf("delivery log leaked %q: %q", forbidden, line)
				}
			}
		})
	}

	for _, outcome := range []credentialDeliveryOutcome{
		credentialDeliveryDelivered,
		credentialDeliveryShortWrite,
		credentialDeliveryPipeClosed,
		credentialDeliveryWriteError,
		credentialDeliveryCloseFailed,
	} {
		t.Run(string(outcome)+" remains allowlisted", func(t *testing.T) {
			line := formatCredentialDeliveryLog("downloader", credentialDeliveryReport{
				Outcome:       outcome,
				WrittenBytes:  3,
				DocumentBytes: len(document),
				Err:           fmt.Errorf("write |1: %s", document),
			})
			if !strings.Contains(line, "outcome="+string(outcome)+" progress=partial") {
				t.Fatalf("%s log dropped its fixed outcome: %q", outcome, line)
			}
			if strings.Contains(line, fmt.Sprint(len(document))) || strings.Contains(line, "written=") || strings.Contains(line, "total=") {
				t.Fatalf("%s log exposed an exact byte count: %q", outcome, line)
			}
		})
	}
}

type panickingCredentialPipe struct{ closes int }

func (p *panickingCredentialPipe) Write([]byte) (int, error) { panic("pipe exploded") }

func (p *panickingCredentialPipe) Close() error {
	p.closes++
	return nil
}

// TestCredentialDeliveryContainsCloseAndObserverPanics covers the two panics
// that occur after the write loop has already returned. recover() runs once, so
// a panic raised later by Close or by the observer would otherwise escape the
// goroutine and take down the whole backend on a diagnostic path.
func TestCredentialDeliveryContainsCloseAndObserverPanics(t *testing.T) {
	document := []byte(`{"applePassword":"panic-path-sentinel"}`)

	t.Run("a panicking Close is reported instead of escaping", func(t *testing.T) {
		payload := append([]byte(nil), document...)
		pipe := &closePanicCredentialPipe{}
		var reports []credentialDeliveryReport
		func() {
			defer func() {
				if recovered := recover(); recovered != nil {
					t.Fatalf("Close panic escaped the writer: %v", recovered)
				}
			}()
			writeCredentialDocument(pipe, payload, func(report credentialDeliveryReport) {
				reports = append(reports, report)
			})
		}()
		assertCredentialDocumentWiped(t, payload)
		if len(reports) != 1 || reports[0].Outcome != credentialDeliveryCloseFailed {
			t.Fatalf("close panic reports = %#v, want one close_failed", reports)
		}
		if line := formatCredentialDeliveryLog("wrapper", reports[0]); strings.Contains(line, "panic-path-sentinel") {
			t.Fatalf("close panic log leaked the document: %q", line)
		}
	})

	t.Run("a panicking observer cannot kill the delivery goroutine", func(t *testing.T) {
		payload := append([]byte(nil), document...)
		pipe := &recordingCredentialPipe{}
		observed := 0
		func() {
			defer func() {
				if recovered := recover(); recovered != nil {
					t.Fatalf("observer panic escaped the writer: %v", recovered)
				}
			}()
			writeCredentialDocument(pipe, payload, func(credentialDeliveryReport) {
				observed++
				panic("observer exploded")
			})
		}()
		if observed != 1 {
			t.Fatalf("observer invocations = %d, want 1", observed)
		}
		assertCredentialDocumentWiped(t, payload)
		if pipe.closes != 1 {
			t.Fatalf("pipe closed %d times despite an observer panic", pipe.closes)
		}
	})
}

// TestCredentialDeliveryHandleReapsTheWriter proves a caller that still owns the
// document can wait for the one-shot writer to finish with it. Without this the
// caller's own wipe races the writer's Write and its wipe of the same buffer.
func TestCredentialDeliveryHandleReapsTheWriter(t *testing.T) {
	document := []byte(`{"applePassword":"handle-sentinel"}`)
	entered := make(chan struct{})
	release := make(chan struct{})
	pipe := &gatedCredentialPipe{entered: entered, release: release}
	handle := newCredentialDeliveryHandle()
	go func() { handle.finish(writeCredentialDocument(pipe, document, nil)) }()

	// Wait until the writer is provably inside Write. A scheduler delay must not
	// be able to make this test pass without exercising the blocked-writer state.
	<-entered
	select {
	case <-handle.done:
		t.Fatal("the handle reported completion while the writer was still blocked")
	default:
	}
	close(release)

	report := handle.Wait()
	if report.Outcome != credentialDeliveryDelivered {
		t.Fatalf("handle report = %#v, want delivered", report)
	}
	if report.WrittenBytes != len(document) || report.DocumentBytes != len(document) {
		t.Fatalf("handle byte counts = %d/%d, want %d", report.WrittenBytes, report.DocumentBytes, len(document))
	}
	// Wait must be idempotent: a caller may check the outcome after wiping.
	if again := handle.Wait(); again != report {
		t.Fatalf("second Wait returned a different report: %#v vs %#v", again, report)
	}
	assertCredentialDocumentWiped(t, document)
	if handle := (*credentialDeliveryHandle)(nil); handle.Wait().Outcome != "" {
		t.Fatal("a nil handle must be safe to wait on")
	}
}

type closePanicCredentialPipe struct{ accepted []byte }

func (p *closePanicCredentialPipe) Write(data []byte) (int, error) {
	p.accepted = append(p.accepted, data...)
	return len(data), nil
}

func (p *closePanicCredentialPipe) Close() error { panic("close exploded") }

type gatedCredentialPipe struct {
	entered chan struct{}
	release chan struct{}
	closes  int
}

func (p *gatedCredentialPipe) Write(data []byte) (int, error) {
	close(p.entered)
	<-p.release
	return len(data), nil
}

func (p *gatedCredentialPipe) Close() error {
	p.closes++
	return nil
}

func assertCredentialDocumentWiped(t *testing.T, document []byte) {
	t.Helper()
	for index, value := range document {
		if value != 0 {
			t.Fatalf("credential document byte %d survived delivery", index)
		}
	}
}
