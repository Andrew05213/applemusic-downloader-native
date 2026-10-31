//go:build !windows

package main

import (
	"bytes"
	"os"
	"os/exec"
	"path/filepath"
	"strconv"
	"strings"
	"testing"

	"github.com/Andrew05213/applemusic_downloader/internal/secretfd"
)

const (
	unixCredentialHelperEnvironment = "AMDL_TEST_UNIX_CREDENTIAL_HELPER"
	unixCredentialExpectedBytes     = "AMDL_TEST_UNIX_CREDENTIAL_BYTES"
	unixCredentialSentinelToken     = "unix-login-fd-media-token-sentinel"
	unixCredentialSentinelPassword  = "unix-login-fd-password-sentinel"
)

// TestUnixCredentialDescriptorHelper is the child half of the login-fd fixtures.
// It consumes the inherited descriptor through the production reader so the
// tests exercise the same transport the appliance uses. No real Apple
// credential is involved: every value is a fixture sentinel.
func TestUnixCredentialDescriptorHelper(t *testing.T) {
	role := os.Getenv(unixCredentialHelperEnvironment)
	if role == "" {
		return
	}
	if role == "exit-before-read" {
		// Exits without ever reading the descriptor, which is the case that used
		// to leave the parent's write result discarded and unreported.
		os.Exit(0)
	}
	credentials, present, err := secretfd.ReadFromEnvironment()
	if err != nil || !present {
		os.Exit(81)
	}
	if os.Getenv(secretfd.EnvironmentKey) != "" {
		os.Exit(82)
	}
	expected := strings.TrimSpace(os.Getenv(unixCredentialExpectedBytes))
	if expected != "" {
		if strconv.Itoa(len(credentials.MediaUserToken)) != expected {
			os.Exit(83)
		}
	} else if credentials.MediaUserToken != unixCredentialSentinelToken ||
		credentials.ApplePassword != unixCredentialSentinelPassword {
		os.Exit(84)
	}
	os.Exit(0)
}

func newUnixCredentialHelper(t *testing.T, role string, extra ...string) (*exec.Cmd, *bytes.Buffer) {
	t.Helper()
	command := exec.Command(os.Args[0], "-test.run=^TestUnixCredentialDescriptorHelper$")
	command.Env = append(withoutEnvironmentKeys(os.Environ(),
		secretfd.EnvironmentKey,
		processCredentialStdinKey,
		processCredentialsFileEnvironment,
		"APPLE_EMAIL", "APPLE_PASSWORD",
		"MEDIA_USER_TOKEN", "AMDL_MEDIA_USER_TOKEN",
		"AUTHORIZATION_TOKEN", "AMDL_AUTHORIZATION_TOKEN",
	), unixCredentialHelperEnvironment+"="+role)
	command.Env = append(command.Env, extra...)
	output := &bytes.Buffer{}
	command.Stdout = output
	command.Stderr = output
	return command, output
}

func TestUnixLoginFDDeliversTheDocumentThroughTheProductionStartPath(t *testing.T) {
	document, err := secretfd.Encode(secretfd.Credentials{
		AppleEmail:     "fixture@example.test",
		ApplePassword:  unixCredentialSentinelPassword,
		MediaUserToken: unixCredentialSentinelToken,
	})
	if err != nil {
		t.Fatalf("encode credentials: %v", err)
	}
	total := len(document)
	command, output := newUnixCredentialHelper(t, "read")
	delivery := make(chan credentialDeliveryReport, 1)
	if _, err := startCommandWithCredentialDocument(command, document, func(report credentialDeliveryReport) {
		delivery <- report
	}); err != nil {
		t.Fatalf("start helper: %v", err)
	}
	if marker := environmentValue(command.Env, secretfd.EnvironmentKey); marker != "3" {
		t.Fatalf("descriptor marker = %q, want 3", marker)
	}
	public := strings.Join(append(append([]string(nil), command.Args...), command.Env...), "\x00")
	for _, sentinel := range []string{unixCredentialSentinelToken, unixCredentialSentinelPassword} {
		if strings.Contains(public, sentinel) {
			t.Fatal("child argv/environment leaked a credential sentinel")
		}
	}
	if err := command.Wait(); err != nil {
		t.Fatalf("credential helper failed: %v; output=%q", err, output.String())
	}
	report := <-delivery
	if report.Outcome != credentialDeliveryDelivered || report.Err != nil {
		t.Fatalf("delivery outcome = %q (%v), want delivered", report.Outcome, report.Err)
	}
	if report.WrittenBytes != total || report.DocumentBytes != total {
		t.Fatalf("delivery byte counts = %d/%d, want %d/%d", report.WrittenBytes, report.DocumentBytes, total, total)
	}
	assertCredentialDocumentWiped(t, document)
	if line := formatCredentialDeliveryLog("wrapper login-fd", report); strings.Contains(line, unixCredentialSentinelToken) ||
		strings.Contains(line, unixCredentialSentinelPassword) || strings.Contains(line, "{") {
		t.Fatalf("delivery marker leaked document content: %q", line)
	}
	if strings.Contains(output.String(), unixCredentialSentinelToken) {
		t.Fatal("child output leaked a credential sentinel")
	}
}

