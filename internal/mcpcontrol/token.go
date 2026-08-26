package mcpcontrol

import (
	"crypto/subtle"
	"encoding/base64"
	"errors"
	"fmt"
	"io"
)

const credentialBytes = 32

var ErrInvalidCredential = errors.New("invalid local MCP credential")

func Generate(random io.Reader) (string, error) {
	if random == nil {
		return "", errors.New("randomness source is required")
	}

	var raw [credentialBytes]byte
	if _, err := io.ReadFull(random, raw[:]); err != nil {
		return "", fmt.Errorf("generating local MCP credential: %w", err)
	}

	return base64.RawURLEncoding.EncodeToString(raw[:]), nil
}

func Validate(credential string) error {
	raw, err := base64.RawURLEncoding.DecodeString(credential)
	if err != nil || len(raw) != credentialBytes || base64.RawURLEncoding.EncodeToString(raw) != credential {
		return ErrInvalidCredential
	}

	return nil
}

func Verify(expected, presented string) error {
	if err := Validate(expected); err != nil {
		return ErrInvalidCredential
	}
	if err := Validate(presented); err != nil {
		return ErrInvalidCredential
	}

	expectedRaw, _ := base64.RawURLEncoding.DecodeString(expected)
	presentedRaw, _ := base64.RawURLEncoding.DecodeString(presented)
	if subtle.ConstantTimeCompare(expectedRaw, presentedRaw) != 1 {
		return ErrInvalidCredential
	}

	return nil
}
