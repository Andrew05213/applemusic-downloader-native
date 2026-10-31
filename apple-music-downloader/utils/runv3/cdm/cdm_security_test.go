package wv

import (
	"crypto/aes"
	"crypto/cipher"
	"testing"
)

func TestDecryptLicenseKeyRejectsMalformedContainer(t *testing.T) {
	block, err := aes.NewCipher(make([]byte, aes.BlockSize))
	if err != nil {
		t.Fatalf("create AES block: %v", err)
	}
	contentType := License_KeyContainer_CONTENT
	badPadding := make([]byte, aes.BlockSize)
	badPadding[aes.BlockSize-1] = 0
	encryptedBadPadding := make([]byte, aes.BlockSize)
	cipher.NewCBCEncrypter(block, make([]byte, aes.BlockSize)).CryptBlocks(encryptedBadPadding, badPadding)

	testCases := []struct {
		name string
		key  *License_KeyContainer
	}{
		{name: "nil container", key: nil},
		{name: "missing type", key: &License_KeyContainer{Iv: make([]byte, aes.BlockSize), Key: encryptedBadPadding}},
		{name: "invalid IV length", key: &License_KeyContainer{Type: &contentType, Iv: []byte{1}, Key: encryptedBadPadding}},
		{name: "invalid ciphertext length", key: &License_KeyContainer{Type: &contentType, Iv: make([]byte, aes.BlockSize), Key: []byte{1}}},
		{name: "invalid padding", key: &License_KeyContainer{Type: &contentType, Iv: make([]byte, aes.BlockSize), Key: encryptedBadPadding}},
	}

	for _, tc := range testCases {
		t.Run(tc.name, func(t *testing.T) {
			_, err := decryptLicenseKey(block, tc.key)
			if err == nil {
				t.Fatal("decryptLicenseKey() error = nil for malformed license key")
			}
		})
	}
}