// TestUnixLoginFDDeliversADocumentLargerThanThePipeBuffer forces the writer to
// loop across several pipe-buffer-sized writes, which is the path a
// single-shot Write cannot cover.
func TestUnixLoginFDDeliversADocumentLargerThanThePipeBuffer(t *testing.T) {
	token := strings.Repeat("t", 120*1024)
	document, err := secretfd.Encode(secretfd.Credentials{MediaUserToken: token})
	if err != nil {
		t.Fatalf("encode credentials: %v", err)
	}
	total := len(document)
	if total <= 64*1024 {
		t.Fatalf("fixture document is only %d bytes; it cannot exceed a pipe buffer", total)
	}
	command, output := newUnixCredentialHelper(t, "read",
		unixCredentialExpectedBytes+"="+strconv.Itoa(len(token)))
	delivery := make(chan credentialDeliveryReport, 1)
	if _, err := startCommandWithCredentialDocument(command, document, func(report credentialDeliveryReport) {
		delivery <- report
	}); err != nil {
		t.Fatalf("start helper: %v", err)
	}
	if err := command.Wait(); err != nil {
		t.Fatalf("large-document helper failed: %v; output=%q", err, output.String())
	}
	report := <-delivery
	if report.Outcome != credentialDeliveryDelivered || report.WrittenBytes != total {
		t.Fatalf("large delivery = %q %d/%d (%v)", report.Outcome, report.WrittenBytes, report.DocumentBytes, report.Err)
	}
	assertCredentialDocumentWiped(t, document)
}

// TestUnixLoginFDReportsAChildThatExitsBeforeReading is the regression test for
// the discarded write result: the child never reads, so the parent must report
// a classified failure instead of silently completing.
func TestUnixLoginFDReportsAChildThatExitsBeforeReading(t *testing.T) {
	document, err := secretfd.Encode(secretfd.Credentials{
		MediaUserToken:     strings.Repeat("m", 96*1024),
		AuthorizationToken: strings.Repeat("a", 96*1024),
	})
	if err != nil {
		t.Fatalf("encode credentials: %v", err)
	}
	total := len(document)
	command, _ := newUnixCredentialHelper(t, "exit-before-read")
	delivery := make(chan credentialDeliveryReport, 1)
	if _, err := startCommandWithCredentialDocument(command, document, func(report credentialDeliveryReport) {
		delivery <- report
	}); err != nil {
		t.Fatalf("start helper: %v", err)
	}
	_ = command.Wait()
	report := <-delivery
	switch report.Outcome {
	case credentialDeliveryPipeClosed:
		if report.Err == nil {
			t.Fatal("a closed pipe was reported without an error")
		}
	case credentialDeliveryDelivered:
		// The kernel can buffer the whole document before the child exits; the
		// contract is only that the count is exact and the buffer is wiped.
	default:
		t.Fatalf("unread descriptor outcome = %q (%v)", report.Outcome, report.Err)
	}
	if report.DocumentBytes != total || report.WrittenBytes > total {
		t.Fatalf("report byte counts = %d/%d, want total %d", report.WrittenBytes, report.DocumentBytes, total)
	}
	assertCredentialDocumentWiped(t, document)
}

// TestUnixLoginFDNumbersTheDescriptorAfterExistingExtraFiles guards the fd
// arithmetic: an already-populated ExtraFiles must shift the exported number
// rather than overwrite an unrelated inherited handle.
func TestUnixLoginFDNumbersTheDescriptorAfterExistingExtraFiles(t *testing.T) {
	document, err := secretfd.Encode(secretfd.Credentials{
		ApplePassword:  unixCredentialSentinelPassword,
		MediaUserToken: unixCredentialSentinelToken,
	})
	if err != nil {
		t.Fatalf("encode credentials: %v", err)
	}
	placeholder, err := os.Open(os.DevNull)
	if err != nil {
		t.Fatalf("open placeholder: %v", err)
	}
	t.Cleanup(func() { _ = placeholder.Close() })
	command, output := newUnixCredentialHelper(t, "read")
	command.ExtraFiles = []*os.File{placeholder}
	delivery := make(chan credentialDeliveryReport, 1)
	if _, err := startCommandWithCredentialDocument(command, document, func(report credentialDeliveryReport) {
		delivery <- report
	}); err != nil {
		t.Fatalf("start helper: %v", err)
	}
	if marker := environmentValue(command.Env, secretfd.EnvironmentKey); marker != "4" {
		t.Fatalf("descriptor marker = %q, want 4", marker)
	}
	if len(command.ExtraFiles) != 2 {
		t.Fatalf("ExtraFiles length = %d, want 2", len(command.ExtraFiles))
	}
	if err := command.Wait(); err != nil {
		t.Fatalf("shifted-descriptor helper failed: %v; output=%q", err, output.String())
	}
	if report := <-delivery; report.Outcome != credentialDeliveryDelivered {
		t.Fatalf("shifted-descriptor outcome = %q (%v)", report.Outcome, report.Err)
	}
	assertCredentialDocumentWiped(t, document)
}

// TestUnixLoginFDWipesTheDocumentWhenTheChildCannotStart covers the pre-exec
// failure path: no descriptor is leaked and the buffer is still cleared.
func TestUnixLoginFDWipesTheDocumentWhenTheChildCannotStart(t *testing.T) {
	document, err := secretfd.Encode(secretfd.Credentials{MediaUserToken: unixCredentialSentinelToken})
	if err != nil {
		t.Fatalf("encode credentials: %v", err)
	}
	command := exec.Command(filepath.Join(t.TempDir(), "absent-binary"))
	command.Env = []string{}
	if _, err := startCommandWithCredentialDocument(command, document, func(credentialDeliveryReport) {
		t.Error("a delivery report was emitted for a child that never started")
	}); err == nil {
		t.Fatal("starting an absent binary succeeded")
	}
	assertCredentialDocumentWiped(t, document)
}

func environmentValue(environment []string, key string) string {
	for _, entry := range environment {
		name, value, found := strings.Cut(entry, "=")
		if found && strings.EqualFold(name, key) {
			return value
		}
	}
	return ""
}
