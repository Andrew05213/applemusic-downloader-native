package ampapi

import (
	"context"
	"errors"
	"net/http"
	"net/http/httptest"
	"net/url"
	"strings"
	"testing"
)

func TestClientFetchPublicPlaylistDocumentParsesJSONLDWithoutCredentials(t *testing.T) {
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if got := r.Header.Get("Authorization"); got != "" {
			t.Fatalf("public page received Authorization %q", got)
		}
		if got := r.Header.Get("Media-User-Token"); got != "" {
			t.Fatalf("public page received Media-User-Token %q", got)
		}
		if cookie := r.Header.Get("Cookie"); cookie != "" {
			t.Fatalf("public page received Cookie %q", cookie)
		}
		_, _ = w.Write([]byte(`<html><script type="application/ld+json">{"@type":"MusicPlaylist","url":"https://music.apple.com/us/playlist/public/pl.u-abc","name":"Public Mix","numTracks":2,"track":[{"@type":"MusicRecording","name":"One","url":"https://music.apple.com/us/song/one/111"},{"@type":"MusicRecording","name":"Two","url":"https://music.apple.com/us/song/two/222"}]}</script></html>`))
	}))
	defer server.Close()
	client := NewClient(server.Client())
	client.WebBaseURL = server.URL
	document, err := client.FetchPublicPlaylistDocument(context.Background(), server.URL+"/us/playlist/public/pl.u-abc")
	if err != nil {
		t.Fatalf("FetchPublicPlaylistDocument() error = %v", err)
	}
	if document.Name != "Public Mix" || document.TrackCount != 2 || len(document.Tracks) != 2 || document.Tracks[1].CatalogID != "222" {
		t.Fatalf("document = %#v", document)
	}
}

func TestClientFetchPublicPlaylistDocumentRejectsUnrelatedStructuredPlaylist(t *testing.T) {
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		_, _ = w.Write([]byte(`<script type="application/ld+json">{"@type":"MusicPlaylist","url":"https://music.apple.com/us/playlist/other/pl.u-other","name":"Unrelated","numTracks":1,"track":[{"@type":"MusicRecording","name":"Wrong","url":"https://music.apple.com/us/song/wrong/999"}]}</script>`))
	}))
	defer server.Close()
	client := NewClient(server.Client())
	client.WebBaseURL = server.URL
	_, err := client.FetchPublicPlaylistDocument(context.Background(), server.URL+"/us/playlist/target/pl.u-target")
	if !errors.Is(err, ErrPublicPlaylistIncomplete) {
		t.Fatalf("FetchPublicPlaylistDocument() error = %v, want identity mismatch rejection", err)
	}
}

func TestClientFetchPublicPlaylistDocumentSelectsMatchingStructuredPlaylist(t *testing.T) {
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		_, _ = w.Write([]byte(
			`<script type="application/ld+json">{"@type":"MusicPlaylist","url":"https://music.apple.com/us/playlist/other/pl.u-other","name":"Unrelated","numTracks":1,"track":[{"url":"https://music.apple.com/us/song/wrong/999"}]}</script>` +
				`<script type="application/ld+json">{"@type":"MusicPlaylist","url":"https://music.apple.com/us/playlist/target/pl.u-target","name":"Target","numTracks":1,"track":[{"url":"https://music.apple.com/us/song/right/123"}]}</script>`))
	}))
	defer server.Close()
	client := NewClient(server.Client())
	client.WebBaseURL = server.URL
	document, err := client.FetchPublicPlaylistDocument(context.Background(), server.URL+"/us/playlist/target/pl.u-target")
	if err != nil {
		t.Fatalf("FetchPublicPlaylistDocument() error = %v", err)
	}
	if document.PlaylistID != "pl.u-target" || document.Storefront != "us" || len(document.Tracks) != 1 || document.Tracks[0].CatalogID != "123" {
		t.Fatalf("selected document = %#v, want target playlist", document)
	}
}

