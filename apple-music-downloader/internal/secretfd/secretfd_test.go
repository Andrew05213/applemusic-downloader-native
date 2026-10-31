package secretfd

import (
	"bytes"
	"strings"
	"testing"
)

func TestCredentialDocumentRoundTripAndUnknownFieldRejection(t *testing.T) {
	want := Credentials{
		AppleEmail:         "person@example.test",
		ApplePassword:      "password-sentinel",
		MediaUserToken:     "media-token-sentinel",
		AuthorizationToken: "authorization-sentinel",
		Storefront:         "jp",
		APIAccessToken:     strings.Repeat("a", 64),
		ControlToken:       strings.Repeat("b", 64),
	}
	encoded, err := Encode(want)
	if err != nil {
		t.Fatalf("encode: %v", err)
	}
	got, err := Decode(bytes.NewReader(encoded))
	if err != nil {
		t.Fatalf("decode: %v", err)
	}
	if got != want {
		t.Fatal("decoded credentials differ")
	}
	if _, err := Decode(strings.NewReader(`{"appleEmail":"ok","unexpected":"sentinel"}`)); err == nil {
		t.Fatal("unknown credential field was accepted")
	}
}

func TestCredentialStorefrontValidation(t *testing.T) {
	for _, storefront := range []string{"usa", "1p", "日p"} {
		if _, err := Encode(Credentials{Storefront: storefront}); err == nil {
			t.Fatalf("invalid storefront %q was accepted", storefront)
		}
	}
}

func TestCredentialDecodeErrorsNeverContainInput(t *testing.T) {
	const sentinel = "credential-sentinel-must-not-leak"
	_, err := Decode(strings.NewReader(`{"appleEmail":"` + sentinel))
	if err == nil {
		t.Fatal("malformed credential document was accepted")
	}
	if strings.Contains(err.Error(), sentinel) {
		t.Fatalf("decode error leaked input: %v", err)
	}
}

func TestCredentialDocumentSizeLimit(t *testing.T) {
	tooLarge := `{"mediaUserToken":"` + strings.Repeat("x", MaxDocumentBytes) + `"}`
	if _, err := Decode(strings.NewReader(tooLarge)); err == nil {
		t.Fatal("oversized credential document was accepted")
	}
}

func TestWipeOverwritesBuffer(t *testing.T) {
	data := []byte("sentinel")
	Wipe(data)
	for _, value := range data {
		if value != 0 {
			t.Fatalf("buffer was not wiped: %v", data)
		}
	}
}
