package cli

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"net"
	"net/http"
	"strings"
	"time"
)

const (
	healthRequestTimeout = 2 * time.Second
	healthStartupTimeout = 15 * time.Second
	healthPollInterval   = 100 * time.Millisecond
)

type HealthChecker interface {
	Check(context.Context, string) error
	Wait(context.Context, string) error
}
type healthChecker struct {
	client                                       *http.Client
	requestTimeout, startupTimeout, pollInterval time.Duration
}

func NewHealthChecker() HealthChecker {
	return &healthChecker{client: &http.Client{Timeout: healthRequestTimeout}, requestTimeout: healthRequestTimeout, startupTimeout: healthStartupTimeout, pollInterval: healthPollInterval}
}

func healthURL(address string) (string, error) {
	host, port, err := net.SplitHostPort(address)
	if err != nil {
		return "", fmt.Errorf("parsing health address: %w", err)
	}
	if host == "" || host == "0.0.0.0" || host == "::" {
		host = "127.0.0.1"
	}
	return "http://" + net.JoinHostPort(host, port) + "/healthz", nil
}

func (h *healthChecker) Check(ctx context.Context, address string) error {
	if ctx == nil {
		return errors.New("health context is required")
	}
	url, err := healthURL(address)
	if err != nil {
		return err
	}
	requestCtx, cancel := context.WithTimeout(ctx, h.requestTimeout)
	defer cancel()
	request, err := http.NewRequestWithContext(requestCtx, http.MethodGet, url, nil)
	if err != nil {
		return fmt.Errorf("creating health request: %w", err)
	}
	response, err := h.client.Do(request)
	if err != nil {
		return fmt.Errorf("checking health: %w", err)
	}
	defer func() { _ = response.Body.Close() }()
	if response.StatusCode != http.StatusOK {
		return fmt.Errorf("checking health: unexpected status %d", response.StatusCode)
	}
	if !strings.HasPrefix(response.Header.Get("Content-Type"), "application/json") {
		return errors.New("checking health: expected JSON response")
	}
	var result struct {
		Status string `json:"status"`
	}
	if err := json.NewDecoder(response.Body).Decode(&result); err != nil {
		return fmt.Errorf("decoding health response: %w", err)
	}
	if result.Status != "ok" {
		return errors.New("checking health: status is not ok")
	}
	return nil
}

func (h *healthChecker) Wait(ctx context.Context, address string) error {
	if ctx == nil {
		return errors.New("health context is required")
	}
	waitCtx, cancel := context.WithTimeout(ctx, h.startupTimeout)
	defer cancel()
	ticker := time.NewTicker(h.pollInterval)
	defer ticker.Stop()
	for {
		if err := h.Check(waitCtx, address); err == nil {
			return nil
		}
		select {
		case <-waitCtx.Done():
			return fmt.Errorf("waiting for health: %w", waitCtx.Err())
		case <-ticker.C:
		}
	}
}
