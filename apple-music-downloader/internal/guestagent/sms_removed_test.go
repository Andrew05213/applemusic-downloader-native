package guestagent

import (
	"context"
	"net/http/httptest"
	"strings"
	"testing"

	"github.com/Andrew05213/applemusic_downloader/internal/nativeauth"
)

func TestRemovedSMSRoutesAndMethodAreRejected(t *testing.T) {
	m := nativeauth.New(emptyRunner{}, func(context.Context, nativeauth.Tokens) (string, error) {
		t.Fatal("removed SMS operation reached verification")
		return "", nil
	})
	defer m.Close()
	_ = m.Restore(context.Background())
	h, err := NewServer(context.Background(), strings.Repeat("a", 64), m, nil, func() {})
	if err != nil {
		t.Fatal(err)
	}
	for _, sample := range []struct {
		path, body string
		status     int
	}{
		{"/session/sms/send", `{}`, 404},
		{"/session/sms/code", `{}`, 404},
		{"/session", `{"appleEmail":"fixture@example.invalid","applePassword":"SENTINEL_PASSWORD","method":"sms"}`, 400},
	} {
		req := httptest.NewRequest("POST", sample.path, strings.NewReader(sample.body))
		req.Header.Set("Authorization", "Bearer "+strings.Repeat("a", 64))
		out := httptest.NewRecorder()
		h.ServeHTTP(out, req)
		if out.Code != sample.status || strings.Contains(out.Body.String(), "SENTINEL") || m.Snapshot().State != "needs_login" {
			t.Fatalf("retired route/method not rejected: %s status=%d", sample.path, out.Code)
		}
	}
}
