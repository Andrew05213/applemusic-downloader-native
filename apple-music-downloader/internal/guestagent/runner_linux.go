//go:build linux

package guestagent

import (
	"bufio"
	"context"
	"encoding/json"
	"errors"
	"io"
	"net/http"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"sync"
	"syscall"
	"time"

	"github.com/Andrew05213/applemusic_downloader/internal/nativeauth"
)

type LiteRunner struct {
	mu                     sync.Mutex
	base, binary, endpoint string
	client                 *http.Client
	service                *exec.Cmd
	done                   chan struct{}
	env, prefix            []string
}

func NewLiteRunner(base string) *LiteRunner {
	return &LiteRunner{base: base, binary: "/system/bin/lite", endpoint: "http://127.0.0.1:12341", client: &http.Client{Timeout: 30 * time.Second, Transport: &http.Transport{Proxy: nil}, CheckRedirect: func(*http.Request, []*http.Request) error { return http.ErrUseLastResponse }}, env: []string{"ANDROID_ROOT=/system", "ANDROID_DATA=/data", "LD_LIBRARY_PATH=/system/lib64", "PATH=/system/bin:/bin", "TZ=UTC"}}
}
func (r *LiteRunner) HasSession() bool {
	for _, name := range []string{"MUSIC_TOKEN", "token_cache.json"} {
		info, err := os.Lstat(filepath.Join(r.base, name))
		if err == nil && info.Mode().IsRegular() && info.Size() > 0 {
			return true
		}
	}
	return false
}
func (r *LiteRunner) command(args ...string) *exec.Cmd {
	cmd := exec.Command(r.binary, append(append([]string{}, r.prefix...), args...)...)
	cmd.Dir = r.base
	cmd.Env = append([]string{}, r.env...)
	cmd.SysProcAttr = &syscall.SysProcAttr{Setpgid: true}
	return cmd
}
func (r *LiteRunner) stopLocked(ctx context.Context) error {
	if r.service == nil {
		return nil
	}
	cmd, done := r.service, r.done
	_ = syscall.Kill(-cmd.Process.Pid, syscall.SIGTERM)
	select {
	case <-done:
	case <-ctx.Done():
		_ = syscall.Kill(-cmd.Process.Pid, syscall.SIGKILL)
		<-done
	}
	r.service = nil
	r.done = nil
	return nil
}
func (r *LiteRunner) Stop(ctx context.Context) error {
	r.mu.Lock()
	defer r.mu.Unlock()
	return r.stopLocked(ctx)
}
func (r *LiteRunner) StartService(ctx context.Context) error {
	r.mu.Lock()
	defer r.mu.Unlock()
	if r.service != nil {
		select {
		case <-r.done:
			r.service = nil
		default:
			return nil
		}
	}
	cmd := r.command("--native-service", "--base-dir", r.base, "--host", "127.0.0.1", "--port", "12341")
	cmd.Stdout = io.Discard
	cmd.Stderr = io.Discard
	if cmd.Start() != nil {
		return nativeauth.ErrUnverified
	}
	r.service = cmd
	r.done = make(chan struct{})
	done := r.done
	go func() { _ = cmd.Wait(); close(done) }()
	deadline := time.NewTimer(75 * time.Second)
	defer deadline.Stop()
	ticker := time.NewTicker(100 * time.Millisecond)
	defer ticker.Stop()
	for {
		req, _ := http.NewRequestWithContext(ctx, http.MethodGet, r.endpoint+"/status", nil)
		response, err := r.client.Do(req)
		if err == nil {
			_ = response.Body.Close()
			if response.StatusCode == 200 {
				return nil
			}
		}
		select {
		case <-done:
			r.service = nil
			return nativeauth.ErrUnverified
		case <-ctx.Done():
			stop, cancel := context.WithTimeout(context.Background(), 3*time.Second)
			_ = r.stopLocked(stop)
			cancel()
			return nativeauth.ErrCancelled
		case <-deadline.C:
			stop, cancel := context.WithTimeout(context.Background(), 3*time.Second)
			_ = r.stopLocked(stop)
			cancel()
			return nativeauth.ErrUnverified
		case <-ticker.C:
		}
	}
}
func (r *LiteRunner) Tokens(ctx context.Context) (nativeauth.Tokens, error) {
	req, _ := http.NewRequestWithContext(ctx, http.MethodGet, r.endpoint+"/native/tokens", nil)
	response, err := r.client.Do(req)
	if err != nil {
		return nativeauth.Tokens{}, nativeauth.ErrUnverified
	}
	defer response.Body.Close()
	if response.StatusCode == 401 {
		return nativeauth.Tokens{}, nativeauth.ErrAuthRequired
	}
	if response.StatusCode != 200 {
		return nativeauth.Tokens{}, nativeauth.ErrUnverified
	}
	body, err := io.ReadAll(io.LimitReader(response.Body, 256*1024+1))
	if err != nil || len(body) > 256*1024 {
		return nativeauth.Tokens{}, nativeauth.ErrUnverified
	}
	var result struct {
		Code *int `json:"code"`
		Data struct {
			Developer string `json:"developer"`
			Media     string `json:"media"`
			Region    string `json:"region"`
		} `json:"data"`
	}
	if json.Unmarshal(body, &result) != nil || result.Code == nil || *result.Code != 0 || result.Data.Developer == "" || result.Data.Media == "" {
		return nativeauth.Tokens{}, nativeauth.ErrUnverified
	}
	return nativeauth.Tokens{Developer: result.Data.Developer, Media: result.Data.Media, Region: result.Data.Region}, nil
}
func (r *LiteRunner) Login(ctx context.Context, credentials nativeauth.Credentials, codes <-chan string, challenge func()) error {
	return r.loginInput(ctx, credentials, codes, challenge, false)
}

