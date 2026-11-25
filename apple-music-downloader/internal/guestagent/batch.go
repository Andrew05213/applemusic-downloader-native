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
	BatchSocket         = "/run/amdl-native-batch.sock"
	batchGrantHeader    = "X-AMDL-Batch-Grant"
	batchConcurrency    = 3
	batchAdmissionLimit = 35 * time.Second
	batchRequestLimit   = 35 * time.Second
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

func (s *Server) prepareBatch(admission, request context.Context) (context.Context, string, func(), error) {
	for {
		// Do not occupy the media lane while draining older batches or
		// waiting for the shared session renewal.
		epoch, releaseWindow, err := s.Batch.session.ReserveMediaWindow(admission, s.ctx, batchRequestLimit)
		if err != nil {
			return nil, "", nil, err
		}
		releaseLane, err := s.mediaGate.acquire(admission, false)
		if err != nil {
			releaseWindow()
			return nil, "", nil, &proxyFailure{http.StatusServiceUnavailable, "native_batch_queue_timeout"}
		}
		// Queueing and verification use the admission deadline. The work,
		// reply and grant share a fresh budget under the request lifetime.
		execution, cancel := context.WithTimeout(request, batchRequestLimit)
		current, enoughTime := s.Batch.session.VerifiedMediaEpochFor(batchRequestLimit)
		if current != epoch || !enoughTime || admission.Err() != nil {
			cancel()
			releaseLane()
			releaseWindow()
			if admission.Err() != nil || current != epoch {
				return nil, "", nil, nativeauth.ErrUnverified
			}
			// Lane wait used up the reserved margin. Reserve a fresh window
			// outside the lane instead of extending the old epoch.
			continue
		}
		key, revoke, err := s.Batch.register(execution, epoch)
		// The upstream sends headers after all blocks. Give playback the
		// lane before dispatch; the independent batch slot stays held.
		releaseLane()
		if err != nil {
			cancel()
			releaseWindow()
			return nil, "", nil, err
		}
		return execution, key, func() {
			revoke()
			cancel()
			releaseWindow()
		}, nil
	}
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
	request, cancel := context.WithCancel(r.Context())
	defer cancel()
	stop := context.AfterFunc(s.ctx, cancel)
	defer stop()
	admission, finishAdmission := context.WithTimeout(request, batchAdmissionLimit)
	defer finishAdmission()
	select {
	case s.Batch.slots <- struct{}{}:
		defer func() { <-s.Batch.slots }()
	case <-admission.Done():
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
	execution := admission
	var grant string
	var revoke func()
	var err error
	if r.Method == http.MethodPost {
		execution, grant, revoke, err = s.prepareBatch(admission, request)
		finishAdmission()
	} else {
		// Discovery is passive. Its shorter client deadline must neither start
		// nor cancel a session renewal.
		grant, revoke, err = s.Batch.issue(admission)
		if err == nil {
			release, acquireErr := s.mediaGate.acquire(admission, false)
			if acquireErr != nil {
				err = &proxyFailure{http.StatusServiceUnavailable, "native_batch_queue_timeout"}
			} else {
				release()
			}
		}
	}
	if revoke != nil {
		defer revoke()
	}
	if err != nil {
		var typed *proxyFailure
		if errors.As(err, &typed) {
			failure(w, typed.status, typed.code)
		} else if errors.Is(err, nativeauth.ErrUnverified) || errors.Is(err, nativeauth.ErrAuthRequired) {
			sessionFailure(w, err)
		} else {
			failure(w, http.StatusServiceUnavailable, "native_batch_grant_unavailable")
		}
		return
	}
	if !s.Batch.authorized(grant) {
		// No work was sent: an explicit session change while queued is safe
		// to handle through the normal verification pause.
		sessionFailure(w, nativeauth.ErrUnverified)
		return
	}
	if err := s.media(w, r.WithContext(context.WithValue(execution, batchGrantKey{}, grant))); err != nil {
		var typed *proxyFailure
		if errors.As(err, &typed) {
			failure(w, typed.status, typed.code)
			return
		}
		failure(w, http.StatusBadGateway, "native_batch_response_unknown")
	}
}
