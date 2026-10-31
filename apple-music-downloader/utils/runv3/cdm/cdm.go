package wv

import (
	"bytes"
	"crypto"
	"crypto/aes"
	"crypto/cipher"
	"crypto/rsa"
	"crypto/sha1"
	"crypto/x509"
	"encoding/pem"
	"errors"
	"github.com/aead/cmac"
	"google.golang.org/protobuf/proto"
	"lukechampine.com/frand"
	"math"
	"time"
)

type CDM struct {
	privateKey *rsa.PrivateKey
	clientID   []byte
	sessionID  [32]byte

	widevineCencHeader      *WidevineCencHeader
	signedDeviceCertificate SignedDeviceCertificate
	privacyMode             bool
}

type Key struct {
	ID    []byte
	Type  License_KeyContainer_KeyType
	Value []byte
}

// Creates a new CDM object with the specified device information.
func NewCDM(privateKey string, clientID []byte, initData []byte) (CDM, error) {
	block, _ := pem.Decode([]byte(privateKey))
	if block == nil || block.Type != "RSA PRIVATE KEY" {
		return CDM{}, errors.New("failed to decode device private key")
	}
	keyParsed, err := x509.ParsePKCS1PrivateKey(block.Bytes)
	if err != nil {
		return CDM{}, err
	}

	var widevineCencHeader WidevineCencHeader
	if len(initData) < 32 {
		return CDM{}, errors.New("initData not long enough")
	}
	if err := proto.Unmarshal(initData[32:], &widevineCencHeader); err != nil {
		return CDM{}, err
	}

	sessionID := func() (s [32]byte) {
		c := []byte("ABCDEF0123456789")
		for i := 0; i < 16; i++ {
			s[i] = c[frand.Intn(len(c))]
		}
		s[16] = '0'
		s[17] = '1'
		for i := 18; i < 32; i++ {
			s[i] = '0'
		}
		return s
	}()

	return CDM{
		privateKey: keyParsed,
		clientID:   clientID,

		widevineCencHeader: &widevineCencHeader,

		sessionID: sessionID,
	}, nil
}

// Creates a new CDM object using the default device configuration.
func NewDefaultCDM(initData []byte) (CDM, error) {
	return NewCDM(DefaultPrivateKey, DefaultClientID, initData)
}

// Sets a device certificate.  This is makes generating the license request
// more complicated but is supported.  This is usually not necessary for most
// Widevine applications.
func (c *CDM) SetServiceCertificate(certData []byte) error {
	var message SignedMessage
	if err := proto.Unmarshal(certData, &message); err != nil {
		return err
	}
	if err := proto.Unmarshal(message.Msg, &c.signedDeviceCertificate); err != nil {
		return err
	}
	c.privacyMode = true
	return nil
}

func (c *CDM) GetServiceCertificate() *SignedDeviceCertificate {

	return &c.signedDeviceCertificate
}

// Generates the license request data.  This is sent to the license server via
// HTTP POST and the server in turn returns the license response.
func (c *CDM) GetLicenseRequest() ([]byte, error) {
	var licenseRequest SignedLicenseRequest
	licenseRequest.Msg = new(LicenseRequest)
	licenseRequest.Msg.ContentId = new(LicenseRequest_ContentIdentification)
	licenseRequest.Msg.ContentId.CencId = new(LicenseRequest_ContentIdentification_CENC)

	// this is probably really bad for the GC but protobuf uses pointers for optional
	// fields so it is necessary and this is not a long running program
	{
		v := SignedLicenseRequest_LICENSE_REQUEST
		licenseRequest.Type = &v
	}

	licenseRequest.Msg.ContentId.CencId.Pssh = c.widevineCencHeader

	{
		v := LicenseType_DEFAULT
		licenseRequest.Msg.ContentId.CencId.LicenseType = &v
	}

	licenseRequest.Msg.ContentId.CencId.RequestId = c.sessionID[:]

	{
		v := LicenseRequest_NEW
		licenseRequest.Msg.Type = &v
	}

	{
		v := uint32(time.Now().Unix())
		licenseRequest.Msg.RequestTime = &v
	}

	{
		v := ProtocolVersion_CURRENT
		licenseRequest.Msg.ProtocolVersion = &v
	}

	{
		v := uint32(frand.Uint64n(math.MaxUint32))
		licenseRequest.Msg.KeyControlNonce = &v
	}

	if c.privacyMode {
		pad := func(data []byte, blockSize int) []byte {
			padlen := blockSize - (len(data) % blockSize)
			if padlen == 0 {
				padlen = blockSize
			}
			return append(data, bytes.Repeat([]byte{byte(padlen)}, padlen)...)
		}
		const blockSize = 16

		var cidKey, cidIV [blockSize]byte
		frand.Read(cidKey[:])
		frand.Read(cidIV[:])

		block, err := aes.NewCipher(cidKey[:])
		if err != nil {
			return nil, err
		}

		paddedClientID := pad(c.clientID, blockSize)
		encryptedClientID := make([]byte, len(paddedClientID))
		cipher.NewCBCEncrypter(block, cidIV[:]).CryptBlocks(encryptedClientID, paddedClientID)

		servicePublicKey, err := x509.ParsePKCS1PublicKey(c.signedDeviceCertificate.XDeviceCertificate.PublicKey)
		if err != nil {
			return nil, err
		}

		encryptedCIDKey, err := rsa.EncryptOAEP(sha1.New(), frand.Reader, servicePublicKey, cidKey[:], nil)
		if err != nil {
			return nil, err
		}

		licenseRequest.Msg.EncryptedClientId = new(EncryptedClientIdentification)
		{
			v := string(c.signedDeviceCertificate.XDeviceCertificate.ServiceId)
			licenseRequest.Msg.EncryptedClientId.ServiceId = &v
		}
		licenseRequest.Msg.EncryptedClientId.ServiceCertificateSerialNumber = c.signedDeviceCertificate.XDeviceCertificate.SerialNumber
		licenseRequest.Msg.EncryptedClientId.EncryptedClientId = encryptedClientID
		licenseRequest.Msg.EncryptedClientId.EncryptedClientIdIv = cidIV[:]
		licenseRequest.Msg.EncryptedClientId.EncryptedPrivacyKey = encryptedCIDKey
	} else {
		licenseRequest.Msg.ClientId = new(ClientIdentification)
		if err := proto.Unmarshal(c.clientID, licenseRequest.Msg.ClientId); err != nil {
			return nil, err
		}
	}

	{
		data, err := proto.Marshal(licenseRequest.Msg)
		if err != nil {
			return nil, err
		}
		hash := sha1.Sum(data)
		if licenseRequest.Signature, err = rsa.SignPSS(frand.Reader, c.privateKey, crypto.SHA1, hash[:], &rsa.PSSOptions{SaltLength: rsa.PSSSaltLengthEqualsHash}); err != nil {
			return nil, err
		}
	}

	return proto.Marshal(&licenseRequest)
}

