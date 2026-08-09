package config

import "testing"

func TestLoad(t *testing.T) {
	t.Parallel()

	tests := []struct {
		name      string
		values    map[string]string
		wantAddr  string
		wantLimit int
		wantError bool
	}{
		{
			name:      "defaults",
			values:    map[string]string{},
			wantAddr:  defaultHTTPAddress,
			wantLimit: defaultResourceLimit,
		},
		{
			name: "overrides",
			values: map[string]string{
				"DATAPORCH_HTTP_ADDRESS":   "127.0.0.1:9090",
				"DATAPORCH_RESOURCE_LIMIT": "25",
			},
			wantAddr:  "127.0.0.1:9090",
			wantLimit: 25,
		},
		{
			name: "invalid address",
			values: map[string]string{
				"DATAPORCH_HTTP_ADDRESS": "localhost",
			},
			wantError: true,
		},
		{
			name: "invalid limit",
			values: map[string]string{
				"DATAPORCH_RESOURCE_LIMIT": "0",
			},
			wantError: true,
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()

			lookup := func(key string) (string, bool) {
				value, exists := tt.values[key]
				return value, exists
			}

			cfg, err := Load(lookup)
			if (err != nil) != tt.wantError {
				t.Fatalf("Load() error = %v, wantError %v", err, tt.wantError)
			}

			if tt.wantError {
				return
			}

			if cfg.HTTPAddress != tt.wantAddr {
				t.Errorf("HTTPAddress = %q, want %q", cfg.HTTPAddress, tt.wantAddr)
			}

			if cfg.ResourceLimit != tt.wantLimit {
				t.Errorf("ResourceLimit = %d, want %d", cfg.ResourceLimit, tt.wantLimit)
			}
		})
	}
}

func TestLoadRejectsMissingLookup(t *testing.T) {
	t.Parallel()

	if _, err := Load(nil); err == nil {
		t.Fatal("Load(nil) error = nil, want non-nil")
	}
}
