package appliance

import "testing"

func TestDecodeNativeNetworkPreflightResultIsStrictAndStageAllowlisted(t *testing.T) {
	for _, valid := range []string{
		`{"schemaVersion":2,"ok":true,"curlCode":0,"sslVerifyResult":0,"caSource":"bundled_v1"}`,
		`{"schemaVersion":2,"ok":false,"curlCode":0,"sslVerifyResult":0,"caSource":"bundled_v1"}`,
		`{"schemaVersion":2,"ok":false,"stage":"ca_bundle_missing","curlCode":0,"sslVerifyResult":0,"caSource":"bundled_v1"}`,
		`{"schemaVersion":2,"ok":false,"stage":"ca_bundle_invalid","curlCode":77,"sslVerifyResult":0,"caSource":"bundled_v1"}`,
		`{"schemaVersion":2,"ok":false,"stage":"dns","curlCode":6,"sslVerifyResult":0,"caSource":"bundled_v1"}`,
		`{"schemaVersion":2,"ok":false,"stage":"tcp_connect","curlCode":7,"sslVerifyResult":0,"caSource":"bundled_v1"}`,
		`{"schemaVersion":2,"ok":false,"stage":"tls","curlCode":60,"sslVerifyResult":20,"caSource":"bundled_v1"}`,
		`{"schemaVersion":2,"ok":false,"stage":"url_bag_fetch","curlCode":22,"sslVerifyResult":0,"caSource":"bundled_v1"}`,
		`{"schemaVersion":2,"ok":false,"stage":"url_bag_parse","curlCode":0,"sslVerifyResult":0,"caSource":"bundled_v1"}`,
		`{"schemaVersion":2,"ok":false,"stage":"url_bag_key_lookup","curlCode":0,"sslVerifyResult":0,"caSource":"bundled_v1"}`,
	} {
		if _, err := decodeNativeNetworkPreflightResult([]byte(valid)); err != nil {
			t.Fatalf("valid result %s: %v", valid, err)
		}
	}
	for _, invalid := range []string{
		`{"schemaVersion":1,"ok":true}`,
		`{"schemaVersion":2,"ok":true,"stage":"dns","curlCode":0,"sslVerifyResult":0,"caSource":"bundled_v1"}`,
		`{"schemaVersion":2,"ok":false,"stage":"socket","curlCode":0,"sslVerifyResult":0,"caSource":"bundled_v1"}`,
		`{"schemaVersion":2,"ok":false,"stage":"ca_bundle_missing","curlCode":77,"sslVerifyResult":0,"caSource":"bundled_v1"}`,
		`{"schemaVersion":2,"ok":false,"stage":"ca_bundle_invalid","curlCode":60,"sslVerifyResult":0,"caSource":"bundled_v1"}`,
		`{"schemaVersion":2,"ok":false,"stage":"dns","curlCode":6,"sslVerifyResult":20,"caSource":"bundled_v1"}`,
		`{"schemaVersion":2,"ok":false,"curlCode":0,"sslVerifyResult":0,"caSource":"spoofed"}`,
		`{"schemaVersion":2,"ok":false,"curlCode":0,"sslVerifyResult":0,"caSource":"bundled_v1","extra":true}`,
		`{"schemaVersion":2,"ok":false,"curlCode":0,"sslVerifyResult":0,"caSource":"bundled_v1"}{}`,
	} {
		if _, err := decodeNativeNetworkPreflightResult([]byte(invalid)); err == nil {
			t.Fatalf("invalid result was accepted: %s", invalid)
		}
	}
}
