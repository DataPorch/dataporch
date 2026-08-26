package mcpstdio

import (
	"fmt"
	"net/http"
)

type credentialRoundTripper struct {
	base        http.RoundTripper
	credentials CredentialReader
}

func newCredentialRoundTripper(base http.RoundTripper, credentials CredentialReader) http.RoundTripper {
	if base == nil {
		base = http.DefaultTransport
	}
	return &credentialRoundTripper{base: base, credentials: credentials}
}

func (t *credentialRoundTripper) RoundTrip(request *http.Request) (*http.Response, error) {
	credential, err := t.credentials.Read()
	if err != nil {
		return nil, runtimeUnavailable(fmt.Errorf("reading local MCP credential: %w", err))
	}
	first, err := t.roundTripWithCredential(request, credential)
	if err != nil || first.StatusCode != http.StatusUnauthorized || request.GetBody == nil {
		return first, err
	}
	if err := first.Body.Close(); err != nil {
		return nil, fmt.Errorf("closing unauthorized MCP response: %w", err)
	}

	credential, err = t.credentials.Read()
	if err != nil {
		return nil, runtimeUnavailable(fmt.Errorf("refreshing local MCP credential: %w", err))
	}
	body, err := request.GetBody()
	if err != nil {
		return nil, fmt.Errorf("recreating MCP request body: %w", err)
	}
	retry := request.Clone(request.Context())
	retry.Body = body
	retry.GetBody = request.GetBody

	return t.roundTripWithCredential(retry, credential)
}

func (t *credentialRoundTripper) roundTripWithCredential(request *http.Request, credential string) (*http.Response, error) {
	clone := request.Clone(request.Context())
	clone.Header.Del("Authorization")
	clone.Header.Set("Authorization", "Bearer "+credential)
	return t.base.RoundTrip(clone)
}
