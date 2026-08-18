# DataPorch

DataPorch gives AI agents a standard interface to enterprise data. It supports MCP and HTTP APIs.

DataPorch does not control AI reasoning, plans, conversations, or model selection.

This repository contains the first functional data-discovery path. It includes:

- Validated configuration
- A controlled process lifecycle
- Live connection definitions with lazy relational-database opening
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

- Go 1.25.12 or a later version
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

### MCP compatibility and transport security

DataPorch v0.1 guarantees MCP revision `2026-07-28` over stateless Streamable
HTTP. Each JSON-RPC request uses a separate `POST /mcp` request with its protocol
version and client metadata attached. DataPorch does not depend on the legacy
`initialize`/`notifications/initialized` handshake, GET streams, or
`Mcp-Session-Id`.

Older MCP revisions that the pinned Go SDK happens to accept are not part of the
DataPorch v0.1 compatibility guarantee. Supporting another revision requires an
explicit compatibility decision and DataPorch-level conformance tests.

The server binds to `127.0.0.1:8080` by default. The MCP endpoint rejects invalid
Origin and localhost Host headers before tool execution. Changing
`DATAPORCH_HTTP_ADDRESS` does not disable those protections. `/mcp` also requires
one `Authorization: Bearer ...` header; missing, malformed, duplicate, and
invalid credentials are rejected before MCP execution. `/healthz` remains
unauthenticated.

Manage the one local MCP token through the Unix admin socket after starting the
runtime:

```bash
dataporch mcp-token create
export DATAPORCH_MCP_TOKEN='dp-...'
dataporch mcp-token list
dataporch mcp-token rotate
dataporch mcp-token revoke
```

The plaintext token is shown only by `create` and `rotate`. The server keeps the
active verifier in memory and persists only its SHA-256 digest and timestamps.
`DATAPORCH_MCP_TOKEN` is client-side configuration; the server-side verifier
path is `DATAPORCH_MCP_TOKEN_STORE_PATH`. This is local Bearer access control,
not OAuth authorization. Keep the default loopback boundary; exposing cleartext
Bearer tokens remotely is unsupported.

The MCP endpoint exposes exactly five typed tools. Call them in this order when
an agent needs to inspect a relational source and run a bounded query:

1. `data_source.list` — list configured source IDs and capability families. This
   is a local snapshot and does not check connectivity.
2. `relational_database.list_schemas` — list schemas exposed by the configured
   relational adapter.
3. `relational_database.list_tables` — list readable tables and adapter-supported
   relation kinds for an exact schema.
4. `relational_database.list_columns` — list columns, adapter type details,
   defaults, generated metadata, and representable constraints for an exact
   schema and relation.
5. `relational_database.query` — execute one complete row-producing statement
   against a configured relational source within the adapter's read-only
   policy.

The query tool requires exactly these fields:

```json
{
  "kind": "postgres",
  "source_id": "finance",
  "query": "SELECT id, total FROM invoices ORDER BY id"
}
```

The caller supplies one opaque SQL statement. PostgreSQL parses it; DataPorch
does not parse or rewrite the statement. PostgreSQL's extended protocol rejects
multiple commands, and DataPorch does not add an HTTP query endpoint.

The three relational tools reuse the `source_id` returned by the first tool.
Schema and relation names are case-sensitive identifiers; pass the exact value
returned by the parent listing. Each operation supports a literal,
case-insensitive `search`, bounded `limit`, and an opaque stateless `cursor`.
Descriptions and comments are omitted unless `include_descriptions` is true.
Each adapter applies its own visibility and safety policy. PostgreSQL privilege
predicates filter schemas, relations, columns, and referenced constraints, while
SQLite exposes its `main` catalog and readable objects only.

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
| `DATAPORCH_MCP_TOKEN_STORE_PATH` | `/var/lib/dataporch/mcp-token.json` | Sets the server-side MCP token verifier path. |
| `DATAPORCH_QUERY_TIMEOUT` | `20s` | Bounds each query call; accepts `1s` through `20s` and cannot be disabled. |
| `DATAPORCH_QUERY_RESPONSE_BYTE_LIMIT` | `10485760` | Bounds the encoded MCP tool result; accepts `65536` through `10485760` and cannot be disabled. |
| `DATAPORCH_QUERY_TRUNCATION_ENABLED` | `true` | Reads one extra row to report whether the configured row limit truncated the result. |
| `DATAPORCH_QUERY_ROW_LIMIT` | `1000` | Sets the positive returned-row limit while truncation is enabled; ignored when truncation is disabled. |

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