func TestClientFetchPublicPlaylistDocumentRejectsIncompleteOrOversizedPages(t *testing.T) {
	tests := []struct {
		name string
		body string
		max  int64
		want error
	}{
		{
			name: "incomplete",
			body: `<script type="application/ld+json">{"@type":"MusicPlaylist","name":"Partial","numTracks":3,"track":[{"@type":"MusicRecording","url":"https://music.apple.com/us/song/one/111"}]}</script>`,
			max:  DefaultPublicPageBytes,
			want: ErrPublicPlaylistIncomplete,
		},
		{
			name: "oversized",
			body: strings.Repeat("x", 64),
			max:  8,
			want: ErrResponseTooLarge,
		},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) { _, _ = w.Write([]byte(tt.body)) }))
			defer server.Close()
			client := NewClient(server.Client())
			client.WebBaseURL = server.URL
			client.MaxResponseBytes = tt.max
			_, err := client.FetchPublicPlaylistDocument(context.Background(), server.URL+"/playlist")
			if !errors.Is(err, tt.want) {
				t.Fatalf("error = %v, want %v", err, tt.want)
			}
		})
	}
}

func TestClientFetchPublicPlaylistDocumentFailsClosedOnExplicitIncompleteMarker(t *testing.T) {
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		_, _ = w.Write([]byte(`<script type="application/ld+json">{"@type":"MusicPlaylist","name":"Partial","numTracks":1,"isComplete":false,"track":[{"url":"https://music.apple.com/us/song/one/111"}]}</script>`))
	}))
	defer server.Close()
	client := NewClient(server.Client())
	client.WebBaseURL = server.URL
	_, err := client.FetchPublicPlaylistDocument(context.Background(), server.URL+"/us/playlist/partial/pl.u-abc")
	if !errors.Is(err, ErrPublicPlaylistIncomplete) {
		t.Fatalf("FetchPublicPlaylistDocument() error = %v, want ErrPublicPlaylistIncomplete", err)
	}
}

func TestClientFetchPublicPlaylistDocumentParsesStructuredHydrationEnvelope(t *testing.T) {
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		_, _ = w.Write([]byte(`<script id="serialized-server-data" type="application/json">{"data":{"@type":"MusicPlaylist","url":"https://music.apple.com/us/playlist/hydrated/pl.u-abc","name":"Hydrated","numTracks":1,"track":[{"@type":"MusicRecording","name":"One","url":"https://music.apple.com/us/song/one/333"}]}}</script>`))
	}))
	defer server.Close()
	client := NewClient(server.Client())
	client.WebBaseURL = server.URL
	document, err := client.FetchPublicPlaylistDocument(context.Background(), server.URL+"/us/playlist/hydrated/pl.u-abc")
	if err != nil {
		t.Fatalf("FetchPublicPlaylistDocument() error = %v", err)
	}
	if document.Name != "Hydrated" || len(document.Tracks) != 1 || document.Tracks[0].CatalogID != "333" {
		t.Fatalf("document = %#v", document)
	}
}

func TestCatalogTrackURLRejectsPortFragmentAndQuery(t *testing.T) {
	for _, raw := range []string{
		"https://music.apple.com:443/us/song/name/123",
		"https://music.apple.com/us/song/name/123#fragment",
		"https://music.apple.com/us/song/name/123?token=secret",
		"https://music.apple.com/us/song/name/not-a-number",
		"https://music.apple.com/usa/song/name/123",
		"https://music.apple.com/us/album/name/123",
		"https://evil.example/us/song/name/123",
	} {
		t.Run(raw, func(t *testing.T) {
			if _, err := catalogIDFromPublicTrackURL(raw); err == nil {
				t.Fatalf("unsafe public track URL accepted: %q", raw)
			}
		})
	}
}

func TestPublicPlaylistJSONLDRequiresCompleteTrackCount(t *testing.T) {
	body := []byte(`<script type="application/ld+json">{"@type":"MusicPlaylist","name":"Unknown completeness","track":[{"@type":"MusicRecording","name":"One","url":"https://music.apple.com/us/song/one/123"}]}</script>`)
	if _, err := parsePublicPlaylistJSONLD(body); !errors.Is(err, ErrPublicPlaylistIncomplete) {
		t.Fatalf("parsePublicPlaylistJSONLD() error = %v, want incomplete", err)
	}
}

