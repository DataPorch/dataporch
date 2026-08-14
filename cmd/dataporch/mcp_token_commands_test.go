package main

import (
	"bytes"
	"context"
	"errors"
	"os"
	"strings"
	"testing"
	"time"

	"github.com/adamraziv/dataporch/internal/mcptoken"
)

func TestMCPTokenCommandsCreate(t *testing.T) {
	t.Parallel()

	createdAt := time.Date(2026, 8, 14, 8, 50, 0, 0, time.UTC)
	dependencies, client := mcpTokenCommandDependencies(t)
	client.createToken = "dp-created"
	client.createMetadata = mcptoken.Metadata{CreatedAt: createdAt}

	if err := run([]string{"mcp-token", "create"}, dependencies); err != nil {
		t.Fatalf("run() error = %v", err)
	}

	stdout := commandOutputBuffer(t, dependencies).String()
	for _, want := range []string{
		"MCP token created successfully.\n",
		"Save this token now. It will not be shown again:\n",
		"\ndp-created\n",
		"Set it as DATAPORCH_MCP_TOKEN before starting your MCP client.\n",
		"Created: 2026-08-14T08:50:00Z\n",
	} {
		if !strings.Contains(stdout, want) {
			t.Fatalf("stdout = %q, missing %q", stdout, want)
		}
	}

	if got := strings.Count(stdout, "dp-created"); got != 1 {
		t.Fatalf("token occurrences = %d, want 1", got)
	}
}

func TestMCPTokenCommandsListStates(t *testing.T) {
	t.Parallel()

	createdAt := time.Date(2026, 8, 14, 8, 50, 0, 0, time.UTC)
	rotatedAt := createdAt.Add(time.Hour)

	tests := []struct {
		name      string
		status    mcptoken.Status
		want      string
		wantExtra string
	}{
		{
			name:   "none",
			status: mcptoken.Status{State: mcptoken.StateNone},
			want:   "No MCP token is configured.\n",
		},
		{
			name:      "active",
			status:    mcptoken.Status{State: mcptoken.StateActive, Metadata: mcptoken.Metadata{CreatedAt: createdAt, RotatedAt: &rotatedAt}},
			want:      "MCP token is active.\n",
			wantExtra: "Created: 2026-08-14T08:50:00Z\nRotated: 2026-08-14T09:50:00Z\n",
		},
		{
			name:      "degraded",
			status:    mcptoken.Status{State: mcptoken.StateDegraded},
			want:      "MCP token state is degraded.\n",
			wantExtra: "Use dataporch mcp-token revoke --yes to attempt recovery.\n",
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()
			dependencies, client := mcpTokenCommandDependencies(t)
			client.status = tt.status

			if err := run([]string{"mcp-token", "list"}, dependencies); err != nil {
				t.Fatalf("run() error = %v", err)
			}

			stdout := commandOutputBuffer(t, dependencies).String()
			if !strings.Contains(stdout, tt.want) || (tt.wantExtra != "" && !strings.Contains(stdout, tt.wantExtra)) {
				t.Fatalf("stdout = %q, want %q and %q", stdout, tt.want, tt.wantExtra)
			}

			if strings.Contains(stdout, "dp-") || strings.Contains(stdout, "verifier") {
				t.Fatalf("list output contains sensitive token material: %q", stdout)
			}
		})
	}
}

func TestMCPTokenCommandsRotate(t *testing.T) {
	t.Parallel()
	dependencies, client := mcpTokenCommandDependencies(t)
	client.rotateToken = "dp-rotated"
	client.rotateMetadata = mcptoken.Metadata{CreatedAt: time.Date(2026, 8, 14, 8, 50, 0, 0, time.UTC)}

	if err := run([]string{"mcp-token", "rotate"}, dependencies); err != nil {
		t.Fatalf("run() error = %v", err)
	}

	stdout := commandOutputBuffer(t, dependencies).String()
	for _, want := range []string{
		"MCP token rotated successfully.",
		"The previous token is no longer valid.",
		"dp-rotated",
	} {
		if !strings.Contains(stdout, want) {
			t.Fatalf("stdout = %q, missing %q", stdout, want)
		}
	}
}