// Retrieves the keys from the license response data.  These keys can be
// used to decrypt the DASH-MP4.
func (c *CDM) GetLicenseKeys(licenseRequest []byte, licenseResponse []byte) (keys []Key, err error) {
	if c == nil || c.privateKey == nil {
		return nil, errors.New("CDM private key is missing")
	}
	var license SignedLicense
	if err = proto.Unmarshal(licenseResponse, &license); err != nil {
		return
	}
	if license.Msg == nil {
		return nil, errors.New("license response message is missing")
	}

	var licenseRequestParsed SignedLicenseRequest
	if err = proto.Unmarshal(licenseRequest, &licenseRequestParsed); err != nil {
		return
	}
	if licenseRequestParsed.Msg == nil {
		return nil, errors.New("license request message is missing")
	}
	licenseRequestMsg, err := proto.Marshal(licenseRequestParsed.Msg)
	if err != nil {
		return
	}

	sessionKey, err := rsa.DecryptOAEP(sha1.New(), frand.Reader, c.privateKey, license.SessionKey, nil)
	if err != nil {
		return
	}

	sessionKeyBlock, err := aes.NewCipher(sessionKey)
	if err != nil {
		return
	}

	encryptionKey := []byte{1, 'E', 'N', 'C', 'R', 'Y', 'P', 'T', 'I', 'O', 'N', 0}
	encryptionKey = append(encryptionKey, licenseRequestMsg...)
	encryptionKey = append(encryptionKey, []byte{0, 0, 0, 0x80}...)
	encryptionKeyCmac, err := cmac.Sum(encryptionKey, sessionKeyBlock, sessionKeyBlock.BlockSize())
	if err != nil {
		return
	}
	encryptionKeyCipher, err := aes.NewCipher(encryptionKeyCmac)
	if err != nil {
		return
	}

	for _, key := range license.Msg.Key {
		decryptedKey, keyErr := decryptLicenseKey(encryptionKeyCipher, key)
		if keyErr != nil {
			return nil, keyErr
		}
		keys = append(keys, decryptedKey)
	}

	return
}

func decryptLicenseKey(block cipher.Block, key *License_KeyContainer) (Key, error) {
	if block == nil {
		return Key{}, errors.New("license key cipher is missing")
	}
	if key == nil {
		return Key{}, errors.New("license key container is missing")
	}
	if key.Type == nil {
		return Key{}, errors.New("license key type is missing")
	}
	blockSize := block.BlockSize()
	if len(key.Iv) != blockSize {
		return Key{}, errors.New("license key IV has an invalid length")
	}
	if len(key.Key) == 0 || len(key.Key)%blockSize != 0 {
		return Key{}, errors.New("license key ciphertext has an invalid length")
	}

	decrypted := make([]byte, len(key.Key))
	cipher.NewCBCDecrypter(block, key.Iv).CryptBlocks(decrypted, key.Key)
	value, err := unpadPKCS7(decrypted, blockSize)
	if err != nil {
		return Key{}, err
	}
	return Key{ID: key.Id, Type: *key.Type, Value: value}, nil
}

func unpadPKCS7(data []byte, blockSize int) ([]byte, error) {
	if blockSize <= 0 || len(data) == 0 || len(data)%blockSize != 0 {
		return nil, errors.New("license key padding is invalid")
	}
	paddingLength := int(data[len(data)-1])
	if paddingLength == 0 || paddingLength > blockSize || paddingLength > len(data) {
		return nil, errors.New("license key padding is invalid")
	}
	for _, value := range data[len(data)-paddingLength:] {
		if int(value) != paddingLength {
			return nil, errors.New("license key padding is invalid")
		}
	}
	return data[:len(data)-paddingLength], nil
}