func decodeLiteLoginFailure(line string) (nativeauth.StageEvent, bool) {
	return decodeLiteLoginRecord(line, false)
}
func decodeLiteLoginResponse(line string) (nativeauth.StageEvent, bool) {
	return decodeLiteLoginRecord(line, true)
}
func decodeLiteLoginRecord(line string, response bool) (nativeauth.StageEvent, bool) {
	raw, ok := strings.CutPrefix(line, "AMDL_NATIVE_EVENT:")
	if !ok || len(raw) > 512 {
		return nativeauth.StageEvent{}, false
	}
	var value struct {
		Type       string `json:"type"`
		Reason     string `json:"reason"`
		Response   *uint8 `json:"storeResponseType"`
		Error      *int32 `json:"storeErrorCode"`
		External   *int64 `json:"storeExternalErrorCode"`
		Protocol   *int64 `json:"storeProtocolErrorCode"`
		AuthStatus *int32 `json:"storeAuthStatus"`
		HTTPStatus int    `json:"httpStatus"`
	}
	decoder := json.NewDecoder(strings.NewReader(raw))
	decoder.DisallowUnknownFields()
	expectedType := "login_failure"
	if response {
		expectedType = "login_response"
	}
	if decoder.Decode(&value) != nil || decoder.Decode(new(any)) != io.EOF || value.Type != expectedType || response && value.Reason != "" {
		return nativeauth.StageEvent{}, false
	}
	code := map[string]string{
		"input_invalid": "native_login_input_invalid", "store_bridge_failed": "native_login_store_bridge_failed",
		"store_no_response": "native_login_store_no_response", "store_response_failed": "native_login_store_response_failed",
		"interaction_timeout": "native_login_interaction_timeout", "interaction_unavailable": "native_login_interaction_unavailable", "interaction_unsupported": "native_login_interaction_unsupported", "internal_error": "native_login_internal_error", "token_cache_failed": "native_login_token_cache_failed", "cancelled": "native_auth_cancelled",
	}[value.Reason]
	if !response && (code == "" || (value.Response != nil || value.Error != nil || value.External != nil || value.Protocol != nil || value.AuthStatus != nil || value.HTTPStatus != 0) && value.Reason != "store_response_failed") {
		return nativeauth.StageEvent{}, false
	}
	if value.External != nil && (*value.External < -9007199254740991 || *value.External > 9007199254740991) {
		return nativeauth.StageEvent{}, false
	}
	if value.Protocol != nil && (*value.Protocol < -9007199254740991 || *value.Protocol > 9007199254740991) || value.HTTPStatus != 0 && (value.HTTPStatus < 100 || value.HTTPStatus > 599) {
		return nativeauth.StageEvent{}, false
	}
	event := nativeauth.StageEvent{Code: code, StoreResponseType: value.Response, StoreErrorCode: value.Error, StoreExternalErrorCode: value.External, StoreProtocolErrorCode: value.Protocol, StoreAuthStatus: value.AuthStatus, HTTPStatus: value.HTTPStatus}
	if response {
		event.Stage = "device_login"
		event.Outcome = "waiting"
		event.Code = ""
		event.LoginEvent = "store_response_received"
	}
	return event, true
}

