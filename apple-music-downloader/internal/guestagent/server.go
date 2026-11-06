// Package guestagent is the private guest control plane, never a renderer API.
package guestagent

import (
	"context"
	"crypto/subtle"
	"encoding/json"
	"errors"
	"io"
	"net/http"
	"strings"

	"github.com/Andrew05213/applemusic_downloader/internal/nativeauth"
)

type MediaProxy func(http.ResponseWriter, *http.Request) error
type Server struct {
	ctx        context.Context
	capability string
	session    *nativeauth.Manager
	media      MediaProxy
	shutdown   func()
	mediaGate  *mediaLane
}

type proxyFailure struct {
	status int
	code   string
}

func (e *proxyFailure) Error() string { return e.code }

func NewServer(ctx context.Context, capability string, session *nativeauth.Manager, media MediaProxy, shutdown func()) (*Server, error) {
	if len(capability) != 64 || session == nil || shutdown == nil {
		return nil, errors.New("native_agent_config_invalid")
	}
	for _, r := range capability {
		if !(r >= 'a' && r <= 'f' || r >= '0' && r <= '9') {
			return nil, errors.New("native_agent_config_invalid")
		}
	}
	return &Server{ctx: ctx, capability: capability, session: session, media: media, shutdown: shutdown, mediaGate: &mediaLane{}}, nil
}
func respond(w http.ResponseWriter, status int, value any) {
	w.Header().Set("Content-Type", "application/json")
	w.Header().Set("Cache-Control", "no-store")
	w.WriteHeader(status)
	_ = json.NewEncoder(w).Encode(value)
}
func failure(w http.ResponseWriter, status int, code string) {
	respond(w, status, map[string]string{"code": code})
}
func sessionFailure(w http.ResponseWriter, err error) {
	if errors.Is(err, nativeauth.ErrUnverified) {
		// The request has not reached Lite. A transient verification failure
		// is not evidence that Apple rejected the account or these credentials.
		failure(w, http.StatusServiceUnavailable, nativeauth.ErrUnverified.Error())
		return
	}
	failure(w, http.StatusUnauthorized, nativeauth.ErrAuthRequired.Error())
}
func decode(w http.ResponseWriter, r *http.Request, value any, limit int64) bool {
	reader := http.MaxBytesReader(w, r.Body, limit)
	defer reader.Close()
	decoder := json.NewDecoder(reader)
	decoder.DisallowUnknownFields()
	return decoder.Decode(value) == nil && decoder.Decode(new(any)) == io.EOF
}
func (s *Server) ServeHTTP(w http.ResponseWriter, r *http.Request) {
	header := strings.TrimPrefix(r.Header.Get("Authorization"), "Bearer ")
	if subtle.ConstantTimeCompare([]byte(header), []byte(s.capability)) != 1 {
		failure(w, 401, "native_agent_unauthorized")
		return
	}
	if r.URL.RawQuery != "" && strings.HasPrefix(r.URL.Path, "/session") {
		failure(w, 400, "native_agent_input_invalid")
		return
	}
	switch {
	case r.URL.Path == "/status" && r.Method == http.MethodGet:
		state := s.session.Snapshot()
		regions := []string{}
		if state.State == "authenticated" {
			regions = append(regions, state.Region)
		}
		respond(w, 200, map[string]any{"code": 0, "data": map[string]any{"regions": regions, "controlProtocol": 1, "sessionState": state.State}})
	case r.URL.Path == "/session" && r.Method == http.MethodGet:
		respond(w, 200, s.session.Refresh(s.ctx))
	case r.URL.Path == "/session" && r.Method == http.MethodPost:
		var credentials nativeauth.Credentials
		if !decode(w, r, &credentials, 32768) {
			failure(w, 400, nativeauth.ErrInvalidInput.Error())
			return
		}
		err := s.session.Begin(s.ctx, credentials)
		credentials = nativeauth.Credentials{}
		if err != nil {
			status := 400
			if errors.Is(err, nativeauth.ErrBusy) {
				status = 409
			}
			failure(w, status, err.Error())
			return
		}
		respond(w, 202, s.session.Snapshot())
	case r.URL.Path == "/session/code" && r.Method == http.MethodPost:
		var input struct {
			Code string `json:"code"`
		}
		if !decode(w, r, &input, 128) {
			failure(w, 400, nativeauth.ErrInvalidInput.Error())
			return
		}
		err := s.session.SubmitCode(input.Code)
		input.Code = ""
		if err != nil {
			failure(w, 400, nativeauth.ErrInvalidInput.Error())
			return
		}
		respond(w, 202, s.session.Snapshot())
	case r.URL.Path == "/session/cancel" && r.Method == http.MethodPost:
		s.session.Cancel()
		respond(w, 200, s.session.Snapshot())
	case r.URL.Path == "/session/recheck" && r.Method == http.MethodPost:
		if err := s.session.Restore(r.Context()); err != nil {
			failure(w, 409, "native_auth_busy")
			return
		}
		respond(w, 200, s.session.Snapshot())
	case r.URL.Path == "/session/credentials" && r.Method == http.MethodGet:
		tokens, err := s.session.Ensure(r.Context())
		if err != nil {
			sessionFailure(w, err)
			return
		}
		// This route is private backend-to-agent. Never serialize it in a UI event.
		respond(w, 200, map[string]string{"developer": tokens.Developer, "media": tokens.Media, "region": tokens.Region})
	case r.URL.Path == "/shutdown" && r.Method == http.MethodPost:
		respond(w, 202, map[string]bool{"accepted": true})
		go s.shutdown()
	case (r.Method == http.MethodGet && (r.URL.Path == "/m3u8" || r.URL.Path == "/lyrics" || r.URL.Path == "/webplayback")) ||
		(r.Method == http.MethodPost && (r.URL.Path == "/decrypt" || r.URL.Path == "/license")) || (r.Method == http.MethodDelete && r.URL.Path == "/decrypt"):
		// Closing an owned stream must still work when a session expires.
		// Capability authentication above remains mandatory for cleanup.
		if r.Method != http.MethodDelete {
			if _, err := s.session.Ensure(r.Context()); err != nil {
				sessionFailure(w, err)
				return
			}
		}
		if s.media == nil {
			failure(w, 503, "native_media_unavailable")
			return
		}
		// All clients share Lite's single playback mutex. Queue outside its HTTP
		// call so the 30-second upstream timer measures work, not other requests.
		if r.URL.Path == "/m3u8" || r.URL.Path == "/decrypt" || r.URL.Path == "/license" {
			ctx, cancel := context.WithCancel(r.Context())
			defer cancel()
			stop := context.AfterFunc(s.ctx, cancel)
			defer stop()
			release, err := s.mediaGate.acquire(ctx, r.Header.Get("X-AMDL-Playback") == "1")
			if err != nil {
				failure(w, 503, "native_media_unavailable")
				return
			}
			defer release()
			r = r.WithContext(ctx)
		}
		if err := s.media(w, r); err != nil {
			var typed *proxyFailure
			if errors.As(err, &typed) {
				failure(w, typed.status, typed.code)
				return
			}
			failure(w, 502, "native_media_failed")
		}
	default:
		failure(w, 404, "native_agent_route_not_found")
	}
}
