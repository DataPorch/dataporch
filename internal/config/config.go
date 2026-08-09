package config

import (
	"errors"
	"fmt"
	"net"
	"strconv"
	"time"
)

const (
	defaultHTTPAddress   = "127.0.0.1:8080"
	defaultResourceLimit = 100
	maxResourceLimit     = 1000
)

var errLookupRequired = errors.New("config: environment lookup is required")

type LookupEnv func(string) (string, bool)

type Config struct {
	HTTPAddress    string
	ResourceLimit  int
	ShutdownPeriod time.Duration
}

func Load(lookup LookupEnv) (Config, error) {
	if lookup == nil {
		return Config{}, errLookupRequired
	}

	cfg := Config{
		HTTPAddress:    defaultHTTPAddress,
		ResourceLimit:  defaultResourceLimit,
		ShutdownPeriod: 10 * time.Second,
	}

	if value, exists := lookup("DATAPORCH_HTTP_ADDRESS"); exists {
		cfg.HTTPAddress = value
	}

	if value, exists := lookup("DATAPORCH_RESOURCE_LIMIT"); exists {
		limit, err := strconv.Atoi(value)
		if err != nil {
			return Config{}, fmt.Errorf("parsing DATAPORCH_RESOURCE_LIMIT: %w", err)
		}

		cfg.ResourceLimit = limit
	}

	if err := cfg.Validate(); err != nil {
		return Config{}, err
	}

	return cfg, nil
}

func (c Config) Validate() error {
	if _, _, err := net.SplitHostPort(c.HTTPAddress); err != nil {
		return fmt.Errorf("validating DATAPORCH_HTTP_ADDRESS: %w", err)
	}

	if c.ResourceLimit <= 0 || c.ResourceLimit > maxResourceLimit {
		return fmt.Errorf(
			"validating DATAPORCH_RESOURCE_LIMIT: must be between 1 and %d",
			maxResourceLimit,
		)
	}

	if c.ShutdownPeriod <= 0 {
		return errors.New("validating shutdown period: must be positive")
	}

	return nil
}
