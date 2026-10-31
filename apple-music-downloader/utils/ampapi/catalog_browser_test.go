package ampapi

import (
	"context"
	"errors"
	"fmt"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
)

func TestBrowseStorefrontUsesOnlyAccountEndpointAndSanitizesFailure(t *testing.T) {
	status := 200
	body := `{"data":[{"id":"us","type":"storefronts"}]}`
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path != "/v1/me/storefront" || r.Header.Get("Authorization") != "Bearer developer-sentinel" || r.Header.Get("Media-User-Token") != "user-sentinel" || r.Header.Get("Cookie") != "" {
			t.Error("incorrect account request")
		}
		w.WriteHeader(status)
		fmt.Fprint(w, body)
	}))
	defer server.Close()
	client := NewClient(server.Client())
	client.APIBaseURL = server.URL
	region, err := client.BrowseStorefront(context.Background(), "developer-sentinel", "user-sentinel")
	if err != nil || region != "us" {
		t.Fatalf("region=%q err=%v", region, err)
	}
	status, body = 403, `user-sentinel private response`
	if _, err = client.BrowseStorefront(context.Background(), "developer-sentinel", "user-sentinel"); !errors.Is(err, ErrBrowseAuthInvalid) || strings.Contains(err.Error(), "sentinel") {
		t.Fatalf("failure=%v", err)
	}
	status, body = 200, `{"data":[{"id":"bad/path","type":"storefronts"}]}`
	if _, err = client.BrowseStorefront(context.Background(), "developer-sentinel", "user-sentinel"); !errors.Is(err, ErrBrowseSchema) {
		t.Fatalf("schema=%v", err)
	}
	ctx, cancel := context.WithCancel(context.Background())
	cancel()
	if _, err = client.BrowseStorefront(ctx, "developer-sentinel", "user-sentinel"); !errors.Is(err, context.Canceled) {
		t.Fatalf("cancellation=%v", err)
	}
}

func TestBrowseCatalogUsesRegionalAPIWithoutUserTokenOrReturnedURL(t *testing.T) {
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path != "/v1/catalog/us/search" || r.URL.Query().Get("term") != "HOYO-MiX" || r.URL.Query().Get("types") != "albums" {
			t.Error("wrong catalog region/search")
		}
		if r.Header.Get("Authorization") != "Bearer developer-sentinel" || r.Header.Get("Media-User-Token") != "" || r.Header.Get("Cookie") != "" {
			t.Error("catalog credential scope")
		}
		fmt.Fprint(w, `{"results":{"albums":{"data":[{"id":"6791462937","type":"albums","attributes":{"name":"Genshin Impact","artistName":"HOYO-MiX","trackCount":92,"url":"https://evil.invalid/user-sentinel","playParams":{"id":"6791462937"},"artwork":{"url":"https://is1-ssl.mzstatic.com/image/{w}x{h}bb.jpg"}}}]}}}`)
	}))
	defer server.Close()
	client := NewClient(server.Client())
	client.APIBaseURL = server.URL
	result, err := client.BrowseCatalog(context.Background(), BrowseRequest{Storefront: "us", Mode: "search", Kind: "albums", Term: "HOYO-MiX", DeveloperToken: "developer-sentinel"})
	if err != nil {
		t.Fatal(err)
	}
	item := result.Groups[0].Items[0]
	if item.URL != "https://music.apple.com/us/album/6791462937" || !item.Downloadable || item.TrackCount != 92 || strings.Contains(item.Artwork, "{") {
		t.Fatalf("incorrect normalized result: %+v", item)
	}
}

func TestBrowseStorefrontRejectsUnrelatedSameOriginRedirect(t *testing.T) {
	var leaked bool
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path == "/v1/me/storefront" {
			http.Redirect(w, r, "/unrelated", http.StatusFound)
			return
		}
		leaked = r.Header.Get("Media-User-Token") != ""
		fmt.Fprint(w, `{"data":[{"id":"us","type":"storefronts"}]}`)
	}))
	defer server.Close()
	client := NewClient(server.Client())
	client.APIBaseURL = server.URL
	_, err := client.BrowseStorefront(context.Background(), "developer-sentinel", "user-sentinel")
	if leaked || err == nil {
		t.Fatal("account token followed an unrelated redirect")
	}
}

