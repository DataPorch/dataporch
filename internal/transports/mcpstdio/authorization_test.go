package mcpstdio

import (
	"errors"
	"io"
	"net/http"
	"strings"
	"testing"
)

func TestCredentialRoundTripperRetryPolicy(t *testing.T) {
	t.Parallel()

	tests := []struct {
		name      string
		statuses  []int
		wantCalls int
		wantReads int
	}{
		{name: "success", statuses: []int{http.StatusOK}, wantCalls: 1, wantReads: 1},
		{name: "one 401 retry", statuses: []int{http.StatusUnauthorized, http.StatusOK}, wantCalls: 2, wantReads: 2},
		{name: "second 401 stops", statuses: []int{http.StatusUnauthorized, http.StatusUnauthorized}, wantCalls: 2, wantReads: 2},
		{name: "500 is not replayed", statuses: []int{http.StatusInternalServerError}, wantCalls: 1, wantReads: 1},
	}
	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			t.Parallel()
			credentials := &credentialSequence{values: []string{"first", "second"}}
			calls := 0
			authorizations := make([]string, 0, len(test.statuses))
			transport := newCredentialRoundTripper(roundTripperFunc(func(request *http.Request) (*http.Response, error) {
				calls++
				authorizations = append(authorizations, request.Header.Get("Authorization"))
				return &http.Response{
					StatusCode: test.statuses[calls-1],
					Body:       io.NopCloser(strings.NewReader("response")),
					Request:    request,
				}, nil
			}), credentials)

			response, err := transport.RoundTrip(newRequest(t, true))
			if err != nil {
				t.Fatalf("RoundTrip() error = %v", err)
			}
			_ = response.Body.Close()
			if calls != test.wantCalls || credentials.reads != test.wantReads {
				t.Fatalf("calls/reads = %d/%d, want %d/%d", calls, credentials.reads, test.wantCalls, test.wantReads)
			}
			if len(authorizations) > 0 && authorizations[0] != "Bearer first" {
				t.Fatalf("first Authorization = %q, want Bearer first", authorizations[0])
			}
			if len(authorizations) > 1 && authorizations[1] != "Bearer second" {
				t.Fatalf("retry Authorization = %q, want Bearer second", authorizations[1])
			}
		})
	}
}

func TestCredentialRoundTripperDoesNotRetryWithoutGetBody(t *testing.T) {
	t.Parallel()

	credentials := &credentialSequence{values: []string{"first", "second"}}
	calls := 0
	transport := newCredentialRoundTripper(roundTripperFunc(func(request *http.Request) (*http.Response, error) {
		calls++
		return &http.Response{StatusCode: http.StatusUnauthorized, Body: io.NopCloser(strings.NewReader("response")), Request: request}, nil
	}), credentials)
	response, err := transport.RoundTrip(newRequest(t, false))
	if err != nil {
		t.Fatalf("RoundTrip() error = %v", err)
	}
	_ = response.Body.Close()
	if calls != 1 || credentials.reads != 1 {
		t.Fatalf("calls/reads = %d/%d, want 1/1", calls, credentials.reads)
	}
}

func TestCredentialRoundTripperDoesNotRetryTransportErrors(t *testing.T) {
	t.Parallel()

	transportErr := errors.New("transport failed")
	credentials := &credentialSequence{values: []string{"first", "second"}}
	calls := 0
	transport := newCredentialRoundTripper(roundTripperFunc(func(*http.Request) (*http.Response, error) {
		calls++
		return nil, transportErr
	}), credentials)
	_, err := transport.RoundTrip(newRequest(t, true))
	if !errors.Is(err, transportErr) {
		t.Fatalf("RoundTrip() error = %v, want transport error", err)
	}
	if calls != 1 || credentials.reads != 1 {
		t.Fatalf("calls/reads = %d/%d, want 1/1", calls, credentials.reads)
	}
}

func TestCredentialRoundTripperClosesUnauthorizedResponse(t *testing.T) {
	t.Parallel()

	body := &trackingBody{Reader: strings.NewReader("response")}
	credentials := &credentialSequence{values: []string{"first", "second"}}
	calls := 0
	transport := newCredentialRoundTripper(roundTripperFunc(func(request *http.Request) (*http.Response, error) {
		calls++
		if calls == 1 {
			return &http.Response{StatusCode: http.StatusUnauthorized, Body: body, Request: request}, nil
		}
		return &http.Response{StatusCode: http.StatusOK, Body: io.NopCloser(strings.NewReader("ok")), Request: request}, nil
	}), credentials)
	response, err := transport.RoundTrip(newRequest(t, true))
	if err != nil {
		t.Fatalf("RoundTrip() error = %v", err)
	}
	_ = response.Body.Close()
	if !body.closed {
		t.Fatal("unauthorized response body was not closed")
	}
}

type trackingBody struct {
	io.Reader
	closed bool
}

func (b *trackingBody) Close() error {
	b.closed = true
	return nil
}
