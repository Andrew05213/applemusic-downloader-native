package main

import (
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
)

func TestDecodeJSONRejectsNonJSONContentTypes(t *testing.T) {
	request := httptest.NewRequest(http.MethodPost, "/api/v1/jobs", strings.NewReader(`{"urls":["https://music.apple.com/us/album/example/123"]}`))
	request.Header.Set("Content-Type", "text/plain;charset=utf-8")
	var destination map[string]any
	if err := decodeJSON(request, &destination); err == nil {
		t.Fatal("text/plain request was accepted as JSON")
	}
}

func TestDecodeJSONAcceptsJSONContentTypeWithCharset(t *testing.T) {
	request := httptest.NewRequest(http.MethodPost, "/api/v1/jobs", strings.NewReader(`{"quality":"alac"}`))
	request.Header.Set("Content-Type", "application/json; charset=utf-8")
	var destination map[string]string
	if err := decodeJSON(request, &destination); err != nil {
		t.Fatalf("application/json request was rejected: %v", err)
	}
	if destination["quality"] != "alac" {
		t.Fatalf("decoded JSON = %#v", destination)
	}
}
