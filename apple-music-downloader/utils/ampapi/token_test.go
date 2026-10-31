package ampapi

import (
	"context"
	"encoding/base64"
	"errors"
	"fmt"
	"net/http"
	"net/http/httptest"
	"os"
	"strings"
	"testing"
	"time"
)

func TestClientGetTokenAcceptsCurrentJWTHeaderOrderAndSkipsOriginToken(t *testing.T) {
	wildcardOriginToken := testJWT(
		`{"typ":"JWT","alg":"ES256","kid":"wildcard"}`,
		`{"iss":"issuer","iat":1,"exp":4102444800,"origin":"*.apple.com","root_https_origin":["apple.com"]}`,
	)
	developerToken := testJWT(
		`{"typ":"JWT","alg":"ES256","kid":"catalog"}`,
		`{"iss":"issuer","iat":1,"exp":4102444800,"root_https_origin":["apple.com"]}`,
	)

	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		switch r.URL.Path {
		case "/":
			_, _ = w.Write([]byte(`<script src="/assets/index-legacy~current.js"></script>`))
		case "/assets/index-legacy~current.js":
			_, _ = w.Write([]byte(`window.tokens=["` + wildcardOriginToken + `","` + developerToken + `"];`))
		default:
			http.NotFound(w, r)
		}
	}))
	defer server.Close()

	client := NewClient(server.Client())
	client.WebBaseURL = server.URL
	got, err := client.GetToken(context.Background())
	if err != nil {
		t.Fatalf("GetToken() error = %v", err)
	}
	if got != developerToken {
		t.Fatal("GetToken() did not select the catalog developer token")
	}
}

func TestClientGetTokenSkipsInvalidDeveloperTokenCandidates(t *testing.T) {
	validToken := testJWT(
		`{"typ":"JWT","alg":"ES256","kid":"catalog"}`,
		`{"iss":"issuer","iat":1,"exp":4102444800,"root_https_origin":["apple.com"]}`,
	)
	candidates := []string{
		"YWJj.Zm9v.c2ln",
		testJWT(`{"alg":"HS256"}`, `{"iss":"issuer","iat":1,"exp":4102444800,"root_https_origin":["apple.com"]}`),
		testJWT(`{"alg":"ES256"}`, `{"iat":1,"exp":4102444800,"root_https_origin":["apple.com"]}`),
		testJWT(`{"alg":"ES256"}`, `{"iss":"issuer","exp":4102444800,"root_https_origin":["apple.com"]}`),
		testJWT(`{"alg":"ES256"}`, `{"iss":"issuer","iat":1,"root_https_origin":["apple.com"]}`),
		testJWT(`{"alg":"ES256"}`, `{"iss":"issuer","iat":1,"exp":1,"root_https_origin":["apple.com"]}`),
		testJWT(`{"alg":"ES256"}`, `{"iss":"issuer","iat":1,"exp":4102444800,"root_https_origin":["example.com"]}`),
		validToken,
	}

	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		switch r.URL.Path {
		case "/":
			_, _ = w.Write([]byte(`<script src="/assets/index-legacy-current.js"></script>`))
		case "/assets/index-legacy-current.js":
			_, _ = w.Write([]byte(`window.tokens=["` + strings.Join(candidates, `","`) + `"];`))
		default:
			http.NotFound(w, r)
		}
	}))
	defer server.Close()

	client := NewClient(server.Client())
	client.WebBaseURL = server.URL
	got, err := client.GetToken(context.Background())
	if err != nil {
		t.Fatalf("GetToken() error = %v", err)
	}
	if got != validToken {
		t.Fatal("GetToken() did not skip invalid developer token candidates")
	}
}

// TestClientGetTokenLive is intentionally opt-in because it fetches Apple's
// public web bundle. It never logs the token and is used for release checks.
func TestClientGetTokenLive(t *testing.T) {
	if os.Getenv("AMDL_LIVE_APPLE_TEST") != "1" {
		t.Skip("set AMDL_LIVE_APPLE_TEST=1 to run against Apple's public web bundle")
	}
	ctx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
	defer cancel()

	token, err := GetTokenContext(ctx)
	if err != nil {
		t.Fatalf("GetTokenContext() error = %v", err)
	}
	if token == "" {
		t.Fatal("GetTokenContext() returned an empty token")
	}
}

