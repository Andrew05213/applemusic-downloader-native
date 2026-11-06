package main

import (
	"context"
	"encoding/json"
	"fmt"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"github.com/Andrew05213/applemusic_downloader/utils/ampapi"
	"github.com/gorilla/mux"
)

func TestCatalogAPIChartsUsesChartResponseResultsArrays(t *testing.T) {
	upstream := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path != "/v1/catalog/cn/charts" || r.URL.Query().Get("types") != "songs,albums" || r.Header.Get("Media-User-Token") != "" || r.Header.Get("Authorization") != "Bearer fixture" {
			t.Error("charts request or public credential boundary changed")
		}
		fmt.Fprint(w, `{"results":{"songs":[{"chart":"most-played","data":[{"id":"102","type":"songs","attributes":{"name":"Fixture Chart Song","playParams":{"id":"102"}}}]}],"albums":[{"chart":"most-played","data":[{"id":"201","type":"albums","attributes":{"name":"Fixture Chart Album","playParams":{"id":"201"}}}]}]}}`)
	}))
	defer upstream.Close()
	client := ampapi.NewClient(upstream.Client())
	client.APIBaseURL = upstream.URL
	router := mux.NewRouter()
	RegisterCatalogRoutes(router, NewCatalogAPI(CatalogAPIDependencies{Client: client,
		DeveloperToken: func(context.Context) (string, error) { return "fixture", nil },
		MediaUserToken: func(context.Context) string { t.Error("charts acquired account credentials"); return "" },
	}))
	res := httptest.NewRecorder()
	router.ServeHTTP(res, httptest.NewRequest(http.MethodGet, "/api/v1/catalog/charts?region=cn", nil))
	var result ampapi.BrowseResponse
	if res.Code != http.StatusOK || json.Unmarshal(res.Body.Bytes(), &result) != nil || len(result.Groups) != 2 {
		t.Fatalf("charts response=%d %s", res.Code, res.Body)
	}
	if result.Storefront != "cn" || result.Groups[0].Type != "songs" || result.Groups[0].Items[0].Title != "Fixture Chart Song" || result.Groups[1].Type != "albums" || result.Groups[1].Items[0].URL != "https://music.apple.com/cn/album/201" {
		t.Fatalf("incorrect charts response: %+v", result)
	}
}

func TestCatalogAPIChartsKeepsMalformedResponsesAsFixedSchemaErrors(t *testing.T) {
	upstream := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		fmt.Fprint(w, `{"results":{"songs":["private-response-sentinel"]}}`)
	}))
	defer upstream.Close()
	client := ampapi.NewClient(upstream.Client())
	client.APIBaseURL = upstream.URL
	router := mux.NewRouter()
	RegisterCatalogRoutes(router, NewCatalogAPI(CatalogAPIDependencies{Client: client, DeveloperToken: func(context.Context) (string, error) { return "fixture", nil }}))
	res := httptest.NewRecorder()
	router.ServeHTTP(res, httptest.NewRequest(http.MethodGet, "/api/v1/catalog/charts?region=us", nil))
	if res.Code != http.StatusBadGateway || !strings.Contains(res.Body.String(), "catalog_schema_invalid") || strings.Contains(res.Body.String(), "private-response-sentinel") {
		t.Fatalf("unsafe or misclassified schema failure: %d %s", res.Code, res.Body)
	}
}
