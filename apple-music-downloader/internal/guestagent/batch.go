package guestagent

import (
	"bytes"
	"context"
	"crypto/rand"
	"encoding/hex"
	"errors"
	"io"
	"net"
	"net/http"
	"net/url"
	"os"
	"sync"
	"time"

	"github.com/Andrew05213/applemusic_downloader/internal/nativeauth"
	"github.com/Andrew05213/applemusic_downloader/internal/nativebatch"
)

const (
	BatchSocket       = "/run/amdl-native-batch.sock"
	batchGrantHeader  = "X-AMDL-Batch-Grant"
	batchConcurrency  = 3
	batchRequestLimit = 35 * time.Second
)

type batchGrantKey struct{}

type batchGrant struct {
	ctx   context.Context
	epoch uint64
}

// BatchGateway is the only issuer of grants for the private Lite routes. The
// grant is scoped to one proxy request and its already verified session epoch.
// The socket is guest-local; it exports a one-byte decision, not credentials.
type BatchGateway struct {
	session *nativeauth.Manager
	slots   chan struct{}
	mu      sync.Mutex
	grants  map[string]batchGrant
}

func newBatchGateway(session *nativeauth.Manager) *BatchGateway {
	return &BatchGateway{session: session, slots: make(chan struct{}, batchConcurrency), grants: make(map[string]batchGrant)}
}

func ListenBatchGateway(ctx context.Context, session *nativeauth.Manager) (*BatchGateway, error) {
	listener, err := net.ListenUnix("unix", &net.UnixAddr{Name: BatchSocket, Net: "unix"})
	if err != nil {
		return nil, err
	}
	if err := os.Chmod(BatchSocket, 0600); err != nil {
		_ = listener.Close()
		return nil, err
	}
	gateway := newBatchGateway(session)
	context.AfterFunc(ctx, func() { _ = listener.Close() })
	go func() {
		connections := make(chan struct{}, 32)
		for {
			conn, err := listener.AcceptUnix()
			if err != nil {
				return
			}
			select {
			case connections <- struct{}{}:
				go func() {
					defer func() { <-connections }()
					gateway.serveGrantChecks(ctx, conn)
				}()
			default:
				_ = conn.Close()
			}
		}
	}()
	return gateway, nil
}

func (g *BatchGateway) serveGrantChecks(ctx context.Context, conn net.Conn) {
	defer conn.Close()
	stop := context.AfterFunc(ctx, func() { _ = conn.Close() })
	defer stop()
	var grant [32]byte
	for {
		if _, err := io.ReadFull(conn, grant[:]); err != nil {
			return
		}
		response := [1]byte{}
		if g.authorized(string(grant[:])) {
			response[0] = 1
		}
		_ = conn.SetWriteDeadline(time.Now().Add(time.Second))
		if _, err := conn.Write(response[:]); err != nil {
			return
		}
	}
}

func (g *BatchGateway) authorized(key string) bool {
	g.mu.Lock()
	defer g.mu.Unlock()
	grant, ok := g.grants[key]
	if !ok || grant.ctx.Err() != nil {
		return false
	}
	epoch, verified := g.session.VerifiedMediaEpoch()
	return verified && epoch == grant.epoch && grant.ctx.Err() == nil
}

func (g *BatchGateway) issue(ctx context.Context) (string, func(), error) {
	epoch, verified := g.session.VerifiedMediaEpoch()
	if !verified {
		return "", nil, nativeauth.ErrUnverified
	}
	return g.register(ctx, epoch)
}

func (g *BatchGateway) issueBatch(ctx, renewalParent context.Context) (string, func(), error) {
	epoch, release, err := g.session.ReserveMediaWindow(ctx, renewalParent, batchRequestLimit)
	if err != nil {
		return "", nil, err
	}
	key, revoke, err := g.register(ctx, epoch)
	if err != nil {
		release()
		return "", nil, err
	}
	return key, func() {
		revoke()
		release()
	}, nil
}

func (g *BatchGateway) register(ctx context.Context, epoch uint64) (string, func(), error) {
	if ctx.Err() != nil {
		return "", nil, nativeauth.ErrUnverified
	}
	var nonce [16]byte
	if _, err := rand.Read(nonce[:]); err != nil {
		return "", nil, errors.New("native_batch_grant_unavailable")
	}
	key := hex.EncodeToString(nonce[:])
	g.mu.Lock()
	g.grants[key] = batchGrant{ctx: ctx, epoch: epoch}
	g.mu.Unlock()
	return key, func() {
		g.mu.Lock()
		delete(g.grants, key)
		g.mu.Unlock()
	}, nil
}

