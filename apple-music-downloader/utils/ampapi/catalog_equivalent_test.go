package ampapi

import (
	"context"
	"errors"
	"fmt"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync/atomic"
	"testing"
)

func TestCatalogEquivalentClassifiesOnlyAuthenticationRejections(t *testing.T) {
	for _, status := range []int{http.StatusUnauthorized, http.StatusForbidden, http.StatusNotFound, http.StatusUnprocessableEntity, http.StatusInternalServerError} {
		t.Run(fmt.Sprint(status), func(t *testing.T) {
			server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
				w.WriteHeader(status)
				fmt.Fprint(w, "private-response-sentinel")
			}))
			defer server.Close()
			client := NewClient(server.Client())
			client.APIBaseURL = server.URL
			_, err := client.GetEquivalentCatalogID(context.Background(), "us", BrowseKindSongs, "456", "developer-sentinel")
			wantAuthInvalid := status == http.StatusUnauthorized || status == http.StatusForbidden
			if !errors.Is(err, ErrCatalogEquivalent) || errors.Is(err, ErrBrowseAuthInvalid) != wantAuthInvalid || strings.Contains(err.Error(), "sentinel") {
				t.Fatalf("unsafe or incorrect equivalent error classification: %v", err)
			}
		})
	}
}

func TestCatalogEquivalentResourcesAndSafeFailure(t *testing.T) {
	for _, resource := range []string{"songs", "albums", "music-videos"} {
		t.Run(resource, func(t *testing.T) {
			var body atomic.Value
			body.Store(fmt.Sprintf(`{"data":[{"id":"999","type":%q,"attributes":{"playParams":{"id":"999"}}}]}`, resource))
			server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
				if r.URL.Path != "/v1/catalog/us/"+resource || r.URL.Query().Get("filter[equivalents]") != "1845873514" {
					t.Error("wrong equivalence request")
				}
				if r.Header.Get("Authorization") != "Bearer developer-sentinel" || r.Header.Get("Media-User-Token") != "" || r.Header.Get("Cookie") != "" {
					t.Error("wrong credential scope")
				}
				fmt.Fprint(w, body.Load().(string))
			}))
			defer server.Close()
			client := NewClient(server.Client())
			client.APIBaseURL = server.URL
			id, err := client.GetEquivalentCatalogID(context.Background(), "us", resource, "1845873514", "developer-sentinel")
			if err != nil || id != "999" {
				t.Fatalf("id=%q err=%v", id, err)
			}
			for _, invalid := range []string{`{"data":[]}`, `{"next":"/more","data":[]}`, `{"data":[{"id":"999","type":"songs"}]}`, `{"data":[{"id":"999","type":"songs","attributes":{"playParams":{"id":"other"}}}]}`, `{"data":[{"id":"999","type":"songs","attributes":{"playParams":{"id":"999"}}},{"id":"888","type":"songs","attributes":{"playParams":{"id":"888"}}}]}`, `developer-sentinel`} {
				body.Store(invalid)
				if _, err = client.GetEquivalentCatalogID(context.Background(), "us", resource, "1845873514", "developer-sentinel"); err == nil || strings.Contains(err.Error(), "sentinel") {
					t.Fatal("unsafe/invalid result accepted")
				}
			}
			ctx, cancel := context.WithCancel(context.Background())
			cancel()
			if _, err = client.GetEquivalentCatalogID(ctx, "us", resource, "1845873514", "developer-sentinel"); err != context.Canceled {
				t.Fatalf("cancellation: %v", err)
			}
		})
	}
}
