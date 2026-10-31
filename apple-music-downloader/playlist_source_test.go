package main

import (
	"strings"
	"testing"
)

func TestParseAppleMusicPlaylistURLClassifiesAndCanonicalizesSources(t *testing.T) {
	tests := []struct {
		name        string
		raw         string
		wantKind    PlaylistSourceKind
		wantID      string
		wantStore   string
		wantSlug    string
		wantURLTail string
	}{
		{
			name:        "catalog",
			raw:         "https://music.apple.com/us/playlist/secret-token-mix/pl.abc_123?l=en-US&itsct=music_box",
			wantKind:    PlaylistSourceCatalog,
			wantID:      "pl.abc_123",
			wantStore:   "us",
			wantSlug:    "secret-token-mix",
			wantURLTail: "/us/playlist/secret-token-mix/pl.abc_123?l=en-US",
		},
		{
			name:        "public user",
			raw:         "https://beta.music.apple.com/gb/playlist/my-personal-token/pl.u-abc-123?uo=4",
			wantKind:    PlaylistSourcePublicUser,
			wantID:      "pl.u-abc-123",
			wantStore:   "gb",
			wantSlug:    "my-personal-token",
			wantURLTail: "/gb/playlist/my-personal-token/pl.u-abc-123",
		},
		{
			name:        "slug collides with legacy song token",
			raw:         "https://music.apple.com/us/playlist/song/pl.u-song-123",
			wantKind:    PlaylistSourcePublicUser,
			wantID:      "pl.u-song-123",
			wantStore:   "us",
			wantSlug:    "song",
			wantURLTail: "/us/playlist/song/pl.u-song-123",
		},
		{
			name:        "library singular",
			raw:         "https://music.apple.com/us/library/playlist/p.987654321?i=7",
			wantKind:    PlaylistSourceLibrary,
			wantID:      "p.987654321",
			wantStore:   "us",
			wantURLTail: "/us/library/playlist/p.987654321?i=7",
		},
		{
			name:        "library plural with slug",
			raw:         "https://music.apple.com/jp/library/playlists/favorites/p.abc-XYZ",
			wantKind:    PlaylistSourceLibrary,
			wantID:      "p.abc-XYZ",
			wantStore:   "jp",
			wantSlug:    "favorites",
			wantURLTail: "/jp/library/playlists/favorites/p.abc-XYZ",
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			source, err := parseAppleMusicPlaylistURL(tt.raw)
			if err != nil {
				t.Fatalf("parseAppleMusicPlaylistURL() error = %v", err)
			}
			if source.Kind != tt.wantKind || source.PlaylistID != tt.wantID || source.Storefront != tt.wantStore || source.Slug != tt.wantSlug {
				t.Fatalf("source = %#v, want kind=%q id=%q storefront=%q slug=%q", source, tt.wantKind, tt.wantID, tt.wantStore, tt.wantSlug)
			}
			if !strings.HasSuffix(source.CanonicalURL, tt.wantURLTail) {
				t.Fatalf("canonical URL = %q, want suffix %q", source.CanonicalURL, tt.wantURLTail)
			}
		})
	}
}

func TestParseAppleMusicPlaylistURLRejectsUnsafeForms(t *testing.T) {
	for _, raw := range []string{
		"https://user:password@music.apple.com/us/playlist/name/pl.abc",
		"https://music.apple.com:443/us/playlist/name/pl.abc",
		"https://music.apple.com/us/playlist/name/pl.abc#token=secret",
		"https://music.apple.com/us/playlist/name/pl.abc#",
		"http://music.apple.com/us/playlist/name/pl.abc",
		"https://example.test/us/playlist/name/pl.abc",
		"https://music.apple.com/us/playlist/name/p.abc",
		"https://music.apple.com/us/library/playlist/pl.abc",
		"https://music.apple.com/us/library/playlist/p.abc?media-user-token=secret",
		"https://music.apple.com/us/playlist/name/pl.abc?unexpected=value",
		"https://music.apple.com/us/playlist/name/pl.abc?l=%ZZ",
		"https://music.apple.com/us/playlist/name/pl.abc?I=1",
		"https://music.apple.com/us/playlist/name/pl.abc?i=123456789012345678901",
		"https://music.apple.com:/us/playlist/name/pl.abc",
		"https://music.apple.com/us/playlist/name/pl.abc?",
		"https://music.apple.com/us/playlist/name/pl.abc?l=\u4e2d\u6587",
		"https://music.apple.com/us/playlist/name%2Fother/pl.abc",
		"https://music.apple.com/us/playlist/./pl.abc",
		"https://music.apple.com/us/library/playlist/../p.abc",
	} {
		t.Run(raw, func(t *testing.T) {
			if _, err := parseAppleMusicPlaylistURL(raw); err == nil {
				t.Fatalf("unsafe URL was accepted: %q", raw)
			}
		})
	}
}

func TestSupportedAppleMusicURLDelegatesToPlaylistParser(t *testing.T) {
	if !isSupportedAppleMusicURL("https://music.apple.com/us/library/playlist/p.abc-123") {
		t.Fatal("library playlist URL was rejected by shared URL validation")
	}
	if !isSupportedAppleMusicURL("https://music.apple.com/us/playlist/secret-token/pl.u-abc-123?l=zh-Hans") {
		t.Fatal("public user playlist URL was rejected by shared URL validation")
	}
}
