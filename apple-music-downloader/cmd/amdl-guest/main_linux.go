//go:build linux

package main

import (
	"context"
	"flag"
	"io"
	"net/http"
	"os"
	"os/signal"
	"strings"
	"syscall"
	"time"

	"github.com/Andrew05213/applemusic_downloader/internal/guestagent"
	"github.com/Andrew05213/applemusic_downloader/internal/nativeauth"
)

func run() error {
	flags := flag.NewFlagSet("amdl-guest", flag.ContinueOnError)
	flags.SetOutput(io.Discard)
	capFile := flags.String("capability-file", "", "")
	base := flags.String("base-dir", "/data", "")
	if flags.Parse(os.Args[1:]) != nil || flags.NArg() != 0 || *capFile != "/run/control.secret" || *base != "/data" {
		return nativeauth.ErrInvalidInput
	}
	raw, err := os.ReadFile(*capFile)
	if err != nil {
		return nativeauth.ErrInvalidInput
	}
	capability := strings.TrimSpace(string(raw))
	for i := range raw {
		raw[i] = 0
	}
	_ = os.Remove(*capFile)
	verifier, err := guestagent.NewVerifier()
	if err != nil {
		return err
	}
	ctx, cancel := signal.NotifyContext(context.Background(), syscall.SIGINT, syscall.SIGTERM)
	defer cancel()
	runner := guestagent.NewLiteRunner(*base)
	session := nativeauth.New(runner, verifier)
	defer session.Close()
	handler, err := guestagent.NewServer(ctx, capability, session, runner.Proxy, cancel)
	if err != nil {
		return err
	}
	server := &http.Server{Addr: "0.0.0.0:12340", Handler: handler, ReadHeaderTimeout: 5 * time.Second, ReadTimeout: 35 * time.Second, WriteTimeout: 90 * time.Second, IdleTimeout: 30 * time.Second, MaxHeaderBytes: 8192}
	done := make(chan error, 1)
	go func() { done <- server.ListenAndServe() }()
	go func() { _ = session.Restore(ctx) }()
	select {
	case <-ctx.Done():
	case err := <-done:
		if err != http.ErrServerClosed {
			return nativeauth.ErrUnverified
		}
	}
	session.Close()
	shutdown, stop := context.WithTimeout(context.Background(), 5*time.Second)
	defer stop()
	_ = server.Shutdown(shutdown)
	return nil
}
func main() {
	if run() != nil {
		_, _ = os.Stderr.WriteString("native_guest_agent_failed\n")
		os.Exit(1)
	}
}
