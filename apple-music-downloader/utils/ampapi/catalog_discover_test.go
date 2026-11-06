package ampapi

import (
	"context"
	"errors"
	"net/http"
	"net/http/cookiejar"
	"net/http/httptest"
	"net/url"
	"os"
	"testing"
	"time"
)

func TestBrowseDiscoverDoesNotSendCookiesOrFollowRedirects(t *testing.T) {
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path != "/us/new" || r.Header.Get("Cookie") != "" {
			t.Errorf("unexpected request path or cookies: %s", r.URL.Path)
		}
		http.Redirect(w, r, "/unexpected", http.StatusFound)
	}))
	defer server.Close()
	endpoint, _ := url.Parse(server.URL)
	jar, _ := cookiejar.New(nil)
	jar.SetCookies(endpoint, []*http.Cookie{{Name: "fixture", Value: "not-public", Path: "/"}})
	httpClient := server.Client()
	httpClient.Jar = jar
	client := NewClient(httpClient)
	client.WebBaseURL = server.URL
	_, err := client.BrowseCatalog(context.Background(), BrowseRequest{Storefront: "us", Mode: BrowseModeDiscover})
	if !errors.Is(err, ErrBrowseUnavailable) {
		t.Fatalf("unexpected redirect result: %v", err)
	}
}

func TestDecodeDiscoverDistinguishesEmptyAndInvalidPages(t *testing.T) {
	const valid = `<script id='serialized-server-data' type="application/json">{"data":[{"intent":{"$kind":"BrowsePageIntent","storefront":"us"},"data":{"sections":[]}}]}</script>`
	data, err := decodeDiscoverPage([]byte(valid), "us")
	if err != nil || data.Groups == nil || len(data.Groups) != 0 {
		t.Fatalf("empty feed: %+v, %v", data, err)
	}
	for _, body := range []string{valid[:len(valid)-4], `<script id="serialized-server-data">{"data":[]}</script>`, "unexpected HTML"} {
		if _, err := decodeDiscoverPage([]byte(body), "us"); !errors.Is(err, ErrBrowseSchema) {
			t.Errorf("accepted invalid discovery page: %v", err)
		}
	}
	if _, err := decodeDiscoverPage([]byte(valid), "cn"); !errors.Is(err, ErrBrowseSchema) {
		t.Errorf("accepted a different storefront: %v", err)
	}
}

func TestBrowseDiscoverLive(t *testing.T) {
	if os.Getenv("AMDL_TEST_DISCOVER_LIVE") != "1" {
		t.Skip("requires the public Apple Music site")
	}
	for _, region := range []string{"cn", "us"} {
		t.Run(region, func(t *testing.T) {
			ctx, cancel := context.WithTimeout(context.Background(), 12*time.Second)
			defer cancel()
			data, err := NewBrowseClient().BrowseCatalog(ctx, BrowseRequest{Storefront: region, Mode: BrowseModeDiscover})
			if err != nil {
				t.Fatal(err)
			}
			var covers int
			layouts := map[string]int{}
			for _, group := range data.Groups {
				layouts[group.Layout] += len(group.Items)
				for _, item := range group.Items {
					if group.Layout == "featured" && item.Artwork != "" {
						covers++
					}
				}
			}
			if covers == 0 || layouts["shelf"] == 0 || layouts["tracks"] == 0 {
				t.Fatalf("unexpected live feed: layouts=%v, featured covers=%d", layouts, covers)
			}
			t.Logf("layouts=%v, featured covers=%d", layouts, covers)
		})
	}
}
