# DataPorch Agent Plugins

This package connects Codex and Claude Code to a separately installed DataPorch runtime. Both clients reuse the same focused skills for source discovery and bounded read-only queries; neither plugin installs or launches DataPorch.

## Prerequisites

- A current Codex CLI with plugin support, Claude Code with plugin support, or both.
- A separately installed DataPorch runtime listening at `http://127.0.0.1:8080/mcp`.
- DataPorch source configuration completed through the runtime's local administration interface.
- A local MCP token created through `dataporch mcp-token create`.
- `DATAPORCH_MCP_TOKEN` exported to the process that launches the client.

Keep the runtime and plugin on the same local machine unless a separate TLS and authorization boundary is provided.

## Authentication

The package contains only the environment-variable name `DATAPORCH_MCP_TOKEN`. It does not contain, issue, print, or persist a token. The runtime stores only a SHA-256 verifier at the server-side `DATAPORCH_MCP_TOKEN_STORE_PATH`; the client-side `DATAPORCH_MCP_TOKEN` is the plaintext credential used by the MCP connection.

Start the runtime, then create the token through its local Unix admin socket:

```bash
dataporch mcp-token create
```

Make the displayed value available to the shell that launches the client without writing it into this repository:

```bash
read -rsp 'DataPorch MCP token: ' DATAPORCH_MCP_TOKEN
export DATAPORCH_MCP_TOKEN
```

Codex reads the token through its existing `.mcp.json` declaration. Claude Code sends `Authorization: Bearer ${DATAPORCH_MCP_TOKEN}` from its plugin manifest.

Rotate or revoke through the same local interface when needed:

```bash
dataporch mcp-token list
dataporch mcp-token rotate
dataporch mcp-token revoke
dataporch mcp-token revoke --yes
```

The package does not support a static credential, OAuth fallback, another credential variable, or a configurable endpoint. This feature provides local Bearer access control, not full OAuth authorization; remote cleartext Bearer exposure is unsupported.

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
codex plugin marketplace add adamraziv/dataporch --ref main
codex plugin add dataporch@dataporch
```

Remote Git and fresh-machine acceptance belong to DAT-20. DAT-16 verifies the checked-out repository path.

### Update

For an installed local checkout, remove and reinstall the cached plugin after changing the checked-out source:

```bash
codex plugin remove dataporch@dataporch
codex plugin add dataporch@dataporch
```

For the Git marketplace, refresh its snapshot and reinstall the plugin:

```bash
codex plugin marketplace upgrade dataporch
codex plugin add dataporch@dataporch
```

Start a new Codex thread after reinstalling.

### Remove

```bash
codex plugin remove dataporch@dataporch
```

Remove the marketplace separately only when it is no longer wanted:

```bash
codex plugin marketplace remove dataporch
```

## Claude Code

### Install from a checked-out repository

Add the DataPorch repository as a local marketplace, then install the plugin:

```bash
claude plugin marketplace add /absolute/path/to/dataporch
claude plugin install dataporch@dataporch
```

Start Claude Code after installation. If Claude Code is already running, use `/reload-plugins` when the install summary asks you to reload.

### Install from Git

```bash
claude plugin marketplace add adamraziv/dataporch
claude plugin install dataporch@dataporch
```

Claude Code discovers the existing `source-discovery` and `bounded-query` skills from the plugin's default `skills/` directory. No manual `claude mcp add` command is required.

### Update

```bash
claude plugin update dataporch@dataporch
```

Reload plugins when prompted or start a new Claude Code session to use the updated package.

### Remove

```bash
claude plugin uninstall dataporch@dataporch
```

Remove the marketplace separately only when it is no longer wanted:

```bash
claude plugin marketplace remove dataporch
```

## Troubleshooting

### DataPorch tools are unavailable

Confirm the separately managed runtime is listening on `127.0.0.1:8080`. Codex treats its DataPorch MCP declaration as non-required. In Claude Code, use `/mcp` to inspect the plugin-provided `dataporch` server and reconnect after the runtime is available.

### The client cannot see the token

Set and export `DATAPORCH_MCP_TOKEN` in the environment of the process that launches Codex or Claude Code, then restart or reload the client. Do not add the token value to `.mcp.json`, either plugin manifest, a skill, or repository documentation.

### The runtime rejects the token

Run `dataporch mcp-token list` through the local admin socket. If the token was rotated, update `DATAPORCH_MCP_TOKEN` and restart or reload the client. If the verifier store is degraded, use `dataporch mcp-token revoke --yes` to attempt recovery, then create a new token. Neither plugin has a fallback credential mechanism.

### Two DataPorch MCP servers appear

Keep the plugin-provided server named `dataporch`. Remove any manually added duplicate instead of creating an unauthenticated workaround.

### Installed behavior is stale

For Codex, remove and reinstall `dataporch@dataporch`, then start a new thread. For Claude Code, run `claude plugin update dataporch@dataporch` and reload plugins when prompted.