func TestMCPTokenCommandsRevokeConfirmation(t *testing.T) {
	t.Parallel()

	tests := []struct {
		name          string
		answer        string
		args          []string
		wantCalls     int
		wantOutput    string
		wantReadCalls int
	}{
		{name: "declined", answer: "no", wantOutput: "MCP token revocation canceled.", wantReadCalls: 1},
		{name: "confirmed", answer: "yes", wantCalls: 1, wantOutput: "MCP token revoked successfully.", wantReadCalls: 1},
		{name: "yes flag", args: []string{"--yes"}, wantCalls: 1, wantOutput: "MCP token revoked successfully."},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()
			dependencies, client := mcpTokenCommandDependencies(t)
			readCalls := 0
			dependencies.readConfirmation = func(*os.File) (string, error) {
				readCalls++
				return tt.answer, nil
			}

			args := []string{"mcp-token", "revoke"}

			args = append(args, tt.args...)
			if err := run(args, dependencies); err != nil {
				t.Fatalf("run() error = %v", err)
			}

			stdout := commandOutputBuffer(t, dependencies).String()
			if !strings.Contains(stdout, tt.wantOutput) {
				t.Fatalf("stdout = %q, missing %q", stdout, tt.wantOutput)
			}

			if client.revokeCalls != tt.wantCalls {
				t.Fatalf("revoke calls = %d, want %d", client.revokeCalls, tt.wantCalls)
			}

			if readCalls != tt.wantReadCalls {
				t.Fatalf("confirmation reads = %d, want %d", readCalls, tt.wantReadCalls)
			}
		})
	}
}

func TestMCPTokenCommandsRejectUnexpectedArgumentsAndSanitizeFailures(t *testing.T) {
	t.Parallel()
	dependencies, client := mcpTokenCommandDependencies(t)
	client.createErr = errors.New("dp-command-secret-canary")

	if err := run([]string{"mcp-token", "create", "extra"}, dependencies); !errors.Is(err, errUnexpectedArguments) {
		t.Fatalf("unexpected args error = %v, want %v", err, errUnexpectedArguments)
	}

	err := run([]string{"mcp-token", "create"}, dependencies)
	if err == nil || strings.Contains(err.Error(), "dp-command-secret-canary") {
		t.Fatalf("create error = %v, want safe error", err)
	}

	if commandOutputBuffer(t, dependencies).Len() != 0 {
		t.Fatalf("stdout = %q, want empty on client failure", dependencies.stdout)
	}
}

func TestMCPTokenCommandsRequireConfirmationForNonInteractiveRevoke(t *testing.T) {
	t.Parallel()
	dependencies, client := mcpTokenCommandDependencies(t)
	dependencies.isTerminal = func(int) bool { return false }

	err := run([]string{"mcp-token", "revoke"}, dependencies)
	if !errors.Is(err, errMCPTokenConfirmationRequired) {
		t.Fatalf("run() error = %v, want confirmation error", err)
	}

	if client.revokeCalls != 0 {
		t.Fatalf("revoke calls = %d, want 0", client.revokeCalls)
	}
}

func mcpTokenCommandDependencies(t *testing.T) (commandDependencies, *mcpTokenClientStub) {
	t.Helper()

	client := &mcpTokenClientStub{}
	dependencies := testCommandDependencies(t)
	dependencies.newAdminClient = func(string) (mcpTokenClient, error) { return client, nil }
	dependencies.readConfirmation = func(*os.File) (string, error) { return "no", nil }

	return dependencies, client
}

func commandOutputBuffer(t *testing.T, dependencies commandDependencies) *bytes.Buffer {
	t.Helper()

	buffer, ok := dependencies.stdout.(*bytes.Buffer)
	if !ok {
		t.Fatalf("stdout type = %T, want *bytes.Buffer", dependencies.stdout)
	}

	return buffer
}

type mcpTokenClientStub struct {
	createToken    string
	createMetadata mcptoken.Metadata
	createErr      error
	status         mcptoken.Status
	statusErr      error
	rotateToken    string
	rotateMetadata mcptoken.Metadata
	rotateErr      error
	revokeErr      error
	revokeCalls    int
}

func (c *mcpTokenClientStub) CreateMCPToken(context.Context) (string, mcptoken.Metadata, error) {
	return c.createToken, c.createMetadata, c.createErr
}

func (c *mcpTokenClientStub) MCPTokenStatus(context.Context) (mcptoken.Status, error) {
	return c.status, c.statusErr
}

func (c *mcpTokenClientStub) RotateMCPToken(context.Context) (string, mcptoken.Metadata, error) {
	return c.rotateToken, c.rotateMetadata, c.rotateErr
}

func (c *mcpTokenClientStub) RevokeMCPToken(context.Context) error {
	c.revokeCalls++
	return c.revokeErr
}
