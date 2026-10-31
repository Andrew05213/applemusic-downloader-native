package nativeauth

// Fixed vocabulary only. In particular, Apple error titles/details and tokens
// must never be used as diagnostics or as a serialized error message.
var MusicVerificationCodes = []string{
	"native_music_developer_rejected", "native_music_user_rejected",
	"native_music_rate_limited", "native_music_upstream_failed",
	"native_music_http_failed", "native_music_response_invalid",
}

var AppleErrorCodes = []string{"40100", "40101", "40300", "40301", "40302", "40303", "42900", "50000", "50001", "50300"}

func musicVerificationCode(code string) bool {
	for _, known := range MusicVerificationCodes {
		if code == known {
			return true
		}
	}
	return false
}

func KnownAppleErrorCode(code string) bool {
	for _, known := range AppleErrorCodes {
		if code == known {
			return true
		}
	}
	return false
}
