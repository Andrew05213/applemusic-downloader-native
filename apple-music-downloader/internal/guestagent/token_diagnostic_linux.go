package guestagent

import (
	"encoding/json"
	"io"
	"strings"

	"github.com/Andrew05213/applemusic_downloader/internal/nativeauth"
)

const liteTokenDiagnosticPrefix = "WRAPPER_TOKEN_EVENT:"

func decodeLiteTokenDiagnostic(line string) (nativeauth.LoginTokenDiagnostic, bool) {
	var result nativeauth.LoginTokenDiagnostic
	raw, ok := strings.CutPrefix(line, liteTokenDiagnosticPrefix)
	if !ok || len(raw) > 256 {
		return result, false
	}
	var wire struct {
		Type       string          `json:"type"`
		Stage      string          `json:"stage"`
		Reason     string          `json:"reason"`
		Attempt    *int            `json:"attempt"`
		UsedCache  *bool           `json:"usedCache"`
		HTTPStatus json.RawMessage `json:"httpStatus"`
	}
	decoder := json.NewDecoder(strings.NewReader(raw))
	decoder.DisallowUnknownFields()
	if decoder.Decode(&wire) != nil || decoder.Decode(new(any)) != io.EOF ||
		wire.Type != "login_token_diagnostic" || wire.Attempt == nil || wire.UsedCache == nil {
		return result, false
	}
	result = nativeauth.LoginTokenDiagnostic{
		Stage: wire.Stage, Reason: wire.Reason, Attempt: *wire.Attempt, UsedCache: *wire.UsedCache,
	}
	if len(wire.HTTPStatus) != 0 {
		if json.Unmarshal(wire.HTTPStatus, &result.HTTPStatus) != nil || result.HTTPStatus < 100 || result.HTTPStatus > 599 {
			return nativeauth.LoginTokenDiagnostic{}, false
		}
	}
	if !nativeauth.ValidLoginTokenDiagnostic(result) {
		return nativeauth.LoginTokenDiagnostic{}, false
	}
	return result, true
}

// Use the last unresolved diagnostic of this process only. Retry bookkeeping
// never masks a concrete cause. A recovered fetch is not a login failure.
func updateLiteTokenFailure(last *nativeauth.LoginTokenDiagnostic, event *nativeauth.LoginTokenDiagnostic) *nativeauth.LoginTokenDiagnostic {
	if event.Reason == "retry_succeeded" ||
		event.UsedCache && last != nil && last.Attempt == event.Attempt && last.Stage == "music_user_token" {
		return nil
	}
	if event.CanExplainFailure() {
		return event
	}
	return last
}
