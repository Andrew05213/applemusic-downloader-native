//go:build linux

package guestagent

import (
	"bufio"
	"context"
	"encoding/json"
	"io"
	"net/http"
	"os"
	"os/exec"
	"path/filepath"
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
func (r *LiteRunner) loginInput(ctx context.Context, input any, codes <-chan string, challenge func(), storeBinding bool) error {
	cmd := r.command("--native-login-stdin", "--base-dir", r.base)
	stdin, err := cmd.StdinPipe()
	if err != nil {
		return nativeauth.ErrLoginFailed
	}
	defer stdin.Close()
	stdout, err := cmd.StdoutPipe()
	if err != nil {
		return nativeauth.ErrLoginFailed
	}
	cmd.Stderr = io.Discard
	if cmd.Start() != nil {
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
		_ = cmd.Wait()
		return nativeauth.ErrLoginFailed
	}
	scanner := bufio.NewScanner(stdout)
	scanner.Buffer(make([]byte, 4096), 32768)
	valid := true
	for scanner.Scan() {
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
		return nativeauth.ErrCancelled
	}
	if !valid || waitErr != nil {
		return nativeauth.ErrLoginFailed
	}
	return nil
}
func (r *LiteRunner) Proxy(w http.ResponseWriter, request *http.Request) error {
	return proxyMediaRequest(r.client, r.endpoint, w, request)
}