func testJWT(header, payload string) string {
	return base64.RawURLEncoding.EncodeToString([]byte(header)) + "." +
		base64.RawURLEncoding.EncodeToString([]byte(payload)) + "." +
		base64.RawURLEncoding.EncodeToString([]byte("signature"))
}

func TestClientGetTokenModernAndLegacyEntrypoints(t *testing.T) {
	valid := testJWT(`{"alg":"ES256"}`, `{"iss":"issuer","iat":1,"exp":4102444800,"root_https_origin":["apple.com"]}`)
	for _, name := range []string{"index~c10ba4a68d.js", "index-current.js", "index-legacy~current.js", "index-legacy-current.js"} {
		t.Run(name, func(t *testing.T) {
			server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
				if r.URL.Path == "/" {
					_, _ = w.Write([]byte(`<script crossorigin type="module" src='/assets/` + name + `'></script>`))
					return
				}
				if r.URL.Path == "/assets/"+name {
					_, _ = w.Write([]byte(`const token="` + valid + `"`))
					return
				}
				http.NotFound(w, r)
			}))
			defer server.Close()
			client := NewClient(server.Client())
			client.WebBaseURL = server.URL
			got, err := client.GetToken(context.Background())
			if err != nil || got != valid {
				t.Fatalf("entrypoint failed: %v", err)
			}
		})
	}
}

func TestClientGetTokenTriesOtherEntrypointWithoutFollowingUntrustedScripts(t *testing.T) {
	valid := testJWT(`{"alg":"ES256"}`, `{"iss":"issuer","iat":1,"exp":4102444800,"root_https_origin":["apple.com"]}`)
	var paths []string
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		paths = append(paths, r.URL.Path)
		switch r.URL.Path {
		case "/":
			_, _ = w.Write([]byte(`<p>/assets/index~fake.js</p><script src="https://example.com/assets/index~evil.js"></script><script src="/assets/index~current.js"></script><script src="/assets/index-legacy~current.js"></script><script src="/assets/index~current.js"></script>`))
		case "/assets/index~current.js":
			_, _ = w.Write([]byte(`no eligible token`))
		case "/assets/index-legacy~current.js":
			_, _ = w.Write([]byte(valid))
		default:
			t.Error("unexpected script request")
			http.NotFound(w, r)
		}
	}))
	defer server.Close()
	client := NewClient(server.Client())
	client.WebBaseURL = server.URL
	got, err := client.GetToken(context.Background())
	if err != nil || got != valid {
		t.Fatalf("fallback failed: %v", err)
	}
	if strings.Join(paths, ",") != "/,/assets/index~current.js,/assets/index-legacy~current.js" {
		t.Fatalf("unexpected requests: %v", paths)
	}
}

func TestDeveloperTokenScriptsBoundedAndSameOrigin(t *testing.T) {
	page := `<script data-src="/assets/index~fake.js"></script><script src="//evil.test/assets/index~evil.js"></script><script src="/assets/../assets/index~traversal.js"></script><script src="/assets/index~query.js?token=sentinel"></script><script src="/assets/index~one.js"></script><script src="https://apple.test/assets/index~one.js"></script>`
	for _, name := range []string{"two", "three", "four", "five"} {
		page += `<script src="/assets/index~` + name + `.js"></script>`
	}
	got := developerTokenScripts([]byte(page), "https://apple.test/")
	if len(got) != 4 || got[0] != "https://apple.test/assets/index~one.js" || got[3] != "https://apple.test/assets/index~four.js" {
		t.Fatalf("unsafe or unbounded scripts: %v", got)
	}
}

func TestClientGetTokenCancellationDuringBundleFetch(t *testing.T) {
	entered := make(chan struct{})
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path == "/" {
			fmt.Fprint(w, `<script src="/assets/index~current.js"></script>`)
			return
		}
		close(entered)
		<-r.Context().Done()
	}))
	defer server.Close()
	client := NewClient(server.Client())
	client.WebBaseURL = server.URL
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	done := make(chan error, 1)
	go func() { _, err := client.GetToken(ctx); done <- err }()
	<-entered
	cancel()
	if err := <-done; !errors.Is(err, context.Canceled) {
		t.Fatalf("cancellation lost: %v", err)
	}
}