func TestPublicPlaylistJSONLDRejectsExplicitIncompleteMarkerEvenWhenCountsMatch(t *testing.T) {
	body := []byte(`<script type="application/ld+json">{"@type":"MusicPlaylist","name":"Marked partial","numTracks":1,"isComplete":false,"track":[{"@type":"MusicRecording","name":"One","url":"https://music.apple.com/us/song/one/123"}]}</script>`)
	document, err := parsePublicPlaylistJSONLD(body)
	if err != nil {
		t.Fatalf("parsePublicPlaylistJSONLD() error = %v", err)
	}
	if document.Complete {
		t.Fatalf("explicit isComplete=false was accepted as complete: %#v", document)
	}
}

func TestPublicPlaylistFallbackDoesNotInvokeCredentialBearingRedirectCallback(t *testing.T) {
	redirectCallbackCalled := false
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path == "/us/playlist/redirected/pl.u-redirected" {
			http.Redirect(w, r, "/final", http.StatusFound)
			return
		}
		if r.Header.Get("Authorization") != "" || r.Header.Get("Media-User-Token") != "" || r.Header.Get("Cookie") != "" {
			t.Fatalf("redirect target received credential headers")
		}
		_, _ = w.Write([]byte(`<script type="application/ld+json">{"@type":"MusicPlaylist","url":"https://music.apple.com/us/playlist/redirected/pl.u-redirected","name":"Redirected","numTracks":1,"track":[{"@type":"MusicRecording","name":"One","url":"https://music.apple.com/us/song/one/444"}]}</script>`))
	}))
	defer server.Close()
	client := NewClient(server.Client())
	client.WebBaseURL = server.URL
	client.HTTPClient.CheckRedirect = func(req *http.Request, via []*http.Request) error {
		redirectCallbackCalled = true
		req.Header.Set("Authorization", "Bearer injected")
		req.Header.Set("Cookie", "session=injected")
		return nil
	}
	document, err := client.FetchPublicPlaylistDocument(context.Background(), server.URL+"/us/playlist/redirected/pl.u-redirected")
	if err != nil {
		t.Fatalf("FetchPublicPlaylistDocument() error = %v", err)
	}
	if redirectCallbackCalled || document.TrackCount != 1 || document.Tracks[0].CatalogID != "444" {
		t.Fatalf("callback=%v document=%#v", redirectCallbackCalled, document)
	}
}

func TestPublicPlaylistFallbackRejectsRedirectUserinfoAndFragment(t *testing.T) {
	for _, location := range []string{
		"/final#fragment",
		"https://user:pass@same.example/final",
	} {
		t.Run(location, func(t *testing.T) {
			server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
				http.Redirect(w, r, location, http.StatusFound)
			}))
			defer server.Close()
			client := NewClient(server.Client())
			client.WebBaseURL = server.URL
			_, err := client.FetchPublicPlaylistDocument(context.Background(), server.URL+"/start")
			if err == nil {
				t.Fatal("unsafe redirect was accepted")
			}
		})
	}
}