func validBatchInput(r *http.Request) bool {
	if len(r.Header.Values("X-AMDL-Playback")) != 0 || len(r.Header.Values(batchGrantHeader)) != 0 || r.Header.Get("Content-Encoding") != "" || r.Header.Get("Range") != "" || len(r.TransferEncoding) != 0 {
		return false
	}
	if r.Method == http.MethodGet {
		return r.URL.RawQuery == "" && r.ContentLength == 0 && len(r.Header.Values("X-AMDL-Stream")) == 0
	}
	query, err := url.ParseQuery(r.URL.RawQuery)
	return err == nil && len(query) == 2 && len(query["adamId"]) == 1 && len(query["uri"]) == 1 &&
		nativebatch.ValidContext(query.Get("adamId"), query.Get("uri")) &&
		len(r.Header.Values("X-AMDL-Stream")) == 1 && nativebatch.ValidStream(r.Header.Get("X-AMDL-Stream")) &&
		len(r.Header.Values("Content-Type")) == 1 && r.Header.Get("Content-Type") == nativebatch.ContentType &&
		r.ContentLength >= 10 && r.ContentLength <= nativebatch.MaxRequest
}

func (s *Server) serveBatch(w http.ResponseWriter, r *http.Request) {
	if !validBatchInput(r) {
		failure(w, http.StatusBadRequest, "native_batch_frame_invalid")
		return
	}
	if s.media == nil {
		failure(w, http.StatusServiceUnavailable, "native_media_unavailable")
		return
	}
	ctx, cancel := context.WithTimeout(r.Context(), batchRequestLimit)
	defer cancel()
	stop := context.AfterFunc(s.ctx, cancel)
	defer stop()
	select {
	case s.Batch.slots <- struct{}{}:
		defer func() { <-s.Batch.slots }()
	case <-ctx.Done():
		failure(w, http.StatusServiceUnavailable, "native_batch_queue_timeout")
		return
	}
	if r.Method == http.MethodPost {
		raw, err := io.ReadAll(io.LimitReader(r.Body, nativebatch.MaxRequest+1))
		if err != nil || int64(len(raw)) != r.ContentLength || !nativebatch.ValidRequest(raw) {
			failure(w, http.StatusBadRequest, "native_batch_frame_invalid")
			return
		}
		r.Body = io.NopCloser(bytes.NewReader(raw))
	}
	var grant string
	var revoke func()
	var err error
	if r.Method == http.MethodPost {
		// Wait for the verification window outside the shared media lane;
		// renewal and draining an older batch must never queue playback here.
		grant, revoke, err = s.Batch.issueBatch(ctx, s.ctx)
	} else {
		// Discovery is passive. Its shorter client deadline must neither start
		// nor cancel a session renewal.
		grant, revoke, err = s.Batch.issue(ctx)
	}
	if err != nil {
		if errors.Is(err, nativeauth.ErrUnverified) || errors.Is(err, nativeauth.ErrAuthRequired) {
			sessionFailure(w, err)
		} else {
			failure(w, http.StatusServiceUnavailable, "native_batch_grant_unavailable")
		}
		return
	}
	defer revoke()
	release, err := s.mediaGate.acquire(ctx, false)
	if err != nil {
		failure(w, http.StatusServiceUnavailable, "native_batch_queue_timeout")
		return
	}
	allowed := s.Batch.authorized(grant)
	// Lite holds its response until the last block. Keep the independent
	// batch slot, but return the exclusive lane before waiting for its reply.
	release()
	if !allowed {
		// No work was sent: an explicit session change while queued is safe
		// to handle through the normal verification pause.
		sessionFailure(w, nativeauth.ErrUnverified)
		return
	}
	if err := s.media(w, r.WithContext(context.WithValue(ctx, batchGrantKey{}, grant))); err != nil {
		var typed *proxyFailure
		if errors.As(err, &typed) {
			failure(w, typed.status, typed.code)
			return
		}
		failure(w, http.StatusBadGateway, "native_batch_response_unknown")
	}
}
