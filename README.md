# DataPorch

DataPorch gives AI agents a standard interface to enterprise data. It supports MCP and HTTP APIs.

DataPorch does not control AI reasoning, plans, conversations, or model selection.

This repository contains the first functional data-discovery path. It includes:

- Validated configuration
- A controlled process lifecycle
- Live connection definitions with lazy PostgreSQL opening
- Progressive relational metadata discovery
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
- `POST /mcp` for Streamable HTTP MCP

The MCP endpoint exposes exactly four typed, read-only tools. Call them in this
order when an agent needs to build a query plan:

1. `data_source.list` — list configured source IDs and capability families. This
   is a local snapshot and does not check connectivity.
2. `relational_database.list_schemas` — list PostgreSQL schemas accessible to
   the configured database role.
3. `relational_database.list_tables` — list readable tables, partitioned
   tables, views, materialized views, and foreign tables for an exact schema.
4. `relational_database.list_columns` — list columns, PostgreSQL type details,
   defaults, identity/generated metadata, and relevant constraints for an
   exact schema and relation.

The three relational tools reuse the `source_id` returned by the first tool.
Schema and relation names are case-sensitive identifiers; pass the exact value
returned by the parent listing. Each operation supports a literal,
case-insensitive `search`, bounded `limit`, and an opaque stateless `cursor`.
Descriptions and comments are omitted unless `include_descriptions` is true.
PostgreSQL privilege predicates filter schemas, relations, columns, and
referenced constraints, so accessible system schemas may appear alongside
user schemas while unreadable objects do not.

The former MCP `list_resources` tool and HTTP `GET /v1/resources` route were
removed in favor of these typed capabilities.

## Configuration

Use environment variables to configure DataPorch.

| Variable | Default | Function |
| --- | --- | --- |
| `DATAPORCH_HTTP_ADDRESS` | `127.0.0.1:8080` | Sets the HTTP listen address. |
| `DATAPORCH_RESOURCE_LIMIT` | `100` | Sets the maximum number of items returned by one discovery page. |
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

Import parses and stores the normalized definition only. DataPorch uses an
internal lazy Postgres runtime opener: the first open authenticates within ten
seconds, later opens reuse one pgx pool per source ID, and pgx may retire idle
physical connections while DataPorch retains the reusable pool object.

Discovery catalog queries have a separate twenty-second bound. The public HTTP
server has a thirty-five-second write bound so a single request can safely cover
opening plus one metadata query. Startup, health checks, `data_source.list`, and
connection import do not contact PostgreSQL. The allow-all policy remains a
development default; replace it before production deployment.

## Development

Use these commands:

```bash
make build
make test
make test-race
make test-integration \
  DATAPORCH_TEST_POSTGRES_DSN='postgres://user:password@127.0.0.1:5432/database?sslmode=disable'
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
internal/execution/            Validated application operations
internal/access/               Access policy implementations
internal/transports/httpapi/   HTTP adapter
internal/transports/mcp/       MCP adapter
```

## License

DataPorch uses the Apache License, Version 2.0. See [LICENSE](LICENSE).
