package ampapi

import (
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
)

func regionalSong(id, isrc string, duration int, rating string) SongRespData {
	s := SongRespData{ID: id, Type: "songs"}
	s.Attributes.Isrc, s.Attributes.DurationInMillis, s.Attributes.ContentRating = isrc, duration, rating
	s.Attributes.Name, s.Attributes.ArtistName, s.Attributes.AlbumName = "地区标题", "地区艺人", "地区专辑"
	return s
}

func TestRegionalMatchRequiresSameRecordingAndUniqueCandidate(t *testing.T) {
	original := regionalSong("123", "USAAA2400001", 180000, "explicit")
	good := regionalSong("456", "USAAA2400001", 180100, "explicit")
	for _, tc := range []struct {
		name       string
		candidates []SongRespData
		want       bool
	}{
		{"same recording", []SongRespData{good}, true},
		{"missing ISRC", []SongRespData{regionalSong("456", "", 180000, "explicit")}, false},
		{"different ISRC", []SongRespData{regionalSong("456", "USAAA2400002", 180000, "explicit")}, false},
		{"different version", []SongRespData{regionalSong("456", "USAAA2400001", 184000, "explicit")}, false},
		{"clean substitution", []SongRespData{regionalSong("456", "USAAA2400001", 180000, "clean")}, false},
		{"ambiguous editions", []SongRespData{good, regionalSong("789", "USAAA2400001", 180000, "explicit")}, false},
		{"duplicate same resource", []SongRespData{good, good}, true},
		{"empty", nil, false},
	} {
		t.Run(tc.name, func(t *testing.T) {
			_, ok := MatchRegionalSong(original, tc.candidates)
			if ok != tc.want {
				t.Fatalf("matched=%v want=%v", ok, tc.want)
			}
		})
	}
	original.Attributes.Isrc = ""
	if _, ok := MatchRegionalSong(original, []SongRespData{good}); ok {
		t.Fatal("missing original identity matched")
	}
}

func TestRegionalRequestHeadersQueryCancellationAndSafeErrors(t *testing.T) {
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path != "/v1/catalog/cn/songs" || r.URL.Query().Get("filter[equivalents]") != "123" || r.URL.Query().Get("l") != "zh-Hans-CN" {
			t.Errorf("wrong endpoint or query")
		}
		if r.Header.Get("Authorization") != "Bearer sentinel-developer" || r.Header.Get("Cookie") != "" || r.Header.Get("Media-User-Token") != "" {
			t.Errorf("wrong credentials")
		}
		json.NewEncoder(w).Encode(SongResp{Data: []SongRespData{regionalSong("456", "USAAA2400001", 180000, "explicit")}})
	}))
	defer server.Close()
	client := NewClient(server.Client())
	client.APIBaseURL = server.URL
	got, err := client.GetEquivalentSongs(context.Background(), "cn", "123", "zh-Hans-CN", "sentinel-developer")
	if err != nil || len(got) != 1 {
		t.Fatalf("result=%v err=%v", got, err)
	}
	ctx, cancel := context.WithCancel(context.Background())
	cancel()
	if _, err = client.GetEquivalentSongs(ctx, "cn", "123", "", "sentinel-developer"); err == nil {
		t.Fatal("cancel ignored")
	}
	if _, err = client.GetEquivalentSongs(context.Background(), "cn/evil", "123", "", "sentinel-developer"); err == nil || strings.Contains(err.Error(), "sentinel") {
		t.Fatal("invalid input not safely rejected")
	}
}

func TestRegionalErrorsNeverExposeBodiesAndRedirectDoesNotLeakToken(t *testing.T) {
	for _, status := range []int{401, 403, 404, 429, 500} {
		server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
			w.WriteHeader(status)
			w.Write([]byte("sentinel-private-body"))
		}))
		client := NewClient(server.Client())
		client.APIBaseURL = server.URL
		_, err := client.GetEquivalentSongs(context.Background(), "cn", "123", "", "sentinel-developer")
		server.Close()
		if err == nil || strings.Contains(err.Error(), "sentinel") {
			t.Fatalf("status=%d leaked error", status)
		}
	}
	reached := false
	target := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) { reached = true }))
	defer target.Close()
	source := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) { http.Redirect(w, r, target.URL, http.StatusFound) }))
	defer source.Close()
	client := NewClient(source.Client())
	client.APIBaseURL = source.URL
	if _, err := client.GetEquivalentSongs(context.Background(), "cn", "123", "", "sentinel-developer"); err == nil || reached {
		t.Fatal("cross-origin redirect followed")
	}
}