MCP access tokens use the same local admin boundary:

```bash
dataporch mcp-token create
dataporch mcp-token list
dataporch mcp-token rotate
dataporch mcp-token revoke
dataporch mcp-token revoke --yes
```

`create` and `rotate` display the plaintext once; set it as
`DATAPORCH_MCP_TOKEN` in the environment of the MCP client. `list` exposes only
state and timestamps. `revoke --yes` is also the recovery operation for a
corrupt or unsafe verifier file when removing that configured file is safe.

Postgres imports accept `postgres://` and `postgresql://` URIs with a username,
password, one TCP host, a database, an optional explicit port, and an optional
`sslmode` value of `disable`, `allow`, `prefer`, `require`, `verify-ca`, or
`verify-full`. An omitted port stays omitted; DataPorch does not insert a
default port.

Import parses and stores the normalized definition only. DataPorch uses an
internal lazy Postgres runtime opener: the first open authenticates within ten
seconds, later opens reuse one pgx pool per source ID, and pgx may retire idle
physical connections while DataPorch retains the reusable pool object.

### MySQL adapter

The MySQL adapter supports MySQL 8.4 LTS. Imports use this URI form:

```text
mysql://username:password@host[:port]/database[?sslmode=<mode>]
```

Supported `sslmode` values are `disable`, `prefer`, `require`, and
`verify-full`. An omitted port uses runtime port `3306`; an omitted `sslmode`
uses `prefer`. Import is offline and reports `connectionTested: false`. Each
source addresses exactly one database, so multiple MySQL databases require
multiple source definitions. Discovery exposes only the imported database.

The adapter returns typed table, view, column, and representable constraint
metadata. MySQL native type metadata is preserved and does not use SQLite
affinity. Constraints are returned with the first paginated column page.

Queries are one opaque, parameter-free, row-producing statement. Read-only
transactions enforce MySQL's database-level read-only behavior, and
multi-statements are disabled. Binary cells use deterministic uppercase
literals such as `X'00FF'`. The existing DataPorch time, row, and encoded-byte
limits apply; object restrictions belong in MySQL grants.

Query sessions are isolated and physically discarded after each query, so
session-local state is not reused by a later query.

### SQLite adapter

SQLite imports use an exact, offline-only URI with an absolute path:

```text
sqlite:///absolute/path/database.db
```

Import validates only the URI syntax. It does not open the file, create a
database, check connectivity, or run a query, and the import response reports
`connectionTested: false`. The normalized path is stored through the encrypted
local secret store; connection errors and operational logs redact the path.

On first use, the adapter requires an existing, non-empty regular file. It
rejects missing, empty, directory, final-symlink, malformed, corrupt, and
inaccessible files without creating a database or sidecar. Every operation
opens a fresh physical connection with read-only, URI, and no-follow flags,
defensive settings, trusted-schema disabled, query-only mode, and no idle
connection pool. Memory, temporary, attached, encrypted, extension-loading,
DDL, and SQL-text parsing workflows are unsupported.

SQLite exposes only the `main` schema. Ordinary tables, views, and virtual
tables map to the corresponding relation kinds. Declared column text is
preserved while the dynamic type metadata reports SQLite affinity (`integer`,
`text`, `blob`, `real`, or `numeric`); generated columns identify virtual or
stored generation. Primary keys, representable unique indexes, and foreign
keys are exposed as structural constraints. Constraint names, partial or
expression indexes, unsupported deferrability details, and other SQLite
metadata that has no safe representation are omitted.

The query contract is one opaque, parameter-free, row-producing statement.
Cells are returned as strings or JSON `null`; BLOB cells use uppercase SQLite
literals such as `X'00FF'`. Committed updates are visible on the next
operation, and atomic file replacement is observed without retaining a session
or physical connection. WAL reads do not use immutable mode and do not modify
the database or WAL payload; SQLite may update `-shm` lock/read-mark
bookkeeping while a live writer is present. Filesystem permission failures are
reported as safe unavailable errors.

SQLite query logs contain operation, adapter kind, source identity, size,
duration, row count, and outcome metadata. They never include raw SQL, paths,
DSNs, secrets, result cells, or stack traces.

