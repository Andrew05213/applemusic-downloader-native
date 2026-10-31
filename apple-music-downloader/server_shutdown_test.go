package main

import (
	"context"
	"io"
	"net"
	"net/http"
	"sync"
	"testing"
	"time"
)

type shutdownHTTPResult struct {
	status int
	err    error
}

func TestServeHTTPServerWaitsForActiveRequest(t *testing.T) {
	listener, err := net.Listen("tcp4", "127.0.0.1:0")
	if err != nil {
		t.Fatalf("listen: %v", err)
	}

	requestStarted := make(chan struct{})
	releaseRequest := make(chan struct{})
	var releaseOnce sync.Once
	server := &http.Server{Handler: http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		close(requestStarted)
		<-releaseRequest
		w.WriteHeader(http.StatusNoContent)
	})}
	ctx, cancel := context.WithCancel(context.Background())
	t.Cleanup(func() {
		cancel()
		releaseOnce.Do(func() { close(releaseRequest) })
		_ = server.Close()
	})

	serveDone := make(chan error, 1)
	go func() {
		serveDone <- serveHTTPServer(ctx, server, listener, 2*time.Second)
	}()

	transport := &http.Transport{Proxy: nil}
	t.Cleanup(transport.CloseIdleConnections)
	requestDone := make(chan shutdownHTTPResult, 1)
	go func() {
		response, requestErr := (&http.Client{Transport: transport, Timeout: 2 * time.Second}).Get("http://" + listener.Addr().String())
		if requestErr != nil {
			requestDone <- shutdownHTTPResult{err: requestErr}
			return
		}
		_, _ = io.Copy(io.Discard, response.Body)
		closeErr := response.Body.Close()
		requestDone <- shutdownHTTPResult{status: response.StatusCode, err: closeErr}
	}()

	select {
	case <-requestStarted:
	case <-time.After(2 * time.Second):
		t.Fatal("request did not reach the server")
	}

	cancel()
	select {
	case serveErr := <-serveDone:
		t.Fatalf("server returned before its active request completed: %v", serveErr)
	case <-time.After(75 * time.Millisecond):
	}

	releaseOnce.Do(func() { close(releaseRequest) })
	select {
	case result := <-requestDone:
		if result.err != nil {
			t.Fatalf("request failed during graceful shutdown: %v", result.err)
		}
		if result.status != http.StatusNoContent {
			t.Fatalf("response status = %d, want %d", result.status, http.StatusNoContent)
		}
	case <-time.After(2 * time.Second):
		t.Fatal("active request did not finish")
	}
	select {
	case serveErr := <-serveDone:
		if serveErr != nil {
			t.Fatalf("graceful shutdown returned an error: %v", serveErr)
		}
	case <-time.After(2 * time.Second):
		t.Fatal("server did not return after its active request finished")
	}
}

func TestServeHTTPServerCancelsRequestContext(t *testing.T) {
	listener, err := net.Listen("tcp4", "127.0.0.1:0")
	if err != nil {
		t.Fatalf("listen: %v", err)
	}

	serverContext, stopServer := context.WithCancel(context.Background())
	requestStarted := make(chan struct{})
	requestCancelled := make(chan struct{})
	server := newLifecycleHTTPServer(listener.Addr().String(), http.HandlerFunc(func(w http.ResponseWriter, request *http.Request) {
		close(requestStarted)
		<-request.Context().Done()
		close(requestCancelled)
		w.WriteHeader(http.StatusNoContent)
	}), serverContext)
	t.Cleanup(func() {
		stopServer()
		_ = server.Close()
	})

	serveDone := make(chan error, 1)
	go func() {
		serveDone <- serveHTTPServer(serverContext, server, listener, 2*time.Second)
	}()

	transport := &http.Transport{Proxy: nil}
	t.Cleanup(transport.CloseIdleConnections)
	requestDone := make(chan error, 1)
	go func() {
		response, requestErr := (&http.Client{Transport: transport, Timeout: 2 * time.Second}).Get("http://" + listener.Addr().String())
		if requestErr != nil {
			requestDone <- requestErr
			return
		}
		_, _ = io.Copy(io.Discard, response.Body)
		requestDone <- response.Body.Close()
	}()

	select {
	case <-requestStarted:
	case <-time.After(2 * time.Second):
		t.Fatal("request did not reach the server")
	}
	stopServer()

	select {
	case <-requestCancelled:
	case <-time.After(2 * time.Second):
		t.Fatal("server lifetime cancellation did not reach the request context")
	}
	select {
	case requestErr := <-requestDone:
		if requestErr != nil {
			t.Fatalf("request failed while observing lifecycle cancellation: %v", requestErr)
		}
	case <-time.After(2 * time.Second):
		t.Fatal("cancelled request handler did not return")
	}
	select {
	case serveErr := <-serveDone:
		if serveErr != nil {
			t.Fatalf("server shutdown returned an error: %v", serveErr)
		}
	case <-time.After(2 * time.Second):
		t.Fatal("server did not stop after lifecycle cancellation")
	}
}