func TestPublicPlaylistPreservesMediaTypeAndDuplicateOrder(t *testing.T) {
	payload := []byte(`<script type="application/ld+json">{"@type":"MusicPlaylist","name":"Mixed","numTracks":3,"track":[` +
		`{"@type":"MusicRecording","name":"Song","url":"https://music.apple.com/us/song/song/111"},` +
		`{"@type":"VideoObject","name":"Video","url":"https://music.apple.com/us/music-video/video/222"},` +
		`{"@type":"MusicRecording","name":"Song again","url":"https://music.apple.com/us/song/song/111"}` +
		`]}</script>`)
	document, err := parsePublicPlaylistJSONLD(payload)
	if err != nil {
		t.Fatalf("parsePublicPlaylistJSONLD() error = %v", err)
	}
	if !document.Complete || document.TrackCount != 3 || len(document.Tracks) != 3 {
		t.Fatalf("document = %#v", document)
	}
	wantTypes := []string{"songs", "music-videos", "songs"}
	for i, want := range wantTypes {
		if document.Tracks[i].Type != want {
			t.Fatalf("track %d type = %q, want %q", i, document.Tracks[i].Type, want)
		}
	}
	response := document.PlaylistResponse("us", "pl.u-test")
	tracks := response.Data[0].Relationships.Tracks.Data
	if len(tracks) != 3 || tracks[1].Type != "music-videos" || tracks[1].Attributes.PlayParams.Kind != "music-video" {
		t.Fatalf("response tracks = %#v", tracks)
	}
	if tracks[0].ID != tracks[2].ID || tracks[0].Attributes.Name == tracks[2].Attributes.Name {
		t.Fatalf("duplicate/order was not preserved: %#v", tracks)
	}
	if tracks[0].Href != "v1/catalog/us/songs/111" || tracks[1].Href != "v1/catalog/us/music-videos/222" {
		t.Fatalf("public fallback tracks did not receive API hrefs: %#v", tracks)
	}
}

func TestPublicPlaylistSupportsSingleObjectAndNumberOfItemsPartialPage(t *testing.T) {
	payload := []byte(`<script type="application/ld+json">{"@type":"MusicPlaylist","name":"Partial","numberOfItems":4,"track":{"@type":"MusicRecording","name":"Only hydrated item","url":"https://music.apple.com/gb/song/one/333"}}</script>`)
	document, err := parsePublicPlaylistJSONLD(payload)
	if err != nil {
		t.Fatalf("parsePublicPlaylistJSONLD() error = %v", err)
	}
	if document.Complete || document.TrackCount != 4 || len(document.Tracks) != 1 || document.Tracks[0].CatalogID != "333" {
		t.Fatalf("partial document = %#v", document)
	}
}

func TestPublicPlaylistHydrationSupportsAPIShapeAndPartialTracks(t *testing.T) {
	payload := []byte(`{"props":{"data":{"type":"playlists","attributes":{"name":"Hydrated","trackCount":5},"relationships":{"tracks":{"data":[` +
		`{"type":"songs","id":"444","attributes":{"name":"Song","url":"https://music.apple.com/us/song/song/444"}},` +
		`{"type":"music-videos","id":"555","attributes":{"name":"Video","url":"https://music.apple.com/us/music-video/video/555"}}]}}}}}`)
	document, err := parsePublicPlaylistHydration(payload)
	if err != nil {
		t.Fatalf("parsePublicPlaylistHydration() error = %v", err)
	}
	if document.Complete || document.TrackCount != 5 || len(document.Tracks) != 2 {
		t.Fatalf("hydration document = %#v", document)
	}
	if document.Tracks[1].Type != "music-videos" || document.Tracks[1].CatalogID != "555" {
		t.Fatalf("hydrated media type/id = %#v", document.Tracks[1])
	}
}

func TestPublicPlaylistRejectsBareNumTracksPartialAndInvalidTrack(t *testing.T) {
	for _, payload := range []string{
		`{"@type":"MusicPlaylist","name":"Partial","numTracks":3,"track":[{"url":"https://music.apple.com/us/song/one/1"}]}`,
		`{"@type":"MusicPlaylist","name":"Bad","numTracks":1,"track":[{"url":"https://music.apple.com/us/song/one/1?x=1"}]}`,
		`{"@type":"MusicPlaylist","name":"Bad","numTracks":1,"track":[{"url":"https://music.apple.com/us/song/one/1#x"}]}`,
	} {
		if _, err := parsePublicPlaylistPayload([]byte(payload)); !errors.Is(err, ErrPublicPlaylistIncomplete) {
			t.Fatalf("payload accepted or wrong error: %v", err)
		}
	}
}