func decodeLiteLoginEvent(line string) (nativeauth.StageEvent, bool) {
	raw, ok := strings.CutPrefix(line, "AMDL_NATIVE_EVENT:")
	if !ok || len(raw) > 128 {
		return nativeauth.StageEvent{}, false
	}
	var value struct {
		Type  string `json:"type"`
		Event string `json:"event"`
	}
	decoder := json.NewDecoder(strings.NewReader(raw))
	decoder.DisallowUnknownFields()
	if decoder.Decode(&value) != nil || decoder.Decode(new(any)) != io.EOF || value.Type != "login_event" || !nativeauth.KnownDeviceLoginEvent(value.Event) {
		return nativeauth.StageEvent{}, false
	}
	return nativeauth.StageEvent{Stage: "device_login", Outcome: "waiting", LoginEvent: value.Event}, true
}

func recordLiteLoginFailure(ctx context.Context, event nativeauth.StageEvent, waitErr error, storeBinding bool) {
	if storeBinding || errors.Is(ctx.Err(), context.Canceled) {
		return
	}
	event.Stage, event.Outcome = "device_login", "failed"
	if errors.Is(ctx.Err(), context.DeadlineExceeded) {
		event = nativeauth.StageEvent{Stage: "device_login", Outcome: "failed", Code: "native_login_process_timeout"}
	}
	var exit *exec.ExitError
	if errors.As(waitErr, &exit) {
		if status, ok := exit.Sys().(syscall.WaitStatus); ok && status.Signaled() {
			if signal := status.Signal(); signal > 0 && signal <= 64 {
				event.ExitSignal = uint8(signal)
			}
			if event.Code == "" {
				event.Code = "native_login_process_signalled"
			}
		} else if code := exit.ExitCode(); code >= 0 && code <= 255 {
			numeric := uint8(code)
			event.ExitCode = &numeric
		}
	}
	if event.Code == "" {
		event.Code = "native_login_process_exited"
	}
	nativeauth.ReportEvent(ctx, event)
}

