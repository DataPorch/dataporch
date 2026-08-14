package main

import (
	"bufio"
	"context"
	"errors"
	"flag"
	"fmt"
	"io"
	"os"
	"strings"
	"time"

	"github.com/adamraziv/dataporch/internal/mcptoken"
)

const (
	mcpTokenCommand       = "mcp-token"
	mcpTokenCreateCommand = "create"
	mcpTokenRevokeCommand = "revoke"
)

var errMCPTokenConfirmationRequired = errors.New("mcp token revoke requires an interactive terminal")

type mcpTokenClient interface {
	CreateMCPToken(context.Context) (string, mcptoken.Metadata, error)
	MCPTokenStatus(context.Context) (mcptoken.Status, error)
	RotateMCPToken(context.Context) (string, mcptoken.Metadata, error)
	RevokeMCPToken(context.Context) error
}

func mcpTokenCommandRun(args []string, dependencies commandDependencies) error {
	if len(args) == 0 {
		return errUnknownCommand
	}

	switch args[0] {
	case mcpTokenCreateCommand:
		if len(args) != 1 {
			return errUnexpectedArguments
		}

		return createMCPToken(dependencies)
	case "list":
		if len(args) != 1 {
			return errUnexpectedArguments
		}

		return listMCPToken(dependencies)
	case "rotate":
		if len(args) != 1 {
			return errUnexpectedArguments
		}

		return rotateMCPToken(dependencies)
	case mcpTokenRevokeCommand:
		return revokeMCPToken(args[1:], dependencies)
	default:
		return errUnknownCommand
	}
}

func createMCPToken(dependencies commandDependencies) error {
	client, err := newMCPTokenClient(dependencies)
	if err != nil {
		return err
	}

	token, metadata, err := client.CreateMCPToken(context.Background())
	if err != nil {
		return errors.New("creating MCP token: request failed")
	}

	if err := requireCommandOutput(dependencies.stdout); err != nil {
		return err
	}

	_, _ = fmt.Fprintln(dependencies.stdout, "MCP token created successfully.")
	_, _ = fmt.Fprintln(dependencies.stdout)
	writeMCPTokenOnce(dependencies.stdout, token, metadata)

	return nil
}

func listMCPToken(dependencies commandDependencies) error {
	client, err := newMCPTokenClient(dependencies)
	if err != nil {
		return err
	}

	status, err := client.MCPTokenStatus(context.Background())
	if err != nil {
		return errors.New("listing MCP token: request failed")
	}

	if err := requireCommandOutput(dependencies.stdout); err != nil {
		return err
	}

	switch status.State {
	case mcptoken.StateNone:
		_, _ = fmt.Fprintln(dependencies.stdout, "No MCP token is configured.")
	case mcptoken.StateActive:
		_, _ = fmt.Fprintln(dependencies.stdout, "MCP token is active.")
		writeMCPTokenMetadata(dependencies.stdout, status.Metadata)
	case mcptoken.StateDegraded:
		_, _ = fmt.Fprintln(dependencies.stdout, "MCP token state is degraded.")
		_, _ = fmt.Fprintln(dependencies.stdout, "Use dataporch mcp-token revoke --yes to attempt recovery.")
	default:
		return errors.New("received invalid MCP token state")
	}

	return nil
}

func rotateMCPToken(dependencies commandDependencies) error {
	client, err := newMCPTokenClient(dependencies)
	if err != nil {
		return err
	}

	token, metadata, err := client.RotateMCPToken(context.Background())
	if err != nil {
		return errors.New("rotating MCP token: request failed")
	}

	if err := requireCommandOutput(dependencies.stdout); err != nil {
		return err
	}

	_, _ = fmt.Fprintln(dependencies.stdout, "MCP token rotated successfully.")
	_, _ = fmt.Fprintln(dependencies.stdout, "The previous token is no longer valid.")
	_, _ = fmt.Fprintln(dependencies.stdout)
	writeMCPTokenOnce(dependencies.stdout, token, metadata)

	return nil
}

