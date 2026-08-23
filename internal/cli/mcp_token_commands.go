package cli

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
	mcpTokenListCommand   = "list"
	mcpTokenRevokeCommand = "revoke"
	mcpTokenRotateCommand = "rotate"
)

var errMCPTokenConfirmationRequired = errors.New("mcp token revoke requires an interactive terminal")

type MCPTokenClient interface {
	CreateMCPToken(context.Context) (string, mcptoken.Metadata, error)
	MCPTokenStatus(context.Context) (mcptoken.Status, error)
	RotateMCPToken(context.Context) (string, mcptoken.Metadata, error)
	RevokeMCPToken(context.Context) error
}

type mcpTokenClient = MCPTokenClient

func mcpTokenCommandRun(ctx context.Context, args []string, dependencies commandDependencies) error {
	if len(args) == 0 {
		return errUnknownCommand
	}

	switch args[0] {
	case mcpTokenCreateCommand:
		if len(args) != 1 {
			return errUnexpectedArguments
		}

		return createMCPToken(ctx, dependencies)
	case mcpTokenListCommand:
		if len(args) != 1 {
			return errUnexpectedArguments
		}

		return listMCPToken(ctx, dependencies)
	case mcpTokenRotateCommand:
		if len(args) != 1 {
			return errUnexpectedArguments
		}

		return rotateMCPToken(ctx, dependencies)
	case mcpTokenRevokeCommand:
		return revokeMCPToken(ctx, args[1:], dependencies)
	default:
		return errUnknownCommand
	}
}

func createMCPToken(ctx context.Context, dependencies commandDependencies) error {
	if err := requireCommandOutput(dependencies.stdout); err != nil {
		return err
	}

	client, err := newMCPTokenClient(dependencies)
	if err != nil {
		return err
	}

	token, metadata, err := client.CreateMCPToken(ctx)
	if err != nil {
		return errors.New("creating MCP token: request failed")
	}

	if err := writeMCPTokenOutput(
		dependencies.stdout,
		"MCP token created successfully.\n\n",
		token,
		metadata,
	); err != nil {
		return fmt.Errorf(
			"mcp token was changed but could not be displayed; run mcp-token rotate with working output: %w",
			err,
		)
	}

	return nil
}

func listMCPToken(ctx context.Context, dependencies commandDependencies) error {
	client, err := newMCPTokenClient(dependencies)
	if err != nil {
		return err
	}

	status, err := client.MCPTokenStatus(ctx)
	if err != nil {
		return errors.New("listing MCP token: request failed")
	}

	if err := requireCommandOutput(dependencies.stdout); err != nil {
		return err
	}

	switch status.State {
	case mcptoken.StateNone:
		if err := writeString(dependencies.stdout, "No MCP token is configured.\n"); err != nil {
			return err
		}
	case mcptoken.StateActive:
		if err := writeString(dependencies.stdout, "MCP token is active.\n"); err != nil {
			return err
		}
		if err := writeMCPTokenMetadata(dependencies.stdout, status.Metadata); err != nil {
			return err
		}
	case mcptoken.StateDegraded:
		if err := writeString(dependencies.stdout, "MCP token state is degraded.\nUse dataporch mcp-token revoke --yes to attempt recovery.\n"); err != nil {
			return err
		}
	default:
		return errors.New("received invalid MCP token state")
	}

	return nil
}

func rotateMCPToken(ctx context.Context, dependencies commandDependencies) error {
	if err := requireCommandOutput(dependencies.stdout); err != nil {
		return err
	}

	client, err := newMCPTokenClient(dependencies)
	if err != nil {
		return err
	}

	token, metadata, err := client.RotateMCPToken(ctx)
	if err != nil {
		return errors.New("rotating MCP token: request failed")
	}

	if err := writeMCPTokenOutput(
		dependencies.stdout,
		"MCP token rotated successfully.\nThe previous token is no longer valid.\n\n",
		token,
		metadata,
	); err != nil {
		return fmt.Errorf(
			"mcp token was changed but could not be displayed; run mcp-token rotate with working output: %w",
			err,
		)
	}

	return nil
}

func revokeMCPToken(ctx context.Context, args []string, dependencies commandDependencies) error {
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

	if err := client.RevokeMCPToken(ctx); err != nil {
		return errors.New("revoking MCP token: request failed")
	}

	if err := requireCommandOutput(dependencies.stdout); err != nil {
		return err
	}

	return writeString(dependencies.stdout, "MCP token revoked successfully.\n")
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

	if err := writeString(dependencies.stdout, "Revoke the MCP token? [y/N] "); err != nil {
		return false, err
	}

	answer, err := dependencies.readConfirmation(dependencies.stdin)
	if err != nil {
		return false, fmt.Errorf("reading revoke confirmation: %w", err)
	}

	if !strings.EqualFold(strings.TrimSpace(answer), "y") &&
		!strings.EqualFold(strings.TrimSpace(answer), "yes") {
		if err := writeString(dependencies.stdout, "MCP token revocation canceled.\n"); err != nil {
			return false, err
		}
		return false, nil
	}

	return true, nil
}

func writeMCPTokenOutput(
	writer io.Writer,
	prefix string,
	token string,
	metadata mcptoken.Metadata,
) error {
	output := []string{
		prefix,
		"Save this token now. It will not be shown again:\n\n",
		token,
		"\n\nSet it as DATAPORCH_MCP_TOKEN before starting your MCP client.\n",
	}

	if !metadata.CreatedAt.IsZero() {
		output = append(output, fmt.Sprintf("Created: %s\n", metadata.CreatedAt.UTC().Format(time.RFC3339Nano)))
	}

	if metadata.RotatedAt != nil {
		output = append(output, fmt.Sprintf("Rotated: %s\n", metadata.RotatedAt.UTC().Format(time.RFC3339Nano)))
	}

	return writeCommandOutput(writer, strings.Join(output, ""))
}

func writeCommandOutput(writer io.Writer, output string) error {
	written, err := io.WriteString(writer, output)
	if err != nil {
		return fmt.Errorf("writing command output: %w", err)
	}

	if written != len(output) {
		return fmt.Errorf("writing command output: %w", io.ErrShortWrite)
	}

	return nil
}

func writeMCPTokenMetadata(writer io.Writer, metadata mcptoken.Metadata) error {
	if !metadata.CreatedAt.IsZero() {
		if err := writeString(writer, fmt.Sprintf("Created: %s\n", metadata.CreatedAt.UTC().Format(time.RFC3339Nano))); err != nil {
			return err
		}
	}

	if metadata.RotatedAt == nil {
		return nil
	}

	return writeString(writer, fmt.Sprintf("Rotated: %s\n", metadata.RotatedAt.UTC().Format(time.RFC3339Nano)))
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