func TestBrowseDetailValidatesPaginationAndUnavailableTracks(t *testing.T) {
	body := `{"data":[{"id":"6791462937","type":"albums","attributes":{"name":"Album","playParams":{"id":"6791462937"}},"relationships":{"tracks":{"next":"/v1/catalog/us/albums/6791462937/tracks?offset=100","data":[{"id":"6791462947","type":"songs","attributes":{"name":"Track","playParams":{"id":"6791462947"}}},{"id":"6791462950","type":"songs","attributes":{"name":"Unavailable"}}]}}}]}`
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) { fmt.Fprint(w, body) }))
	defer server.Close()
	client := NewClient(server.Client())
	client.APIBaseURL = server.URL
	request := BrowseRequest{Storefront: "us", Mode: "detail", Kind: "albums", ID: "6791462937", DeveloperToken: "developer-sentinel"}
	result, err := client.BrowseCatalog(context.Background(), request)
	if err != nil || result.NextOffset == nil || *result.NextOffset != 100 || len(result.Tracks) != 2 || result.Tracks[1].Downloadable {
		t.Fatalf("result=%+v err=%v", result, err)
	}
	for _, next := range []string{"https://evil.invalid/private?offset=100", "/v1/catalog/us/albums/other/tracks?offset=100", "/v1/catalog/us/albums/6791462937/tracks?offset=0"} {
		body = strings.Replace(body, "/v1/catalog/us/albums/6791462937/tracks?offset=100", next, 1)
		if _, err = client.BrowseCatalog(context.Background(), request); !errors.Is(err, ErrBrowsePagination) {
			t.Fatalf("unsafe next accepted: %v", err)
		}
		body = strings.Replace(body, next, "/v1/catalog/us/albums/6791462937/tracks?offset=100", 1)
	}
}

func TestBrowseChartsAndAllResourceKinds(t *testing.T) {
	for _, kind := range []string{"songs", "albums", "playlists", "music-videos"} {
		t.Run(kind, func(t *testing.T) {
			id := "123"
			if kind == "playlists" {
				id = "pl.u-fixture"
			}
			resource := fmt.Sprintf(`{"id":%q,"type":%q,"attributes":{"name":"Fixture","playParams":{"id":%q}}}`, id, kind, id)
			server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
				if r.Header.Get("Media-User-Token") != "" || r.Header.Get("Cookie") != "" {
					t.Error("public credentials leaked")
				}
				switch r.URL.Path {
				case "/v1/catalog/us/charts":
					fmt.Fprintf(w, `{"charts":{%q:[{"data":[%s]}]}}`, kind, resource)
				case "/v1/catalog/us/" + kind + "/" + id:
					fmt.Fprintf(w, `{"data":[%s]}`, resource)
				default:
					t.Error("unexpected endpoint")
				}
			}))
			defer server.Close()
			client := NewClient(server.Client())
			client.APIBaseURL = server.URL
			result, err := client.BrowseCatalog(context.Background(), BrowseRequest{Storefront: "us", Mode: "charts", DeveloperToken: "fixture"})
			if err != nil || len(result.Groups) != 1 || result.Groups[0].Items[0].Type != kind {
				t.Fatalf("charts=%+v err=%v", result, err)
			}
			result, err = client.BrowseCatalog(context.Background(), BrowseRequest{Storefront: "us", Mode: "detail", Kind: kind, ID: id, DeveloperToken: "fixture"})
			if err != nil || result.Resource == nil || !result.Resource.Downloadable {
				t.Fatalf("detail=%+v err=%v", result, err)
			}
		})
	}
}

func TestBrowseTrackPageReconstructsRequestAndRejectsMalformedSchema(t *testing.T) {
	body := `{"data":[{"id":"456","type":"songs","attributes":{"name":"Second page","playParams":{"id":"456"}}}]}`
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path != "/v1/catalog/us/albums/123/tracks" || r.URL.Query().Get("offset") != "100" || r.URL.Query().Get("limit") != "100" ||
			r.Header.Get("Authorization") != "Bearer fixture" || r.Header.Get("Media-User-Token") != "" {
			t.Error("incorrect reconstructed page")
		}
		fmt.Fprint(w, body)
	}))
	defer server.Close()
	client := NewClient(server.Client())
	client.APIBaseURL = server.URL
	result, err := client.BrowseCatalog(context.Background(), BrowseRequest{Storefront: "us", Mode: "detail", Kind: "albums", ID: "123", Offset: 100, DeveloperToken: "fixture"})
	if err != nil || result.Resource != nil || len(result.Tracks) != 1 || result.Tracks[0].ID != "456" {
		t.Fatalf("page=%+v err=%v", result, err)
	}
	for _, mode := range []string{"charts", "search"} {
		empty := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) { fmt.Fprint(w, `{}`) }))
		client.APIBaseURL = empty.URL
		_, err = client.BrowseCatalog(context.Background(), BrowseRequest{Storefront: "us", Mode: mode, Term: "hello", DeveloperToken: "fixture"})
		empty.Close()
		if !errors.Is(err, ErrBrowseSchema) {
			t.Fatalf("missing %s envelope accepted: %v", mode, err)
		}
	}
}
