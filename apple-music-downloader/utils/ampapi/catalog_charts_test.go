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

// ChartResponse.results contains arrays of charts, unlike SearchResponse.results,
// whose members are individual resource groups. All resources here are synthetic.
func TestBrowseChartsResultsArraysAndResourceOrder(t *testing.T) {
	for _, region := range []string{"us", "cn"} {
		t.Run(region, func(t *testing.T) {
			var calls atomic.Int32
			server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
				calls.Add(1)
				if r.URL.Path != "/v1/catalog/"+region+"/charts" || r.URL.Query().Get("types") != "songs,albums" || r.URL.Query().Get("limit") != "12" || r.URL.Query().Get("offset") != "" {
					t.Errorf("incorrect charts query: %s", r.URL)
				}
				if r.Header.Get("Authorization") != "Bearer fixture" || r.Header.Get("Media-User-Token") != "" || r.Header.Get("Cookie") != "" {
					t.Error("charts must use public developer identity only")
				}
				fmt.Fprint(w, `{"results":{"songs":[{"chart":"most-played","name":"Top Songs","orderId":"most-played:songs","data":[{"id":"102","type":"songs","attributes":{"name":"Second Number, First Rank","artistName":"Fixture Artist","playParams":{"id":"102"}}},{"id":"101","type":"songs","attributes":{"name":"First Number, Second Rank","playParams":{"id":"101"}}}]},{"chart":"another-chart","data":[{"id":"999","type":"songs","attributes":{"name":"Not In First Chart","playParams":{"id":"999"}}}]}],"albums":[{"chart":"most-played","name":"Top Albums","data":[{"id":"201","type":"albums","attributes":{"name":"Fixture Album","playParams":{"id":"201"}}}]}]},"meta":{"results":{"order":["most-played:songs","most-played:albums"]}}}`)
			}))
			defer server.Close()
			client := NewClient(server.Client())
			client.APIBaseURL = server.URL
			result, err := client.BrowseCatalog(context.Background(), BrowseRequest{Storefront: region, Mode: BrowseModeCharts, DeveloperToken: "fixture", MediaUserToken: "unused-user-fixture"})
			if err != nil || result == nil || len(result.Groups) != 2 {
				t.Fatalf("charts=%+v err=%v", result, err)
			}
			if calls.Load() != 1 || result.Storefront != region || result.Groups[0].Type != BrowseKindSongs || result.Groups[1].Type != BrowseKindAlbums {
				t.Fatalf("incorrect groups or unexpected request count: %+v calls=%d", result, calls.Load())
			}
			if len(result.Groups[0].Items) != 2 || result.Groups[0].Items[0].ID != "102" || result.Groups[0].Items[1].ID != "101" || len(result.Groups[1].Items) != 1 || result.Groups[1].Items[0].ID != "201" {
				t.Fatalf("chart ranking changed or chart lists were mixed: %+v", result.Groups)
			}
			if result.Groups[0].NextOffset != nil || result.Groups[0].Items[0].URL != "https://music.apple.com/"+region+"/song/102" {
				t.Fatalf("incorrect normalized chart: %+v", result.Groups[0])
			}
		})
	}
}

func TestBrowseChartsResultsEmptyAndInvalidShapes(t *testing.T) {
	for _, tc := range []struct {
		name, body string
		valid      bool
	}{
		{"empty results", `{"results":{}}`, true},
		{"empty chart arrays", `{"results":{"songs":[],"albums":[]}}`, true},
		{"empty chart data", `{"results":{"songs":[{"data":[]}]}}`, true},
		{"obsolete mock envelope", `{"charts":{"songs":[{"data":[]}]}}`, false},
		{"missing results", `{}`, false},
		{"null results", `{"results":null}`, false},
		{"array envelope", `{"results":[]}`, false},
		{"search shaped result", `{"results":{"songs":{"data":[]}}}`, false},
		{"invalid chart entry", `{"results":{"songs":["private-response-sentinel"]}}`, false},
		{"wrong resource type", `{"results":{"songs":[{"data":[{"id":"201","type":"albums","attributes":{"name":"Fixture"}}]}]}}`, false},
		{"malformed JSON", `{"results":`, false},
	} {
		t.Run(tc.name, func(t *testing.T) {
			server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) { fmt.Fprint(w, tc.body) }))
			defer server.Close()
			client := NewClient(server.Client())
			client.APIBaseURL = server.URL
			result, err := client.BrowseCatalog(context.Background(), BrowseRequest{Storefront: "us", Mode: BrowseModeCharts, DeveloperToken: "fixture"})
			if tc.valid {
				if err != nil || result == nil {
					t.Fatalf("valid empty charts failed: %+v %v", result, err)
				}
				return
			}
			if !errors.Is(err, ErrBrowseSchema) || result != nil || strings.Contains(err.Error(), "private-response-sentinel") {
				t.Fatalf("invalid charts must return a fixed schema error: result=%+v err=%v", result, err)
			}
		})
	}
}
