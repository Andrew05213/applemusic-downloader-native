package nativeauth

// History-only vocabulary: deleting SMS login must not erase previously
// exported failures. These codes do not enable a login method or an API route.
var LegacyAuthCodes = []string{"native_gsa_provider_unavailable", "native_gsa_protocol_invalid", "native_gsa_unavailable", "native_gsa_authentication_failed", "native_gsa_cancelled", "native_gsa_input_invalid", "native_gsa_code_rejected", "native_gsa_challenge_expired", "native_gsa_sms_cooldown", "native_gsa_sms_unavailable", "native_gsa_busy", "native_music_binding_failed"}

func knownLegacyAuthCode(code string) bool {
	for _, known := range LegacyAuthCodes {
		if code == known {
			return true
		}
	}
	return false
}
