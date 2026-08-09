# DataPorch

DataPorch gives AI agents a standard interface to enterprise data. It supports MCP and HTTP APIs.

DataPorch does not control AI reasoning, plans, conversations, or model selection.

This repository contains the first functional path. The path includes these components:

- Validated configuration
- A controlled process lifecycle
- An in-memory connector
- Resource discovery with a result limit
- HTTP and MCP transports that use the same execution service

## Architecture

DataPorch uses one Go module. The module contains separate internal packages.

```text
HTTP / MCP -> execution service -> connector interfaces
                                <- connector implementations
```

The transport packages call the execution service. The execution service calls connector interfaces. Connector packages implement these interfaces.

The code uses manual constructor injection. It does not use a dependency injection framework.

The `cmd/dataporch` directory contains the program entry point. Private application code is in the `internal` directory.

The workspace stores architecture decision records outside this repository.

## Requirements

Install these tools:

- Go 1.25 or a later version
- golangci-lint v2.12.0 for `make lint`
- govulncheck for `make audit`

The Makefile does not install these tools. Go can download modules that are not in the local module cache.

## Start DataPorch

Run this command:

```bash
make run
```

By default, DataPorch listens on `127.0.0.1:8080`.

DataPorch supplies these endpoints:

- `GET /healthz`
- `GET /v1/resources?limit=10`
- `POST /mcp` for Streamable HTTP MCP

The MCP endpoint includes the `list_resources` tool.

## Configuration

Use environment variables to configure DataPorch.

| Variable | Default | Function |
| --- | --- | --- |
| `DATAPORCH_HTTP_ADDRESS` | `127.0.0.1:8080` | Sets the HTTP listen address. |
| `DATAPORCH_RESOURCE_LIMIT` | `100` | Sets the maximum number of resources in one response. |

The initial configuration uses an allow-all access policy and in-memory resources. Do not use these components for production access control or storage.

## Development

Use these commands:

```bash
make build
make test
make test-race
make vet
make lint
make audit
make check
```

The `make check` command checks formatting and module files. It also runs tests, lint checks, and a production build.

## Project layout

```text
cmd/dataporch/                 Program entry point
internal/app/                  Dependency setup and process lifecycle
internal/config/               Environment configuration
internal/catalog/              Resource metadata
internal/execution/            Validated application operations
internal/access/               Access policy implementations
internal/connectors/memory/    Initial in-memory connector
internal/transports/httpapi/   HTTP adapter
internal/transports/mcp/       MCP adapter
```

## License

DataPorch uses the Apache License, Version 2.0. See [LICENSE](LICENSE).
