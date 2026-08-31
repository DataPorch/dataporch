# DataPorch Agent Plugins

This package connects Codex and Claude Code to a separately installed DataPorch runtime. Both clients reuse the same focused skills for source discovery and bounded read-only queries; neither plugin installs or launches DataPorch.

## Local setup

Install DataPorch, then initialize and start the runtime:

```bash
dataporch secrets init
dataporch run
```

Install the Codex or Claude Code plugin using the instructions below. The plugin launches `dataporch mcp` over stdio and connects to the running same-machine runtime.

Local plugin users do not run `dataporch mcp-token create`, copy credentials, set `DATAPORCH_MCP_TOKEN`, or edit a shell profile. Authentication is initialized and rotated by the runtime, so new terminals and client restarts require no repeated setup.

The runtime-only local state includes:

```text
~/.dataporch/mcp.sock
~/.dataporch/mcp-control-token
```

Override these paths for a managed local deployment with `DATAPORCH_MCP_SOCKET_PATH` and `DATAPORCH_MCP_CONTROL_TOKEN_PATH`. The control credential is owner-only runtime state and is never included in plugin configuration, environment variables, logs, or normal command output.

## Codex

### Install from a checked-out repository

Add the absolute path to the DataPorch repository root as a local marketplace, then install the plugin:

```bash
codex plugin marketplace add /absolute/path/to/dataporch
codex plugin add dataporch@dataporch
```

Start a new Codex thread after installation so it loads the shared skills and MCP declaration.

### Install from Git

```bash
codex plugin marketplace add DataPorch/dataporch --ref main
codex plugin add dataporch@dataporch
```

Remote Git and fresh-machine acceptance belong to DAT-20. DAT-16 verifies the checked-out repository path.

### Update and remove

For an installed local checkout, remove and reinstall the cached plugin after changing the checkout:

```bash
codex plugin remove dataporch@dataporch
codex plugin add dataporch@dataporch
```

For the Git marketplace, refresh its snapshot and reinstall the plugin:

```bash
codex plugin marketplace upgrade dataporch
codex plugin add dataporch@dataporch
```

Remove the marketplace separately only when it is no longer wanted:

```bash
codex plugin marketplace remove dataporch
```

## Claude Code

### Install from a checked-out repository

```bash
claude plugin marketplace add /absolute/path/to/dataporch
claude plugin install dataporch@dataporch
```

Start Claude Code after installation. If it is already running, use `/reload-plugins` when the install summary asks you to reload.

### Install from Git

```bash
claude plugin marketplace add DataPorch/dataporch
claude plugin install dataporch@dataporch
```

Claude Code discovers the existing `source-discovery` and `bounded-query` skills from the plugin's default `skills/` directory. No manual `claude mcp add` command is required.

### Update and remove

```bash
claude plugin update dataporch@dataporch
claude plugin uninstall dataporch@dataporch
```

Reload plugins when prompted or start a new Claude Code session after an update. Remove the marketplace separately when it is no longer wanted:

```bash
claude plugin marketplace remove dataporch
```

## Direct HTTP MCP clients

Direct HTTP clients remain supported separately from the local plugin flow. Start the runtime, create a long-lived bearer token, and provide it only to the client process:

```bash
dataporch mcp-token create
export DATAPORCH_MCP_TOKEN='dp-...'
```

The HTTP endpoint is `http://127.0.0.1:8080/mcp`. Rotate, inspect, or revoke the token through the local admin interface:

```bash
dataporch mcp-token list
dataporch mcp-token rotate
dataporch mcp-token revoke
dataporch mcp-token revoke --yes
```

The token value must not be added to a plugin manifest, repository file, shell history, or logs. Remote cleartext bearer access is unsupported; hosted OAuth is separate work.

## Troubleshooting

### Not initialized

Run `dataporch secrets init`, then `dataporch run`, and reload the plugin.

### Runtime stopped

Run `dataporch run`. The `dataporch mcp` adapter does not start the runtime automatically.

### Unsafe local state or reported path

The runtime requires owner-only local state. Correct the reported socket or control-token path and its parent permissions, then restart the runtime. Do not copy or export the control credential.

### Installed behavior is stale

For Codex, remove and reinstall `dataporch@dataporch`, then start a new thread. For Claude Code, run `claude plugin update dataporch@dataporch` and reload plugins when prompted.

