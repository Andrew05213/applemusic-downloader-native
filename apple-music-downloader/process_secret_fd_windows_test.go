//go:build windows

package main

import (
	"bytes"
	"os"
	"os/exec"
	"strings"
	"testing"

	"github.com/Andrew05213/applemusic_downloader/internal/secretfd"
)

const (
	windowsMediaTokenSentinel = "windows-stdin-media-token-sentinel"
	windowsAuthTokenSentinel  = "windows-stdin-authorization-token-sentinel"
)

func TestWindowsCredentialStdinHelper(t *testing.T) {
	if os.Getenv("AMDL_TEST_WINDOWS_CREDENTIAL_HELPER") != "1" {
		return
	}
	inheritedProcessCredentials = secretfd.Credentials{}
	inheritedCredentialsPresent = false
	if err := initializeProcessCredentials(); err != nil {
		os.Exit(91)
	}
	if !inheritedCredentialsPresent ||
		inheritedProcessCredentials.MediaUserToken != windowsMediaTokenSentinel ||
		inheritedProcessCredentials.AuthorizationToken != windowsAuthTokenSentinel {
		os.Exit(92)
	}
	if os.Getenv(processCredentialStdinKey) != "" || os.Getenv(secretfd.EnvironmentKey) != "" {
		os.Exit(93)
	}
}

func TestWindowsJobChildTransportsSecretsOnlyThroughAnonymousStdin(t *testing.T) {
	document, err := secretfd.Encode(secretfd.Credentials{
		MediaUserToken:     windowsMediaTokenSentinel,
		AuthorizationToken: windowsAuthTokenSentinel,
	})
	if err != nil {
		t.Fatalf("encode credentials: %v", err)
	}
	command := exec.Command(os.Args[0], "-test.run=^TestWindowsCredentialStdinHelper$")
	command.Env = append(withoutEnvironmentKeys(os.Environ(),
		secretfd.EnvironmentKey,
		processCredentialStdinKey,
		"MEDIA_USER_TOKEN", "AMDL_MEDIA_USER_TOKEN",
		"AUTHORIZATION_TOKEN", "AMDL_AUTHORIZATION_TOKEN",
	),
		"AMDL_TEST_WINDOWS_CREDENTIAL_HELPER=1",
		"AMDL_JOB_CHILD=1",
	)
	var output bytes.Buffer
	command.Stdout = &output
	command.Stderr = &output
	delivery := make(chan credentialDeliveryReport, 1)
	if _, err := startCommandWithCredentialDocument(command, document, func(report credentialDeliveryReport) {
		delivery <- report
	}); err != nil {
		t.Fatalf("start helper: %v", err)
	}
	publicProcessData := strings.Join(append(append([]string(nil), command.Args...), command.Env...), "\x00")
	for _, sentinel := range []string{windowsMediaTokenSentinel, windowsAuthTokenSentinel} {
		if strings.Contains(publicProcessData, sentinel) {
			t.Fatalf("child argv/environment leaked a credential sentinel")
		}
	}
	if err := command.Wait(); err != nil {
		t.Fatalf("credential helper failed: %v; output=%q", err, output.String())
	}
	if strings.Contains(output.String(), windowsMediaTokenSentinel) || strings.Contains(output.String(), windowsAuthTokenSentinel) {
		t.Fatal("child output leaked a credential sentinel")
	}
	// The child already exited, so the writer goroutine has finished and the
	// report is readable without a sleep.
	report := <-delivery
	if report.Outcome != credentialDeliveryDelivered {
		t.Fatalf("delivery outcome = %q (%v), want delivered", report.Outcome, report.Err)
	}
	if report.WrittenBytes != report.DocumentBytes || report.WrittenBytes == 0 {
		t.Fatalf("delivery byte counts = %d/%d", report.WrittenBytes, report.DocumentBytes)
	}
	line := formatCredentialDeliveryLog("downloader", report)
	if strings.Contains(line, windowsMediaTokenSentinel) || strings.Contains(line, windowsAuthTokenSentinel) {
		t.Fatalf("delivery marker leaked a credential sentinel: %q", line)
	}
	for _, value := range document {
		if value != 0 {
			t.Fatal("credential document was not wiped after stdin delivery")
		}
	}
}