func TestPublicPlaylistRejectsTrackAndPlaylistCountLimits(t *testing.T) {
	tooMany := make([]string, MaxPublicPlaylistTracks+1)
	for i := range tooMany {
		tooMany[i] = `{"url":"https://music.apple.com/us/song/song/1"}`
	}
	payload := `{"@type":"MusicPlaylist","name":"Huge","numTracks":100001,"track":[` + strings.Join(tooMany, ",") + `]}`
	if _, err := parsePublicPlaylistPayload([]byte(payload)); !errors.Is(err, ErrPublicPlaylistIncomplete) {
		t.Fatalf("oversized playlist accepted: %v", err)
	}
	if _, err := parsePublicPlaylistPayload([]byte(`{"@type":"MusicPlaylist","name":"Huge","numTracks":1,"track":[{"url":"https://music.apple.com/us/song/` + strings.Repeat("x", MaxPublicTrackURLBytes) + `/1"}]}`)); err == nil {
		t.Fatal("oversized track URL accepted")
	}
}

func TestValidatePublicPageURLProductionContract(t *testing.T) {
	base, err := url.Parse("https://beta.music.apple.com")
	if err != nil {
		t.Fatal(err)
	}
	valid, _ := url.Parse("https://music.apple.com/us/playlist/mix/pl.u-abc?l=en-US")
	if err := validatePublicPageURL(valid, base); err != nil {
		t.Fatalf("valid Apple page rejected: %v", err)
	}
	for _, raw := range []string{
		"https://music.apple.com/us/playlist/mix/pl.u-abc?token=secret",
		"https://music.apple.com/us/playlist/mix/pl.u-abc?I=1",
		"https://music.apple.com/us/playlist/mix/pl.u-abc?i=not-a-number",
		"https://music.apple.com/us/playlist/mix/pl.u-abc?l=%E4%B8%AD%E6%96%87",
		"https://music.apple.com/us/playlist/mix/pl.u-abc#fragment",
		"https://music.apple.com/us/playlist/mix/pl.u-abc#",
		"https://music.apple.com:443/us/playlist/mix/pl.u-abc",
		"https://music.apple.com/us/album/mix/pl.u-abc",
		"https://music.apple.com/usa/playlist/mix/pl.u-abc",
	} {
		parsed, _ := url.Parse(raw)
		if err := validatePublicPageURL(parsed, base, raw); err == nil {
			t.Fatalf("unsafe Apple page accepted: %q", raw)
		}
	}
}

func TestPublicPlaylistHydrationUsesDeterministicUnknownKeyOrder(t *testing.T) {
	payload := []byte(`{"z":{"@type":"MusicPlaylist","name":"Zulu","numTracks":1,"track":[{"url":"https://music.apple.com/us/song/zulu/222"}]},"a":{"@type":"MusicPlaylist","name":"Alpha","numTracks":1,"track":[{"url":"https://music.apple.com/us/song/alpha/111"}]}}`)
	for iteration := 0; iteration < 32; iteration++ {
		document, err := parsePublicPlaylistHydration(payload)
		if err != nil {
			t.Fatalf("iteration %d parsePublicPlaylistHydration() error = %v", iteration, err)
		}
		if document.Name != "Alpha" || len(document.Tracks) != 1 || document.Tracks[0].CatalogID != "111" {
			t.Fatalf("iteration %d non-deterministic hydration selection: %#v", iteration, document)
		}
	}
}

func TestPublicPlaylistHydrationSkipsMalformedCandidateForLaterValidResource(t *testing.T) {
	payload := []byte(`{"data":{"type":"playlists","attributes":{"name":"Malformed"},"relationships":{"tracks":{}}},"z":{"@type":"MusicPlaylist","name":"Valid","numTracks":1,"track":[{"url":"https://music.apple.com/us/song/valid/777"}]}}`)
	document, err := parsePublicPlaylistHydration(payload)
	if err != nil {
		t.Fatalf("parsePublicPlaylistHydration() error = %v", err)
	}
	if document.Name != "Valid" || len(document.Tracks) != 1 || document.Tracks[0].CatalogID != "777" {
		t.Fatalf("malformed hydration candidate was not skipped: %#v", document)
	}
}
