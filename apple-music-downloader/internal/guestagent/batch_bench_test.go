package guestagent

import (
	"context"
	"fmt"
	"io"
	"net"
	"net/http"
	"net/http/httptest"
	"strconv"
	"strings"
	"sync/atomic"
	"testing"
	"time"

	"github.com/Andrew05213/applemusic_downloader/internal/guestclient"
	"github.com/Andrew05213/applemusic_downloader/internal/nativebatch"
)

// This A/B measures transport overhead only. The upstream server echoes
// synthetic samples; there is no SDK, actual audio, or external network.
func BenchmarkThreeDownloadTransports(b *testing.B) {
	const streams, samples, sampleBytes = 3, 128, 9 << 10
	for _, batch := range []bool{false, true} {
		name, width := "single", 1
		if batch {
			name, width = "batch", nativebatch.MaxBlocks
		}
		b.Run(name, func(b *testing.B) {
			ctx, cancel := context.WithCancel(context.Background())
			defer cancel()
			m := verifiedBatchManager(b)
			g := newBatchGateway(m)
			var requests atomic.Int64
			lite := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
				if r.URL.Path == "/native"+nativebatch.Path {
					if !g.authorized(r.Header.Get(batchGrantHeader)) {
						w.WriteHeader(401)
						return
					}
					if r.Method == http.MethodGet {
						_, _ = io.WriteString(w, `{"version":1,"maxBlocks":4,"maxBytes":65536}`)
						return
					}
					requests.Add(1)
					buf, err := io.ReadAll(r.Body)
					if err != nil || !nativebatch.ValidRequest(buf) {
						w.WriteHeader(400)
						return
					}
					for i := byte(0); i < buf[1]; i++ {
						if !g.authorized(r.Header.Get(batchGrantHeader)) {
							w.WriteHeader(401)
							return
						}
					}
					w.Header().Set("Content-Type", nativebatch.ContentType)
					_, _ = w.Write([]byte{1, 0, buf[1], buf[1]})
					_, _ = w.Write(buf[2:])
				} else if r.Method == http.MethodPost {
					requests.Add(1)
					data, err := io.ReadAll(r.Body)
					if err != nil {
						w.WriteHeader(400)
						return
					}
					_, _ = w.Write(data)
				}
			}))
			defer lite.Close()
			key := strings.Repeat("a", 64)
			h, err := NewServer(ctx, key, m, func(w http.ResponseWriter, r *http.Request) error {
				return proxyMediaRequest(lite.Client(), lite.URL, w, r)
			}, cancel)
			if err != nil {
				b.Fatal(err)
			}
			if batch {
				h.Batch = g
			}
			agent := httptest.NewServer(h)
			defer agent.Close()
			_, rawPort, _ := net.SplitHostPort(agent.Listener.Addr().String())
			port, _ := strconv.Atoi(rawPort)
			client, err := guestclient.NewDownloadClient(port, key)
			if err != nil {
				b.Fatal(err)
			}
			defer client.Close()
			const uri = "skd://itunes.apple.com/synthetic"
			b.SetBytes(streams * samples * sampleBytes)
			b.ResetTimer()
			for i := 0; i < b.N; i++ {
				results := make(chan error, streams)
				for track := 0; track < streams; track++ {
					go func() {
						stream := fmt.Sprintf("%032x", track)
						var session *guestclient.DownloadBatch
						var failure error
						defer func() {
							if session != nil {
								session.Close()
							} else if !batch {
								cleanup, cancel := context.WithTimeout(ctx, time.Second)
								client.CloseStream(cleanup, stream)
								cancel()
							}
							results <- failure
						}()
						blocks := make([][]byte, width)
						for i := range blocks {
							blocks[i] = make([]byte, sampleBytes)
						}
						if batch {
							session = client.OpenDownloadBatch(ctx)
							if session == nil {
								failure = fmt.Errorf("fixture did not negotiate")
								return
							}
						}
						for j := 0; j < samples; j += width {
							if batch {
								failure = session.Decrypt(ctx, "42", uri, blocks)
							} else {
								_, failure = client.Decrypt(ctx, stream, "42", uri, blocks[0])
							}
							if failure != nil {
								return
							}
						}
					}()
				}
				for i := 0; i < streams; i++ {
					if err := <-results; err != nil {
						b.Fatal(err)
					}
				}
			}
			b.StopTimer()
			want := int64(b.N * streams * samples / width)
			if got := requests.Load(); got != want {
				b.Fatalf("fixture transport requests = %d, want %d", got, want)
			}
			b.ReportMetric(float64(requests.Load())/float64(b.N), "media-req/op")
		})
	}
}