// TestWindowsCredentialDeliveryReportsAChildThatNeverReadsStdin drives the real
// production start path against a child that exits before reading, which is the
// failure that previously vanished into a discarded byte count.
func TestWindowsCredentialDeliveryReportsAChildThatNeverReadsStdin(t *testing.T) {
	// A document larger than the pipe buffer guarantees the write is still in
	// flight when the child exits, so the outcome is deterministic.
	document, err := secretfd.Encode(secretfd.Credentials{
		MediaUserToken:     strings.Repeat("m", 96*1024),
		AuthorizationToken: strings.Repeat("a", 96*1024),
	})
	if err != nil {
		t.Fatalf("encode credentials: %v", err)
	}
	total := len(document)
	command := exec.Command(os.Args[0], "-test.run=^$")
	command.Env = append(withoutEnvironmentKeys(os.Environ(),
		secretfd.EnvironmentKey, processCredentialStdinKey, "AMDL_JOB_CHILD",
	), "AMDL_TEST_WINDOWS_CREDENTIAL_HELPER=0")
	var output bytes.Buffer
	command.Stdout = &output
	command.Stderr = &output
	delivery := make(chan credentialDeliveryReport, 1)
	if _, err := startCommandWithCredentialDocument(command, document, func(report credentialDeliveryReport) {
		delivery <- report
	}); err != nil {
		t.Fatalf("start helper: %v", err)
	}
	_ = command.Wait()
	report := <-delivery
	switch report.Outcome {
	case credentialDeliveryPipeClosed, credentialDeliveryDelivered:
	default:
		t.Fatalf("unread stdin outcome = %q (%v)", report.Outcome, report.Err)
	}
	if report.DocumentBytes != total {
		t.Fatalf("report total = %d, want %d", report.DocumentBytes, total)
	}
	if report.WrittenBytes > total {
		t.Fatalf("report claimed %d of %d bytes written", report.WrittenBytes, total)
	}
	if report.Outcome == credentialDeliveryPipeClosed && report.Err == nil {
		t.Fatal("a closed pipe was reported without an error")
	}
	for _, value := range document {
		if value != 0 {
			t.Fatal("credential document was not wiped after a failed delivery")
		}
	}
}

func TestWindowsCredentialStdinRejectsNonJobChildren(t *testing.T) {
	document, err := secretfd.Encode(secretfd.Credentials{
		MediaUserToken:     windowsMediaTokenSentinel,
		AuthorizationToken: windowsAuthTokenSentinel,
	})
	if err != nil {
		t.Fatalf("encode credentials: %v", err)
	}
	command := exec.Command(os.Args[0], "-test.run=^TestWindowsCredentialStdinHelper$")
	command.Env = append(withoutEnvironmentKeys(os.Environ(),
		secretfd.EnvironmentKey,
		processCredentialStdinKey,
		"AMDL_JOB_CHILD",
	), "AMDL_TEST_WINDOWS_CREDENTIAL_HELPER=1")
	var output bytes.Buffer
	command.Stdout = &output
	command.Stderr = &output
	if _, err := startCommandWithCredentialDocument(command, document, nil); err != nil {
		t.Fatalf("start helper: %v", err)
	}
	if err := command.Wait(); err == nil {
		t.Fatal("non-job child accepted stdin credentials")
	}
	if strings.Contains(output.String(), windowsMediaTokenSentinel) || strings.Contains(output.String(), windowsAuthTokenSentinel) {
		t.Fatal("rejected child output leaked a credential sentinel")
	}
}
