# DataPorch Codex Plugin

This plugin connects Codex to a separately installed DataPorch runtime. It bundles the local MCP connection and focused skills for source discovery and bounded read-only queries; it does not install or launch DataPorch.

## Prerequisites

- A current Codex CLI with plugin support.
- A separately installed DataPorch runtime listening at `http://127.0.0.1:8080/mcp`.
- DataPorch source configuration completed through the runtime's local administration interface.
- `DATAPORCH_MCP_TOKEN` available to the process that launches Codex once DAT-15 supplies the token lifecycle.

This is a prerelease distribution package. Its Codex manifest is intentionally unversioned, and the current development runtime does not enforce bearer tokens until DAT-15 is delivered. Do not describe this package alone as the secure `0.1.0` experience.

## Authentication

The plugin contains only the environment-variable name `DATAPORCH_MCP_TOKEN`. It does not contain, issue, print, or persist a token.

After DAT-15 provides a token, make it available to the shell that launches Codex without writing it into this repository:

```bash
read -rsp 'DataPorch MCP token: ' DATAPORCH_MCP_TOKEN
export DATAPORCH_MCP_TOKEN
codex
```

The plugin does not support a static authorization header, OAuth fallback, another credential variable, or a configurable endpoint.

## Install from a checked-out repository

From any shell, add the absolute path to the DataPorch repository root as a local marketplace, then install the plugin:

```bash
codex plugin marketplace add /absolute/path/to/dataporch
codex plugin add dataporch@dataporch
```

Start a new Codex thread after installation so it loads the new skills and MCP declaration.

## Install from Git

The intended repository-marketplace commands are:

```bash
codex plugin marketplace add adamraziv/dataporch --ref main
codex plugin add dataporch@dataporch
```

Remote Git and fresh-machine acceptance belong to DAT-20. DAT-16 verifies the checked-out repository path.

## Update

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

## Remove

Remove the plugin while keeping the marketplace available:

```bash
codex plugin remove dataporch@dataporch
```

Remove the marketplace separately only when it is no longer wanted:

```bash
codex plugin marketplace remove dataporch
```

Removing the plugin or marketplace does not remove the DataPorch runtime, its source definitions, or its local secret store.

## Troubleshooting

### DataPorch tools are unavailable

Confirm the separately managed runtime is listening on `127.0.0.1:8080`. The MCP server is non-required, so an unavailable runtime must not prevent Codex from starting.

### Codex cannot see the token

Set and export `DATAPORCH_MCP_TOKEN` in the environment of the process that launches Codex, then restart Codex. Do not add the value to `.mcp.json`, `plugin.json`, a skill, or repository documentation.

### The runtime rejects the token

After DAT-15 is delivered, obtain a valid token through its documented runtime interface and restart or reconnect Codex. The plugin has no fallback credential mechanism.

### Two DataPorch MCP servers appear

Keep the plugin-provided server named `dataporch`. Remove any manually added duplicate instead of creating an unauthenticated workaround.

### Installed behavior is stale

Codex loads plugins from its cache. Remove and reinstall `dataporch@dataporch`, then start a new thread.
