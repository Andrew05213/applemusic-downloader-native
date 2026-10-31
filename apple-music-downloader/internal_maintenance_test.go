package main

import (
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"github.com/Andrew05213/applemusic_downloader/internal/secretfd"
	"github.com/gorilla/mux"
)

func TestInternalReadyRouteRequiresTheInheritedControlToken(t *testing.T) {
	previous := inheritedProcessCredentials
	previousPresent := inheritedCredentialsPresent
	t.Cleanup(func() {
		inheritedProcessCredentials = previous
		inheritedCredentialsPresent = previousPresent
	})
	token := strings.Repeat("d", 64)
	inheritedCredentialsPresent = true
	inheritedProcessCredentials = secretfd.Credentials{ControlToken: token}
	router := mux.NewRouter()
	registerInternalReadyRoute(router, func(w http.ResponseWriter, _ *http.Request) {
		w.WriteHeader(http.StatusNoContent)
	})

	for _, authorized := range []bool{false, true} {
		request := httptest.NewRequest(http.MethodGet, internalReadyURL, nil)
		if authorized {
			request.Header.Set(internalControlHeader, token)
		}
		response := httptest.NewRecorder()
		router.ServeHTTP(response, request)
		want := http.StatusNotFound
		if authorized {
			want = http.StatusNoContent
		}
		if response.Code != want {
			t.Fatalf("authorized=%t status=%d want=%d", authorized, response.Code, want)
		}
	}
}
