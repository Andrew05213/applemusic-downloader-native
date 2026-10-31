package main

import (
	"crypto/hmac"
	"crypto/rand"
	"crypto/sha256"
	"encoding/binary"
	"encoding/hex"
	"encoding/json"
	"errors"
	"io"
	"os"
	"path/filepath"
	"strings"

	"github.com/Andrew05213/applemusic_downloader/internal/secretfd"
)

// A receipt selects a restore attempt, never proves a live Apple session.
// It persists only a keyed binding, not raw credentials or tokens. The key
// does not replace the required private/durable Wrapper state permissions.
type wrapperSessionReceipt struct {
	Version int    `json:"version"`
	Key     []byte `json:"key"`
	Tag     string `json:"tag"`
}

func wrapperSessionTag(key []byte, credentials secretfd.Credentials) string {
	mac := hmac.New(sha256.New, key)
	for _, value := range []string{strings.ToLower(strings.TrimSpace(credentials.AppleEmail)), credentials.ApplePassword} {
		var length [8]byte
		binary.BigEndian.PutUint64(length[:], uint64(len(value)))
		_, _ = mac.Write(length[:])
		_, _ = mac.Write([]byte(value))
	}
	return hex.EncodeToString(mac.Sum(nil))
}

func wrapperSessionMatches(path string, credentials secretfd.Credentials) bool {
	if !filepath.IsAbs(path) || !credentials.WrapperConfigured() {
		return false
	}
	info, err := os.Lstat(path)
	if err != nil || !info.Mode().IsRegular() || info.Size() > 1024 || info.Mode().Perm()&0077 != 0 {
		return false
	}
	file, err := os.Open(path)
	if err != nil {
		return false
	}
	defer file.Close()
	actual, err := file.Stat()
	if err != nil || !os.SameFile(info, actual) {
		return false
	}
	var receipt wrapperSessionReceipt
	decoder := json.NewDecoder(io.LimitReader(file, 1025))
	decoder.DisallowUnknownFields()
	if decoder.Decode(&receipt) != nil || receipt.Version != 1 || len(receipt.Key) != 32 {
		return false
	}
	var extra any
	if decoder.Decode(&extra) != io.EOF {
		return false
	}
	tag, err := hex.DecodeString(receipt.Tag)
	if err != nil || len(tag) != sha256.Size {
		return false
	}
	expected, _ := hex.DecodeString(wrapperSessionTag(receipt.Key, credentials))
	return hmac.Equal(tag, expected)
}

func writeWrapperSessionReceipt(path string, credentials secretfd.Credentials) error {
	if !filepath.IsAbs(path) || !credentials.WrapperConfigured() {
		return errors.New("session receipt unavailable")
	}
	if info, err := os.Lstat(path); err == nil && !info.Mode().IsRegular() {
		return errors.New("session receipt is not a regular file")
	} else if err != nil && !os.IsNotExist(err) {
		return err
	}
	parent := filepath.Dir(path)
	info, err := os.Lstat(parent)
	if err != nil || !info.IsDir() || info.Mode()&os.ModeSymlink != 0 {
		return errors.New("session receipt directory unavailable")
	}
	key := make([]byte, 32)
	if _, err := rand.Read(key); err != nil {
		return err
	}
	receipt := wrapperSessionReceipt{Version: 1, Key: key, Tag: wrapperSessionTag(key, credentials)}
	data, err := json.Marshal(receipt)
	if err != nil {
		return err
	}
	file, err := os.CreateTemp(parent, ".wrapper-session-*")
	if err != nil {
		return err
	}
	defer os.Remove(file.Name())
	if err := file.Chmod(0600); err != nil {
		_ = file.Close()
		return err
	}
	_, writeErr := file.Write(data)
	syncErr := file.Sync()
	closeErr := file.Close()
	if writeErr != nil {
		return writeErr
	}
	if syncErr != nil {
		return syncErr
	}
	if closeErr != nil {
		return closeErr
	}
	if err := os.Rename(file.Name(), path); err != nil {
		return err
	}
	directory, err := os.Open(parent)
	if err != nil {
		return err
	}
	defer directory.Close()
	return directory.Sync()
}

func (m *WrapperManager) verifyRestoredSession(runToken chan struct{}, process WrapperProcess) {
	m.mu.Lock()
	if m.done != runToken || process == nil || m.process != process || m.processExited || !m.running || m.stopRequested || m.tearingDown || m.authenticationFailed || (!m.resumeAttempt && !m.loginCompleted) {
		m.mu.Unlock()
		return
	}
	m.loginCompleted = true
	m.authenticated = true
	if m.portReady {
		m.status = WrapperStatusOnline
		m.phase = WrapperPhaseOnline
		m.pauseInitializationDeadlineLocked(process)
	} else {
		m.phase = WrapperPhaseAuthenticatedInitializing
		m.resetInitializationDeadlineLocked(process)
	}
	var err error
	if m.config.SessionReceiptPath != "" {
		err = writeWrapperSessionReceipt(m.config.SessionReceiptPath, m.config.LoginCredentials)
	}
	m.mu.Unlock()
	if err != nil {
		m.emitLog("[wrapper] session receipt could not be saved; next start requires login\n")
	}
}