Discovery catalog queries have a separate twenty-second bound. Query calls have
the timeout and encoded-response ceilings above. Disabling row truncation does
not disable either mandatory ceiling. Callers should add an `ORDER BY` when
stable truncation order matters.

Query results use ordered columns and positional rows. Values are text, SQL
`NULL` remains `null`, and `row_count` reports the returned rows:

```json
{
  "kind": "postgres",
  "source_id": "finance",
  "columns": [
    {"name": "id", "database_type": "int8"},
    {"name": "note", "database_type": "text"}
  ],
  "rows": [["101", null]],
  "row_count": 1,
  "truncated": false
}
```

Query failures may include every PostgreSQL server field in the approved
`database_error` object:

```json
{
  "category": "database_conflict",
  "message": "serialization failure",
  "retryable": true,
  "database_error": {
    "kind": "postgres",
    "code": "40001",
    "severity": "ERROR",
    "message": "serialization failure"
  }
}
```

Every query attempt logs operation, kind, source ID, duration, query size, and
result or failure metadata at INFO or WARN. Raw SQL, result cells, credentials,
DSNs, resolved settings, secret references, and filesystem paths are not log
fields.

`AllowAll` initially permits any MCP caller to query any configured PostgreSQL
source. MCP network reachability and the PostgreSQL identity stored for each
source are the initial boundaries. PostgreSQL grants, ownership, row-level
security, security-definer functions, external functions, extensions, and
foreign wrappers remain operator responsibilities. `READ ONLY` does not
guarantee that arbitrary functions have no external side effects. Use separate
source IDs and credentials for separate access levels.

Query endpoints require direct connections or session pooling. Transaction and
statement pooling are unsupported and are not auto-detected because rollback
cleanup must target the same backend session. A connection is reused only after
rollback, `DEALLOCATE ALL`, and `DISCARD ALL`; uncertain sessions are removed
from the pool and physically closed.

The public HTTP server has a thirty-five-second write bound so a single request
can safely cover opening plus one metadata query. Startup, health checks,
`data_source.list`, and connection import do not contact PostgreSQL. The
allow-all policy remains a development default; replace it before production
deployment.

## Agent plugins

The repository includes plugins for Codex and Claude Code that connect to the separately installed local DataPorch runtime. Both clients reuse the same source-discovery and bounded-query skills; neither plugin installs or launches the runtime.

See [the agent plugin guide](plugins/dataporch/README.md) for prerequisites, authentication, installation, updates, removal, and troubleshooting for each client.

## Development

Use these commands:

```bash
make build
make test
make test-race
make test-integration-sqlite
DATAPORCH_TEST_POSTGRES_DSN='postgres://user:password@127.0.0.1:5432/database?sslmode=disable' \
  make test-integration-postgres
DATAPORCH_TEST_MYSQL_DSN='mysql://user:password@127.0.0.1:3306/database?sslmode=disable' \
  make test-integration-mysql
DATAPORCH_TEST_POSTGRES_DSN='postgres://user:password@127.0.0.1:5432/database?sslmode=disable' \
  make test-integration
make test-cgo-disabled
make build-cgo-disabled
make vet
make lint
make audit
make check
```

CI keeps adapter-neutral Go checks separate from database integration jobs. Each
external database adapter owns a focused integration target and CI job; supported
database versions belong inside that adapter's job rather than in the core Go
matrix. App-level adapter integration tests must keep the adapter name in the
parent test name and end in `Integration` so the focused Make targets can select
them without adding adapter-specific build tags.

The `make check` command checks formatting and module files. It also runs tests, lint checks, and a production build.

## Project layout

```text
cmd/dataporch/                 Program entry point
internal/app/                  Dependency setup and process lifecycle
internal/config/               Environment configuration
internal/connection/           Built-in database adapter resolution
internal/connection/mysql/     MySQL URI import, discovery, and read-only runtime
internal/connection/postgres/  PostgreSQL URI import and runtime opener
internal/connection/sqlite/    SQLite URI import, discovery, and read-only runtime
internal/execution/            Validated application operations
internal/access/               Access policy implementations
internal/transports/httpapi/   HTTP adapter
internal/transports/mcp/       MCP adapter
```

## License

DataPorch uses the Apache License, Version 2.0. See [LICENSE](LICENSE).