func revokeMCPToken(args []string, dependencies commandDependencies) error {
	revoke, err := parseMCPTokenRevokeArguments(args)
	if err != nil {
		return err
	}

	client, err := newMCPTokenClient(dependencies)
	if err != nil {
		return err
	}

	if !revoke.yes {
		confirmed, err := confirmMCPTokenRevoke(dependencies)
		if err != nil {
			return err
		}

		if !confirmed {
			return nil
		}
	}

	if err := client.RevokeMCPToken(context.Background()); err != nil {
		return errors.New("revoking MCP token: request failed")
	}

	if err := requireCommandOutput(dependencies.stdout); err != nil {
		return err
	}

	_, _ = fmt.Fprintln(dependencies.stdout, "MCP token revoked successfully.")

	return nil
}

type mcpTokenRevokeArguments struct {
	yes bool
}

func parseMCPTokenRevokeArguments(args []string) (mcpTokenRevokeArguments, error) {
	flags := flag.NewFlagSet("mcp-token revoke", flag.ContinueOnError)
	flags.SetOutput(io.Discard)

	yes := flags.Bool("yes", false, "revoke without confirmation")
	if err := flags.Parse(args); err != nil {
		return mcpTokenRevokeArguments{}, fmt.Errorf("parsing revoke command: %w", err)
	}

	if len(flags.Args()) != 0 {
		return mcpTokenRevokeArguments{}, errUnexpectedArguments
	}

	return mcpTokenRevokeArguments{yes: *yes}, nil
}

func newMCPTokenClient(dependencies commandDependencies) (mcpTokenClient, error) {
	cfg, err := loadConfig(dependencies)
	if err != nil {
		return nil, err
	}

	if dependencies.newAdminClient == nil {
		return nil, errors.New("MCP token client is required")
	}

	client, err := dependencies.newAdminClient(cfg.AdminSocketPath)
	if err != nil {
		return nil, fmt.Errorf("creating MCP token client: %w", err)
	}

	if client == nil {
		return nil, errors.New("MCP token client is unavailable")
	}

	return client, nil
}

func confirmMCPTokenRevoke(dependencies commandDependencies) (bool, error) {
	if dependencies.stdin == nil || dependencies.isTerminal == nil || dependencies.readConfirmation == nil {
		return false, errMCPTokenConfirmationRequired
	}

	if !dependencies.isTerminal(int(dependencies.stdin.Fd())) {
		return false, errMCPTokenConfirmationRequired
	}

	if err := requireCommandOutput(dependencies.stdout); err != nil {
		return false, err
	}

	_, _ = fmt.Fprint(dependencies.stdout, "Revoke the MCP token? [y/N] ")

	answer, err := dependencies.readConfirmation(dependencies.stdin)
	if err != nil {
		return false, fmt.Errorf("reading revoke confirmation: %w", err)
	}

	if !strings.EqualFold(strings.TrimSpace(answer), "y") &&
		!strings.EqualFold(strings.TrimSpace(answer), "yes") {
		_, _ = fmt.Fprintln(dependencies.stdout, "MCP token revocation canceled.")
		return false, nil
	}

	return true, nil
}

func writeMCPTokenOnce(writer io.Writer, token string, metadata mcptoken.Metadata) {
	_, _ = fmt.Fprintln(writer, "Save this token now. It will not be shown again:")
	_, _ = fmt.Fprintln(writer)
	_, _ = fmt.Fprintln(writer, token)
	_, _ = fmt.Fprintln(writer)
	_, _ = fmt.Fprintln(writer, "Set it as DATAPORCH_MCP_TOKEN before starting your MCP client.")
	writeMCPTokenMetadata(writer, metadata)
}

func writeMCPTokenMetadata(writer io.Writer, metadata mcptoken.Metadata) {
	if !metadata.CreatedAt.IsZero() {
		_, _ = fmt.Fprintf(writer, "Created: %s\n", metadata.CreatedAt.UTC().Format(time.RFC3339Nano))
	}

	if metadata.RotatedAt == nil {
		return
	}

	_, _ = fmt.Fprintf(writer, "Rotated: %s\n", metadata.RotatedAt.UTC().Format(time.RFC3339Nano))
}

func requireCommandOutput(writer io.Writer) error {
	if writer == nil {
		return errors.New("standard output is required")
	}

	return nil
}

func readConfirmationLine(stdin *os.File) (string, error) {
	if stdin == nil {
		return "", errMCPTokenConfirmationRequired
	}

	line, err := bufio.NewReader(stdin).ReadString('\n')
	if err != nil && !errors.Is(err, io.EOF) {
		return "", err
	}

	return strings.TrimSpace(line), nil
}
