package main

import (
	"context"
	"encoding/json"
	"io"
	"net/http"
	"net/http/httptest"
	"testing"

	"github.com/Andrew05213/applemusic_downloader/utils/ampapi"
	"github.com/gorilla/mux"
)

func TestCatalogDiscoverUsesPublicRegionalEditorialPage(t *testing.T) {
	const fixture = `<script type="application/json" id="serialized-server-data">
{"data":[{"intent":{"$kind":"BrowsePageIntent","storefront":"cn"},"data":{"sections":[
 {"itemKind":"flowcaseLockup","items":[
  {"title":"编辑歌单","subtitle":"Apple Music","heading":"本周精选","description":"<b>新歌</b> &amp; 专辑","artwork":{"dictionary":{"url":"https://is1-ssl.mzstatic.com/image/{w}x{h}{c}.{f}"}},"contentDescriptor":{"kind":"playlist","identifiers":{"storeAdamID":"pl.0123456789abcdef0123456789abcdef"},"url":"https://example.invalid"}},
  {"title":"不支持的专题","contentDescriptor":{"kind":"multiroom","identifiers":{"storeAdamID":"42"}}}]},
 {"itemKind":"squareLockup","header":{"item":{"titleLink":{"title":"本周新发行"}}},"items":[
  {"titleLinks":[{"title":"新专辑"}],"subtitleLinks":[{"title":"音乐人"}],"artwork":{"dictionary":{"url":"https://example.invalid/art.jpg"}},"contentDescriptor":{"kind":"album","identifiers":{"storeAdamID":"101"}}},
  {"title":"重复项","contentDescriptor":{"kind":"album","identifiers":{"storeAdamID":"101"}}},
  {"title":"无效条目","contentDescriptor":{"kind":"album","identifiers":{"storeAdamID":"../101"}}}]},
 {"itemKind":"trackLockup","header":{"item":{"titleLink":{"title":"本周新歌"}}},"items":[
  {"title":"新歌","artistName":"音乐人","contentDescriptor":{"kind":"song","identifiers":{"storeAdamID":"102"}}},
  {"title":"不可用歌曲","isDisabled":true,"contentDescriptor":{"kind":"song","identifiers":{"storeAdamID":"103"}}}]}
]}}]}</script>`
	upstream := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.RequestURI() != "/cn/new" || r.Header.Get("Authorization") != "" || r.Header.Get("Media-User-Token") != "" || r.Header.Get("Cookie") != "" {
			t.Errorf("unexpected public discover request: %s", r.URL.RequestURI())
		}
		_, _ = io.WriteString(w, fixture)
	}))
	defer upstream.Close()
	client := ampapi.NewClient(upstream.Client())
	client.WebBaseURL = upstream.URL
	router := mux.NewRouter()
	RegisterCatalogRoutes(router, NewCatalogAPI(CatalogAPIDependencies{
		Client:         client,
		DeveloperToken: func(context.Context) (string, error) { t.Error("discovery requested developer token"); return "", nil },
		MediaUserToken: func(context.Context) string { t.Error("discovery requested account token"); return "" },
	}))
	res := httptest.NewRecorder()
	router.ServeHTTP(res, httptest.NewRequest("GET", "/api/v1/catalog/discover?region=cn", nil))
	var data ampapi.BrowseResponse
	if err := json.Unmarshal(res.Body.Bytes(), &data); err != nil || res.Code != 200 || data.Feed != "discover" || data.Storefront != "cn" || len(data.Groups) != 3 {
		t.Fatalf("unexpected response (%d): %s; %v", res.Code, res.Body, err)
	}
	featured, albums, tracks := data.Groups[0], data.Groups[1], data.Groups[2]
	if featured.Layout != "featured" || len(featured.Items) != 1 || albums.Layout != "shelf" || len(albums.Items) != 1 || tracks.Layout != "tracks" || len(tracks.Items) != 1 {
		t.Fatalf("unexpected shelves: %+v", data.Groups)
	}
	if v := featured.Items[0]; v.Description != "新歌 & 专辑" || v.EditorialLabel != "本周精选" || v.URL != "https://music.apple.com/cn/playlist/pl.0123456789abcdef0123456789abcdef" || v.Artwork != "https://is1-ssl.mzstatic.com/image/960x540bb.jpg" {
		t.Fatalf("unexpected featured item: %+v", v)
	}
	if v := albums.Items[0]; v.Title != "新专辑" || v.Artist != "音乐人" || v.Artwork != "" || albums.Title != "本周新发行" || !v.Downloadable {
		t.Fatalf("unexpected album: %+v", v)
	}
	for _, query := range []string{"region=CN", "region=cn&offset=0", "region=cn&region=us", "region=cn&url=https://example.invalid"} {
		res = httptest.NewRecorder()
		router.ServeHTTP(res, httptest.NewRequest("GET", "/api/v1/catalog/discover?"+query, nil))
		if res.Code != 400 {
			t.Errorf("discovery accepted invalid query %q: %d", query, res.Code)
		}
	}
}
