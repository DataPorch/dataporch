package cli

import (
	"errors"
	"fmt"
	"io"
	"strings"
)

type commandSpec struct{ path, summary, detail string }

var commandHelp = []commandSpec{
	{runCommand, "start the background user service", "Usage: dataporch run\n\nRegister and start the native user service."},
	{"run -f", "run DataPorch in the current terminal", "Usage: dataporch run -f\n\nRun DataPorch in the foreground."},
	{"restart", "restart the background user service", "Usage: dataporch restart\n\nRefresh and restart the native user service."},
	{"stop", "stop the background user service", "Usage: dataporch stop\n\nStop and unregister the native user service."},
	{"status", "display runtime status", "Usage: dataporch status\n\nDisplay native service and health status."},
	{"secrets", "manage local secrets", "Usage: dataporch secrets <command>\n\nManage the local encrypted secret store."},
	{"secrets init", "initialize the local secret store", "Usage: dataporch secrets init"},
	{"connections", "manage database connections", "Usage: dataporch connections <command>\n\nManage configured database connections."},
	{"connections import", "import a database connection", "Usage: dataporch connections import --id <id> --kind <postgres|mysql|sqlite>"},
	{"mcp-token", "manage the local MCP token", "Usage: dataporch mcp-token <command>\n\nManage the local MCP authentication token."},
	{"mcp-token create", "create the local MCP token", "Usage: dataporch mcp-token create"},
	{"mcp-token list", "display local MCP token status", "Usage: dataporch mcp-token list"},
	{"mcp-token rotate", "rotate the local MCP token", "Usage: dataporch mcp-token rotate"},
	{"mcp-token revoke", "revoke the local MCP token", "Usage: dataporch mcp-token revoke [--yes]"},
}

func writeRootHelp(writer io.Writer, version, path string) error {
	//nolint:dupword // Exact public help text intentionally repeats command names.
	lines := []string{
		"dataporch <command>", "", "Usage:", "",
		"dataporch run                 start the background user service",
		"dataporch run -f              run DataPorch in the current terminal",
		"dataporch restart             restart the background user service",
		"dataporch stop                stop the background user service",
		"dataporch status              display runtime status",
		"dataporch secrets init        initialize the local secret store",
		"dataporch connections import  import a database connection",
		"dataporch mcp-token <command> manage the local MCP token",
		"dataporch <command> -h        quick help on <command>",
		"dataporch -l                  display usage info for all commands",
		"dataporch help <command>      show help for an exact command",
		"dataporch help dataporch      show the complete overview",
		"", fmt.Sprintf("dataporch@%s %s", version, path), "",
	}
	return writeString(writer, strings.Join(lines, "\n"))
}

func writeLongHelp(writer io.Writer, version, path string) error {
	if err := writeRootHelp(writer, version, path); err != nil {
		return err
	}
	for _, command := range commandHelp {
		if err := writeString(writer, command.detail+"\n\n"); err != nil {
			return err
		}
	}
	return nil
}

func writeCommandHelp(writer io.Writer, target []string) error {
	path := strings.Join(target, " ")
	if path == "dataporch" {
		return nil
	}
	for _, command := range commandHelp {
		if command.path == path {
			return writeString(writer, command.detail+"\n")
		}
	}
	return usageError(fmt.Sprintf("unknown help topic %q; run dataporch -l", path), nil)
}

func helpRequest(args []string) (target []string, handled bool, err error) {
	if len(args) == 0 || (len(args) == 1 && (args[0] == "-h" || args[0] == "--help")) {
		return nil, true, nil
	}
	if len(args) > 0 && args[0] == "help" {
		if len(args) == 1 {
			return nil, false, usageError("help requires a command; run dataporch -l", nil)
		}
		return args[1:], true, nil
	}
	if len(args) > 1 && (args[len(args)-1] == "-h" || args[len(args)-1] == "--help") {
		return args[:len(args)-1], true, nil
	}
	return nil, false, nil
}

func writeString(writer io.Writer, value string) error {
	if writer == nil {
		return errors.New("standard output is required")
	}
	written, err := io.WriteString(writer, value)
	if err != nil {
		return fmt.Errorf("writing command output: %w", err)
	}
	if written != len(value) {
		return fmt.Errorf("writing command output: %w", io.ErrShortWrite)
	}
	return nil
}
