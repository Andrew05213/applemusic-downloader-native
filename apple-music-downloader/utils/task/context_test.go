package task

import (
	"context"
	"errors"
	"testing"
)

func TestContextMethodsRejectCanceledContextBeforeRequest(t *testing.T) {
	ctx, cancel := context.WithCancel(context.Background())
	cancel()

	tests := []struct {
		name string
		run  func() error
	}{
		{
			name: "track album data",
			run: func() error {
				return (&Track{Language: "en-US"}).GetAlbumDataContext(ctx, "token")
			},
		},
		{
			name: "album metadata",
			run: func() error {
				return NewAlbum("us", "1").GetRespContext(ctx, "token", "en-US")
			},
		},
		{
			name: "playlist metadata",
			run: func() error {
				return NewPlaylist("us", "pl.example").GetRespContext(ctx, "token", "en-US")
			},
		},
		{
			name: "station metadata",
			run: func() error {
				return NewStation("us", "ra.example").GetRespContext(ctx, "media-user-token", "token", "en-US")
			},
		},
	}

	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			if err := test.run(); !errors.Is(err, context.Canceled) {
				t.Fatalf("error = %v, want context cancellation", err)
			}
		})
	}
}
