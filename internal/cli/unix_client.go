package cli

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net"
	"net/http"
	"time"

	"github.com/adamraziv/dataporch/internal/connection"
)

const importClientTimeout = 10 * time.Second

type unixClient struct{ client *http.Client }

func newUnixClient(socketPath string) (*unixClient, error) {
	if socketPath == "" {
		return nil, errors.New("admin socket path is required")
	}

	dialer := &net.Dialer{Timeout: importClientTimeout}
	transport := &http.Transport{
		DialContext: func(ctx context.Context, _, _ string) (net.Conn, error) {
			return dialer.DialContext(ctx, "unix", socketPath)
		},
	}

	return &unixClient{client: &http.Client{Transport: transport, Timeout: importClientTimeout}}, nil
}

func NewUnixClient(socketPath string) (ImportClient, error) {
	return newUnixClient(socketPath)
}

func NewMCPTokenClient(socketPath string) (MCPTokenClient, error) {
	return newUnixClient(socketPath)
}

func (c *unixClient) Import(ctx context.Context, request connection.ImportRequest) (connection.ImportResult, error) {
	if c == nil || c.client == nil {
		return connection.ImportResult{}, errors.New("connection import client is unavailable")
	}

	payload, err := json.Marshal(struct {
		DatabaseID       connection.ID   `json:"databaseId"`
		Kind             connection.Kind `json:"kind"`
		ConnectionString []byte          `json:"connectionString"`
	}{DatabaseID: request.ID, Kind: request.Kind, ConnectionString: request.ConnectionString})
	if err != nil {
		return connection.ImportResult{}, errors.New("encoding connection import request")
	}
	defer zeroBytes(payload)

	httpRequest, err := http.NewRequestWithContext(
		ctx,
		http.MethodPost,
		"http://unix/v1/connections/import",
		bytes.NewReader(payload),
	)
	if err != nil {
		return connection.ImportResult{}, errors.New("creating connection import request")
	}

	httpRequest.Header.Set("Content-Type", "application/json")

	response, err := c.client.Do(httpRequest)
	if err != nil {
		return connection.ImportResult{}, errors.New("sending connection import request")
	}
	defer func() { _ = response.Body.Close() }()
	defer c.client.CloseIdleConnections()

	if response.StatusCode != http.StatusCreated && response.StatusCode != http.StatusOK {
		return connection.ImportResult{}, importResponseError(response)
	}

	var result struct {
		Status             string        `json:"status"`
		DatabaseID         connection.ID `json:"databaseId"`
		IsConnectionTested bool          `json:"connectionTested"`
	}
	if err := json.NewDecoder(io.LimitReader(response.Body, 16<<10)).Decode(&result); err != nil {
		return connection.ImportResult{}, errors.New("decoding connection import response")
	}

	isKnownStatus := result.Status == "added" || result.Status == "updated"

	hasDatabaseID := result.DatabaseID != ""
	if !isKnownStatus || !hasDatabaseID {
		return connection.ImportResult{}, errors.New("invalid connection import response")
	}

	return connection.ImportResult{
		ID:                 result.DatabaseID,
		IsUpdated:          result.Status == "updated",
		IsConnectionTested: result.IsConnectionTested,
	}, nil
}

func importResponseError(response *http.Response) error {
	var body struct {
		Code string `json:"code"`
	}

	_ = json.NewDecoder(io.LimitReader(response.Body, 16<<10)).Decode(&body)
	switch body.Code {
	case "invalid_connection_string", "invalid_request", "request_too_large", "database_unavailable":
		return fmt.Errorf("connection import failed: %s", body.Code)
	default:
		return errors.New("connection import failed")
	}
}
