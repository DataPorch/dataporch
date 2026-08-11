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
| `DATAPORCH_ADMIN_SOCKET_PATH` | `/run/dataporch/admin.sock` | Sets the local Unix socket for connection administration. |
| `DATAPORCH_MASTER_KEY_PATH` | `/etc/dataporch/master.key` | Sets the local secret-store master key path. |
| `DATAPORCH_SECRETS_STORE_PATH` | `/var/lib/dataporch/secrets.store` | Sets the encrypted local secret-store path. |
| `DATAPORCH_CONNECTIONS_STORE_PATH` | `/var/lib/dataporch/connections.store` | Sets the normalized connection-definition store path. |

## Local secrets and connection imports

Initialize the local secret store once before adding connections:

```bash
dataporch secrets init
```

Start DataPorch normally. Add a database connection through its local admin socket:

```bash
dataporch connections import --id finance --kind postgres
```

The command reads the connection string from a hidden terminal prompt. It saves
normalized non-secret settings and encrypted local secret references; it does
not save the complete connection string. A successful import does not test,
open, ping, or authenticate to the database. The new definition becomes
available to the running process without a restart.

The local admin path uses a Unix socket. It is not exposed through public TCP
HTTP or MCP. Losing the master key makes locally stored secrets unrecoverable.
Root or compromise of the DataPorch process is outside the protection provided
by the local store.

Postgres imports accept `postgres://` and `postgresql://` URIs with a username,
password, one TCP host, a database, an optional explicit port, and an optional
`sslmode` value of `disable`, `allow`, `prefer`, `require`, `verify-ca`, or
`verify-full`. An omitted port stays omitted; DataPorch does not insert a
default port.

Import parses and stores the normalized definition only. Real Postgres
DataPorch now has an internal lazy Postgres runtime opener. The first internal
open authenticates within ten seconds, and later opens in the same process
reuse one pgx pool per database ID without pinging on every call. pgx may retire
idle physical connections while DataPorch retains the reusable pool object.

No HTTP, MCP, CLI, resource-discovery, health, or query operation exposes the
opener yet. Startup and connection import still do not contact PostgreSQL.
Pool capacity uses pgx defaults in this slice; configurable per-database maximum
connections remains a deferred scaling requirement.

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
internal/connection/           Built-in database adapter resolution
internal/connection/postgres/  PostgreSQL URI import and runtime opener
internal/catalog/              Resource metadata
internal/catalog/memory/       In-memory resource catalog
internal/execution/            Validated application operations
internal/access/               Access policy implementations
internal/transports/httpapi/   HTTP adapter
internal/transports/mcp/       MCP adapter
```

## License

DataPorch uses the Apache License, Version 2.0. See [LICENSE](LICENSE).