func (r *LiteRunner) loginInput(ctx context.Context, input any, codes <-chan string, challenge func(), storeBinding bool) error {
	cmd := r.command("--native-login-stdin", "--base-dir", r.base)
	stdin, err := cmd.StdinPipe()
	if err != nil {
		recordLiteLoginFailure(ctx, nativeauth.StageEvent{Code: "native_login_protocol_failed"}, nil, storeBinding)
		return nativeauth.ErrLoginFailed
	}
	defer stdin.Close()
	stdout, err := cmd.StdoutPipe()
	if err != nil {
		recordLiteLoginFailure(ctx, nativeauth.StageEvent{Code: "native_login_protocol_failed"}, nil, storeBinding)
		return nativeauth.ErrLoginFailed
	}
	cmd.Stderr = io.Discard
	if cmd.Start() != nil {
		recordLiteLoginFailure(ctx, nativeauth.StageEvent{Code: "native_login_process_start_failed"}, nil, storeBinding)
		return nativeauth.ErrLoginFailed
	}
	finished := make(chan struct{})
	defer close(finished)
	go func() {
		select {
		case <-ctx.Done():
			_ = syscall.Kill(-cmd.Process.Pid, syscall.SIGKILL)
		case <-finished:
		}
	}()
	secret, err := json.Marshal(input)
	input = nil
	if len(secret) > 32767 {
		err = nativeauth.ErrInvalidInput
	}
	if err == nil {
		secret = append(secret, '\n')
		_, err = stdin.Write(secret)
	}
	for i := range secret {
		secret[i] = 0
	}
	if err != nil {
		_ = syscall.Kill(-cmd.Process.Pid, syscall.SIGKILL)
		waitErr := cmd.Wait()
		recordLiteLoginFailure(ctx, nativeauth.StageEvent{Code: "native_login_protocol_failed"}, waitErr, storeBinding)
		return nativeauth.ErrLoginFailed
	}
	scanner := bufio.NewScanner(stdout)
	scanner.Buffer(make([]byte, 4096), nativeauth.MaxInteractionBytes)
	valid := true
	var failure nativeauth.StageEvent
	eventCount := 0
	for scanner.Scan() {
		if strings.HasPrefix(scanner.Text(), "AMDL_NATIVE_INTERACTION:") {
			q, err := nativeauth.DecodeInteraction(strings.TrimPrefix(scanner.Text(), "AMDL_NATIVE_INTERACTION:"))
			if err != nil || storeBinding {
				valid = false
				failure.Code = "native_login_protocol_failed"
				_ = syscall.Kill(-cmd.Process.Pid, syscall.SIGKILL)
				break
			}
			answer, err := nativeauth.RequestInteraction(ctx, q)
			q = nil
			if err != nil {
				valid = false
				failure.Code = "native_login_interaction_unavailable"
				if errors.Is(err, context.DeadlineExceeded) {
					failure.Code = "native_login_interaction_timeout"
				}
				_ = syscall.Kill(-cmd.Process.Pid, syscall.SIGKILL)
				break
			}
			answer.Scope = ""
			raw, _ := json.Marshal(map[string]any{"type": "interaction_response", "response": answer})
			raw = append(raw, '\n')
			_, err = stdin.Write(raw)
			for i := range raw {
				raw[i] = 0
			}
			answer = nativeauth.InteractionAnswer{}
			if err != nil {
				valid = false
				failure.Code = "native_login_protocol_failed"
				_ = syscall.Kill(-cmd.Process.Pid, syscall.SIGKILL)
				break
			}
			continue
		}
		if event, ok := decodeLiteLoginEvent(scanner.Text()); ok && !storeBinding && eventCount < 128 && failure.Code == "" {
			nativeauth.ReportEvent(ctx, event)
			eventCount++
			if event.LoginEvent == "interaction_answer_rejected" {
				valid = false
				failure.Code = "native_login_input_invalid"
				_ = syscall.Kill(-cmd.Process.Pid, syscall.SIGKILL)
				break
			}
		}
		if event, ok := decodeLiteLoginResponse(scanner.Text()); ok && !storeBinding && eventCount < 128 {
			nativeauth.ReportEvent(ctx, event)
			eventCount++
		}
		if event, ok := decodeLiteLoginFailure(scanner.Text()); ok && failure.Code == "" {
			failure = event
		}
		if scanner.Text() != `AMDL_NATIVE_EVENT:{"type":"two_factor_required"}` {
			continue
		}
		if storeBinding {
			valid = false
			_ = syscall.Kill(-cmd.Process.Pid, syscall.SIGKILL)
			break
		}
		challenge()
		select {
		case code := <-codes:
			input, _ := json.Marshal(map[string]string{"code": code})
			input = append(input, '\n')
			_, err = stdin.Write(input)
			for i := range input {
				input[i] = 0
			}
			if err != nil {
				valid = false
			}
		case <-ctx.Done():
			valid = false
		}
		if !valid {
			_ = syscall.Kill(-cmd.Process.Pid, syscall.SIGKILL)
			break
		}
	}
	if scanner.Err() != nil {
		valid = false
		_ = syscall.Kill(-cmd.Process.Pid, syscall.SIGKILL)
	}
	// Drain completes before Wait; never close a pipe while its scanner is live.
	waitErr := cmd.Wait()
	if ctx.Err() != nil {
		recordLiteLoginFailure(ctx, failure, waitErr, storeBinding)
		return nativeauth.ErrCancelled
	}
	if !valid || waitErr != nil {
		if !valid && failure.Code == "" {
			failure.Code = "native_login_protocol_failed"
		}
		recordLiteLoginFailure(ctx, failure, waitErr, storeBinding)
		return nativeauth.ErrLoginFailed
	}
	return nil
}
func (r *LiteRunner) Proxy(w http.ResponseWriter, request *http.Request) error {
	return proxyMediaRequest(r.client, r.endpoint, w, request)
}
