package main

import (
	"bytes"
	"errors"
	"fmt"
	"io"
	"os"
	"strconv"
	"strings"

	"github.com/Andrew05213/applemusic_downloader/internal/secretfd"
)

const processCredentialsFileEnvironment = "AMDL_CREDENTIALS_FILE"

// readProcessCredentialsFile is the Docker/native file transport. Compose
// mounts the user's ignored .env read-only and exports only this non-secret
// path, so Docker Config.Env and the Go process environment never contain the
// credential values themselves.
func readProcessCredentialsFile() (secretfd.Credentials, bool, error) {
	filename := strings.TrimSpace(os.Getenv(processCredentialsFileEnvironment))
	_ = os.Unsetenv(processCredentialsFileEnvironment)
	if filename == "" {
		return secretfd.Credentials{}, false, nil
	}
	if strings.ContainsRune(filename, '\x00') {
		return secretfd.Credentials{}, true, errors.New("credential file path is invalid")
	}
	info, err := os.Lstat(filename)
	if err != nil {
		return secretfd.Credentials{}, true, fmt.Errorf("inspect credential file: %w", err)
	}
	if !info.Mode().IsRegular() || info.Mode()&os.ModeSymlink != 0 {
		return secretfd.Credentials{}, true, errors.New("credential file is not a regular file")
	}
	file, err := os.Open(filename)
	if err != nil {
		return secretfd.Credentials{}, true, fmt.Errorf("open credential file: %w", err)
	}
	openedInfo, statErr := file.Stat()
	if statErr != nil || !os.SameFile(info, openedInfo) {
		_ = file.Close()
		return secretfd.Credentials{}, true, errors.New("credential file changed while opening")
	}
	data, readErr := io.ReadAll(io.LimitReader(file, secretfd.MaxDocumentBytes+1))
	closeErr := file.Close()
	if readErr != nil || closeErr != nil {
		secretfd.Wipe(data)
		return secretfd.Credentials{}, true, fmt.Errorf("read credential file: %w", errors.Join(readErr, closeErr))
	}
	defer secretfd.Wipe(data)
	if len(data) > secretfd.MaxDocumentBytes {
		return secretfd.Credentials{}, true, errors.New("credential file is too large")
	}
	credentials, err := decodeProcessCredentialsEnv(data)
	if err != nil {
		return secretfd.Credentials{}, true, err
	}
	return credentials, true, nil
}

func decodeProcessCredentialsEnv(data []byte) (secretfd.Credentials, error) {
	data = bytes.TrimPrefix(data, []byte{0xef, 0xbb, 0xbf})
	values := make(map[string]string)
	for index, rawLine := range strings.Split(string(data), "\n") {
		line := strings.TrimSpace(strings.TrimSuffix(rawLine, "\r"))
		if line == "" || strings.HasPrefix(line, "#") {
			continue
		}
		line = strings.TrimSpace(strings.TrimPrefix(line, "export "))
		key, rawValue, found := strings.Cut(line, "=")
		key = strings.TrimSpace(key)
		if !found || key == "" {
			return secretfd.Credentials{}, fmt.Errorf("credential file line %d is invalid", index+1)
		}
		for _, character := range key {
			if character != '_' && (character < 'A' || character > 'Z') && (character < '0' || character > '9') {
				return secretfd.Credentials{}, fmt.Errorf("credential file line %d has an invalid key", index+1)
			}
		}
		if _, duplicate := values[key]; duplicate {
			return secretfd.Credentials{}, fmt.Errorf("credential file contains duplicate key %s", key)
		}
		value, err := parseProcessCredentialValue(rawValue)
		if err != nil {
			return secretfd.Credentials{}, fmt.Errorf("credential file line %d: %w", index+1, err)
		}
		values[key] = value
	}
	first := func(keys ...string) string {
		for _, key := range keys {
			if value, exists := values[key]; exists {
				return value
			}
		}
		return ""
	}
	credentials := secretfd.Credentials{
		AppleEmail:         first("APPLE_EMAIL"),
		ApplePassword:      first("APPLE_PASSWORD"),
		MediaUserToken:     first("AMDL_MEDIA_USER_TOKEN", "MEDIA_USER_TOKEN"),
		AuthorizationToken: first("AMDL_AUTHORIZATION_TOKEN", "AUTHORIZATION_TOKEN"),
		APIAccessToken:     first("AMDL_API_TOKEN"),
	}
	if err := secretfd.Validate(credentials); err != nil {
		return secretfd.Credentials{}, fmt.Errorf("credential file is invalid: %w", err)
	}
	return credentials, nil
}

func parseProcessCredentialValue(raw string) (string, error) {
	value := strings.TrimSpace(raw)
	if value == "" {
		return "", nil
	}
	if value[0] == '\'' {
		if len(value) < 2 || value[len(value)-1] != '\'' {
			return "", errors.New("single-quoted value is not terminated")
		}
		return value[1 : len(value)-1], nil
	}
	if value[0] == '"' {
		if len(value) < 2 || value[len(value)-1] != '"' {
			return "", errors.New("double-quoted value is not terminated")
		}
		decoded, err := strconv.Unquote(value)
		if err != nil {
			return "", errors.New("double-quoted value is invalid")
		}
		return decoded, nil
	}
	for index, character := range value {
		if character == '#' && index > 0 && (value[index-1] == ' ' || value[index-1] == '\t') {
			value = strings.TrimSpace(value[:index])
			break
		}
	}
	return value, nil
}
