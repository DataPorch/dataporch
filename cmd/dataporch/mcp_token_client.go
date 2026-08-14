package main

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/http"
	"time"

	"github.com/adamraziv/dataporch/internal/mcptoken"
)

const mcpTokenResponseLimit = 16 << 10

type mcpTokenMetadataResponse struct {
	CreatedAt *time.Time `json:"created_at,omitempty"`
	RotatedAt *time.Time `json:"rotated_at,omitempty"`
}

type mcpTokenStatusResponse struct {
	State    mcptoken.State           `json:"state"`
	Metadata mcpTokenMetadataResponse `json:"metadata"`
}

type mcpTokenMutationResponse struct {
	Token    string                   `json:"token"`
	Metadata mcpTokenMetadataResponse `json:"metadata"`
}

func (c *unixClient) CreateMCPToken(ctx context.Context) (string, mcptoken.Metadata, error) {
	response, err := c.doMCPTokenRequest(ctx, http.MethodPost, "/v1/mcp-token")
	if err != nil {
		return "", mcptoken.Metadata{}, err
	}
	defer closeMCPTokenResponse(c, response)

	if response.StatusCode != http.StatusCreated {
		return "", mcptoken.Metadata{}, mcpTokenResponseError(response)
	}

	var result mcpTokenMutationResponse
	if err := decodeMCPTokenResponse(response.Body, &result); err != nil {
		return "", mcptoken.Metadata{}, errors.New("decoding MCP token response")
	}
	metadata, err := metadataFromResponse(result.Metadata, true)
	if err != nil || result.Token == "" {
		return "", mcptoken.Metadata{}, errors.New("invalid MCP token response")
	}

	return result.Token, metadata, nil
}

func (c *unixClient) MCPTokenStatus(ctx context.Context) (mcptoken.Status, error) {
	response, err := c.doMCPTokenRequest(ctx, http.MethodGet, "/v1/mcp-token")
	if err != nil {
		return mcptoken.Status{}, err
	}
	defer closeMCPTokenResponse(c, response)

	if response.StatusCode != http.StatusOK {
		return mcptoken.Status{}, mcpTokenResponseError(response)
	}

	var result mcpTokenStatusResponse
	if err := decodeMCPTokenResponse(response.Body, &result); err != nil {
		return mcptoken.Status{}, errors.New("decoding MCP token status response")
	}
	metadata, err := metadataFromResponse(result.Metadata, result.State == mcptoken.StateActive)
	if err != nil {
		return mcptoken.Status{}, errors.New("invalid MCP token status response")
	}
	switch result.State {
	case mcptoken.StateNone, mcptoken.StateActive, mcptoken.StateDegraded:
	default:
		return mcptoken.Status{}, errors.New("invalid MCP token status response")
	}

	return mcptoken.Status{State: result.State, Metadata: metadata}, nil
}

func (c *unixClient) RotateMCPToken(ctx context.Context) (string, mcptoken.Metadata, error) {
	response, err := c.doMCPTokenRequest(ctx, http.MethodPost, "/v1/mcp-token/rotate")
	if err != nil {
		return "", mcptoken.Metadata{}, err
	}
	defer closeMCPTokenResponse(c, response)

	if response.StatusCode != http.StatusOK {
		return "", mcptoken.Metadata{}, mcpTokenResponseError(response)
	}

	var result mcpTokenMutationResponse
	if err := decodeMCPTokenResponse(response.Body, &result); err != nil {
		return "", mcptoken.Metadata{}, errors.New("decoding MCP token response")
	}
	metadata, err := metadataFromResponse(result.Metadata, true)
	if err != nil || result.Token == "" {
		return "", mcptoken.Metadata{}, errors.New("invalid MCP token response")
	}

	return result.Token, metadata, nil
}

func (c *unixClient) RevokeMCPToken(ctx context.Context) error {
	response, err := c.doMCPTokenRequest(ctx, http.MethodDelete, "/v1/mcp-token")
	if err != nil {
		return err
	}
	defer closeMCPTokenResponse(c, response)

	if response.StatusCode != http.StatusNoContent {
		return mcpTokenResponseError(response)
	}

	return nil
}

func (c *unixClient) doMCPTokenRequest(ctx context.Context, method, path string) (*http.Response, error) {
	if c == nil || c.client == nil {
		return nil, errors.New("MCP token client is unavailable")
	}

	request, err := http.NewRequestWithContext(ctx, method, "http://unix"+path, nil)
	if err != nil {
		return nil, errors.New("creating MCP token request")
	}

	response, err := c.client.Do(request)
	if err != nil {
		return nil, errors.New("sending MCP token request")
	}

	return response, nil
}

func (c *unixClient) closeMCPTokenResponse(response *http.Response) {
	if response != nil && response.Body != nil {
		_ = response.Body.Close()
	}
	if c != nil && c.client != nil {
		c.client.CloseIdleConnections()
	}
}

func closeMCPTokenResponse(c *unixClient, response *http.Response) {
	c.closeMCPTokenResponse(response)
}

func decodeMCPTokenResponse(reader io.Reader, target any) error {
	data, err := io.ReadAll(io.LimitReader(reader, mcpTokenResponseLimit+1))
	if err != nil {
		return err
	}
	if len(data) > mcpTokenResponseLimit {
		return errors.New("MCP token response is too large")
	}

	decoder := json.NewDecoder(bytes.NewReader(data))
	decoder.DisallowUnknownFields()
	if err := decoder.Decode(target); err != nil {
		return err
	}
	var extra any
	if err := decoder.Decode(&extra); err != io.EOF {
		return errors.New("MCP token response has trailing data")
	}

	return nil
}

func metadataFromResponse(response mcpTokenMetadataResponse, requireCreated bool) (mcptoken.Metadata, error) {
	if requireCreated && response.CreatedAt == nil {
		return mcptoken.Metadata{}, errors.New("created_at is missing")
	}
	if response.RotatedAt != nil && response.CreatedAt == nil {
		return mcptoken.Metadata{}, errors.New("created_at is missing")
	}
	if response.CreatedAt != nil && response.CreatedAt.IsZero() {
		return mcptoken.Metadata{}, errors.New("created_at is invalid")
	}
	if response.RotatedAt != nil && response.RotatedAt.IsZero() {
		return mcptoken.Metadata{}, errors.New("rotated_at is invalid")
	}
	if response.CreatedAt != nil && response.RotatedAt != nil && response.RotatedAt.Before(*response.CreatedAt) {
		return mcptoken.Metadata{}, errors.New("rotated_at precedes created_at")
	}

	return mcptoken.Metadata{
		CreatedAt: valueOrZero(response.CreatedAt),
		RotatedAt: response.RotatedAt,
	}, nil
}

func valueOrZero(value *time.Time) time.Time {
	if value == nil {
		return time.Time{}
	}
	return *value
}

func mcpTokenResponseError(response *http.Response) error {
	var body struct {
		Code    string `json:"code"`
		Message string `json:"message"`
	}
	if err := decodeMCPTokenResponse(response.Body, &body); err != nil {
		return errors.New("MCP token operation failed")
	}

	switch body.Code {
	case "token_exists", "token_not_configured", "token_unavailable":
		return fmt.Errorf("MCP token operation failed: %s", body.Code)
	default:
		return errors.New("MCP token operation failed")
	}
}
