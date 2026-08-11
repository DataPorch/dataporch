package execution

import (
	"bytes"
	"crypto/sha256"
	"encoding/base64"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"io"
)

const cursorVersion = 1

type cursorRequest struct {
	Operation           string `json:"operation"`
	SourceID            string `json:"source_id"`
	Schema              string `json:"schema"`
	Table               string `json:"table"`
	Limit               int    `json:"limit"`
	Search              string `json:"search"`
	IncludeDescriptions bool   `json:"include_descriptions"`
}

type cursorPayload struct {
	Version     int    `json:"version"`
	Fingerprint string `json:"fingerprint"`
	LastName    string `json:"last_name,omitempty"`
	LastOrdinal int    `json:"last_ordinal,omitempty"`
}

func encodeCursor(request cursorRequest, lastName string, lastOrdinal int) (string, error) {
	fingerprint, err := requestFingerprint(request)
	if err != nil {
		return "", fmt.Errorf("fingerprinting cursor request: %w", err)
	}

	payload := cursorPayload{
		Version:     cursorVersion,
		Fingerprint: fingerprint,
		LastName:    lastName,
		LastOrdinal: lastOrdinal,
	}
	encoded, err := json.Marshal(payload)
	if err != nil {
		return "", fmt.Errorf("marshaling cursor: %w", err)
	}

	return base64.RawURLEncoding.EncodeToString(encoded), nil
}

func decodeCursor(value string, request cursorRequest, ordinal bool) (cursorPayload, error) {
	if value == "" {
		return cursorPayload{}, nil
	}
	if len(value) > 8192 {
		return cursorPayload{}, ErrInvalidCursor
	}

	decoded, err := base64.RawURLEncoding.DecodeString(value)
	if err != nil || len(decoded) > 4096 {
		return cursorPayload{}, ErrInvalidCursor
	}

	decoder := json.NewDecoder(io.LimitReader(bytes.NewReader(decoded), 4097))
	decoder.DisallowUnknownFields()

	var payload cursorPayload
	if err := decoder.Decode(&payload); err != nil {
		return cursorPayload{}, ErrInvalidCursor
	}
	var extra any
	if err := decoder.Decode(&extra); !errors.Is(err, io.EOF) {
		return cursorPayload{}, ErrInvalidCursor
	}

	if payload.Version != cursorVersion || len(payload.Fingerprint) != sha256.Size*2 {
		return cursorPayload{}, ErrInvalidCursor
	}
	if _, err := hex.DecodeString(payload.Fingerprint); err != nil {
		return cursorPayload{}, ErrInvalidCursor
	}
	if (ordinal && (payload.LastOrdinal <= 0 || payload.LastName != "")) ||
		(!ordinal && (payload.LastName == "" || payload.LastOrdinal != 0)) {
		return cursorPayload{}, ErrInvalidCursor
	}

	fingerprint, err := requestFingerprint(request)
	if err != nil || fingerprint != payload.Fingerprint {
		return cursorPayload{}, ErrInvalidCursor
	}

	return payload, nil
}

func requestFingerprint(request cursorRequest) (string, error) {
	encoded, err := json.Marshal(request)
	if err != nil {
		return "", err
	}

	digest := sha256.Sum256(encoded)
	return hex.EncodeToString(digest[:]), nil
}
