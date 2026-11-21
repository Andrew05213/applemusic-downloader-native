package nativeauth

// LoginTokenDiagnostic is an observation from the Wrapper post-auth steps,
// not a terminal login verdict. No part of the request or response is held.
type LoginTokenDiagnostic struct {
	Stage      string `json:"stage"`
	Reason     string `json:"reason"`
	Attempt    int    `json:"attempt"` // 0: login parent's readback/persistence
	UsedCache  bool   `json:"usedCache"`
	HTTPStatus int    `json:"httpStatus,omitempty"`
}

func ValidLoginTokenDiagnostic(d LoginTokenDiagnostic) bool {
	if d.Attempt < 0 || d.Attempt > 5 || d.HTTPStatus != 0 && (d.HTTPStatus < 100 || d.HTTPStatus > 599) ||
		d.UsedCache != (d.Reason == "cache_fallback_used") {
		return false
	}
	switch d.Stage {
	case "storefront":
		return d.Attempt > 0 && d.Reason == "storefront_fetch_failed" && d.HTTPStatus == 0
	case "developer_token":
		return d.Attempt > 0 && d.Reason == "developer_token_fetch_failed"
	case "music_user_token":
		return d.Attempt > 0 && d.Reason == "music_user_token_fetch_failed"
	case "music_token_fallback":
		return d.Attempt > 0 && (d.Reason == "cache_fallback_used" ||
			d.HTTPStatus == 0 && (d.Reason == "cache_read_failed" || d.Reason == "cache_invalid"))
	case "storefront_file", "developer_token_file", "music_token_file":
		if d.HTTPStatus != 0 {
			return false
		}
		switch d.Reason {
		case "cache_read_failed", "cache_invalid":
			return d.Attempt == 0 || d.Stage == "developer_token_file"
		case "cache_write_failed", "cache_replace_failed":
			return d.Attempt > 0
		}
	case "token_cache_json":
		return d.Attempt == 0 && d.HTTPStatus == 0 && (d.Reason == "cache_write_failed" || d.Reason == "cache_replace_failed")
	case "refresh":
		return d.HTTPStatus == 0 && (d.Attempt >= 2 && d.Reason == "retry_succeeded" ||
			d.Attempt == 5 && d.Reason == "retry_exhausted")
	}
	return false
}

func (d LoginTokenDiagnostic) CanExplainFailure() bool {
	return ValidLoginTokenDiagnostic(d) && !d.UsedCache && d.Stage != "refresh"
}

func validLoginTokenEvent(e StageEvent) bool {
	if e.TokenDiagnostic == nil {
		return true
	}
	d := *e.TokenDiagnostic
	if !ValidLoginTokenDiagnostic(d) || e.Stage != "device_login" || e.LoginEvent != "" ||
		e.HTTPStatus != 0 || e.AppleErrorCode != "" || e.StoreResponseType != nil || e.StoreErrorCode != nil ||
		e.StoreExternalErrorCode != nil || e.StoreProtocolErrorCode != nil || e.StoreAuthStatus != nil {
		return false
	}
	if e.Outcome == "waiting" {
		return e.Code == "" && e.ExitCode == nil && e.ExitSignal == 0
	}
	return e.Outcome == "failed" && e.Code == "native_login_token_cache_failed" && d.CanExplainFailure()
}
