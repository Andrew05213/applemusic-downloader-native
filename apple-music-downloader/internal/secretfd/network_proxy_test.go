package secretfd

import "testing"

func TestValidateAcceptsOnlyBoundApplianceLoopbackProxy(t *testing.T) {
	valid := Credentials{
		NetworkProxyURL:   "http://127.0.0.1:43123",
		NetworkGeneration: "0123456789abcdef0123456789abcdef0123456789abcdef0123456789abcdef",
	}
	if err := Validate(valid); err != nil {
		t.Fatalf("valid appliance proxy: %v", err)
	}

	invalid := []Credentials{
		{NetworkProxyURL: valid.NetworkProxyURL},
		{NetworkGeneration: valid.NetworkGeneration},
		{NetworkProxyURL: "https://127.0.0.1:43123", NetworkGeneration: valid.NetworkGeneration},
		{NetworkProxyURL: "http://localhost:43123", NetworkGeneration: valid.NetworkGeneration},
		{NetworkProxyURL: "http://127.0.0.2:43123", NetworkGeneration: valid.NetworkGeneration},
		{NetworkProxyURL: "http://127.0.0.1:43123/path", NetworkGeneration: valid.NetworkGeneration},
		{NetworkProxyURL: "http://user@127.0.0.1:43123", NetworkGeneration: valid.NetworkGeneration},
		{NetworkProxyURL: valid.NetworkProxyURL, NetworkGeneration: "not-a-generation"},
	}
	for _, document := range invalid {
		if err := Validate(document); err == nil {
			t.Fatalf("invalid appliance proxy was accepted: %#v", document)
		}
	}
}
