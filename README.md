# DataPorch

[![CI](https://github.com/adamraziv/dataporch/actions/workflows/ci.yml/badge.svg)](https://github.com/adamraziv/dataporch/actions/workflows/ci.yml)
[![License](https://img.shields.io/github/license/adamraziv/dataporch)](LICENSE)


DataPorch is an open-source data access layer that lets AI agents discover and query relational databases through MCP without exposing database credentials directly to the agent.

PostgreSQL · SQLite · MySQL · MCP · Codex · Claude Code

---

## Give agents data access, not database access

Giving an AI agent a database connection also gives its tool layer the credentials and privileges behind that connection.

**Without DataPorch**

```text
AI Agent
    │
    │ database credentials
    ▼
Your Database
```

**With DataPorch**

```text
AI Agent
    │
    │ MCP
    ▼
┌─────────────────────┐
│      DataPorch      │
│                     │
│  Discover schema    │
│  Keep secrets local │
│  Enforce read-only  │
│  Bound execution    │
└─────────────────────┘
    │
    ▼
Your Database
```

The agent only works with source IDs and database metadata. Credentials, DSNs, and secret references stay inside DataPorch.

---

## Protecting your database from hallucinations

An agent investigating an issue decides to change production data.

**❌ Direct database access**

```sql
UPDATE invoices
SET status = 'paid'
WHERE customer_id = 42
RETURNING id, status;
```

The database executes the write if the supplied credentials allow it.

**✅ Through DataPorch**

```text
> relational_database.query

UPDATE invoices
SET status = 'paid'
WHERE customer_id = 42
RETURNING id, status;

Result: rejected by the database read-only boundary
```

DataPorch executes queries through read-only database sessions. The agent cannot turn the query tool into a general-purpose write connection.

---

An agent asks for far more data than it actually needs.

**❌ Direct database access**

```sql
SELECT * FROM events;
```

The query can continue returning rows until the database, client, network, or agent gives up.

**✅ Through DataPorch**

```text
> relational_database.query

SELECT * FROM events;

1,000 rows returned
truncated: true
```

DataPorch applies mandatory execution limits, including query time and encoded response size, with a bounded row limit enabled by default.

Guardrails live between the agent and the database instead of depending on the agent to remember them.

---

## How it works

DataPorch gives agents a small set of typed MCP tools.

```text
data_source.list
        ↓
relational_database.list_schemas
        ↓
relational_database.list_tables
        ↓
relational_database.list_columns
        ↓
relational_database.query
```

The agent starts with configured source IDs. It can then inspect schemas, tables, and columns before it sends a query.

Each database adapter implements the same discovery and query contract. DataPorch does not manage prompts, models, conversations, or agent reasoning. Your agent decides what to ask. DataPorch controls the data access path.

---

## Quick start

DataPorch requires Go 1.25 or later when installed with Go. The preferred
Homebrew installation is documented after the formula is accepted into
`homebrew/core`; until then, install the published command with Go:

```bash
go install github.com/adamraziv/dataporch/cmd/dataporch@latest
```

The exact-version, reproducible installation becomes available when `v0.1.0`
is published:

```bash
go install github.com/adamraziv/dataporch/cmd/dataporch@v0.1.0
```

Contributors working from a checkout can use `make install`. Go installs the
binary into `GOBIN`, or the default Go bin directory, which must be on `PATH`.
Installation does not initialize DataPorch, create keys, register a service,
or start a process.

Initialize the per-user local state and start the native background service:

```bash
dataporch secrets init
dataporch run
dataporch status
```

Bare `dataporch`, `dataporch -h`, and `dataporch --help` display the command
overview. Use `dataporch run -f` for the foreground process in a terminal,
container, CI job, or external supervisor. Bare invocation no longer starts a
long-running process.

The background service uses `launchd` on macOS and `systemd --user` on Linux.
`dataporch status` reports the PID, configured address, and the applicable log
location. It returns exit code `3` when the service is stopped. DataPorch does
not enable login startup; `restart` refreshes the service definition after an
upgrade, while `stop` unregisters only the generated service definition.

The default state is kept under `~/.dataporch`:

```text
~/.dataporch/admin.sock
~/.dataporch/master.key
~/.dataporch/secrets.store
~/.dataporch/connections.store
~/.dataporch/mcp-token.json
~/.dataporch/logs/
```

For containers, system services, or an explicitly managed deployment, set the
individual `DATAPORCH_*_PATH` overrides and use foreground mode. Existing
state is never removed by `stop`, upgrades, rollback, or binary removal.

```bash
export DATAPORCH_ADMIN_SOCKET_PATH=/run/dataporch/admin.sock
export DATAPORCH_MASTER_KEY_PATH=/etc/dataporch/master.key
export DATAPORCH_SECRETS_STORE_PATH=/var/lib/dataporch/secrets.store
export DATAPORCH_CONNECTIONS_STORE_PATH=/var/lib/dataporch/connections.store
export DATAPORCH_MCP_TOKEN_STORE_PATH=/var/lib/dataporch/mcp-token.json
dataporch run -f
```

DataPorch listens on `127.0.0.1:8080` by default.

Upgrade or roll back an exact Go installation, then refresh the running
service:

```bash
go install github.com/adamraziv/dataporch/cmd/dataporch@v0.1.1
dataporch restart

dataporch stop
```

There is no `dataporch start` command and no `dataporch run --foreground`
alias. Use `dataporch run` for the native user service and `dataporch run -f`
for foreground execution.

### Connect a database

Import a connection through the local administration socket:

| Database   | Connection format                               |
| ---------- | ----------------------------------------------- |
| PostgreSQL | `postgres://user:password@host[:port]/database` |
| MySQL      | `mysql://user:password@host[:port]/database`    |
| SQLite     | `sqlite:///absolute/path/database.db`           |

```bash
dataporch connections import --id finance --kind postgres
```

DataPorch reads the connection string from a hidden terminal prompt. The MCP interface does not receive the connection string.
Change `--kind` and enter the matching connection format for MySQL or SQLite.

### Create an MCP token

```bash
dataporch mcp-token create
```

Export the token in the environment that starts your MCP client:

```bash
export DATAPORCH_MCP_TOKEN='dp-...'
```

DataPorch shows the plaintext token only when you create or rotate it.

---

## Connect an agent

DataPorch includes plugins for Codex and Claude Code. Both plugins connect to the same local MCP service.

### Codex

```bash
codex plugin marketplace add /absolute/path/to/dataporch
codex plugin add dataporch@dataporch
```

### Claude Code

```bash
claude plugin marketplace add /absolute/path/to/dataporch
claude plugin install dataporch@dataporch
```

See [`plugins/dataporch/README.md`](plugins/dataporch/README.md) for installation, authentication, updates, removal, and troubleshooting.

---

## Compatibility

| Component     | Support                          |
| ------------- | -------------------------------- |
| PostgreSQL    | Tested with PostgreSQL 14 and 18 |
| SQLite        | Supported                        |
| MySQL         | MySQL 8.4 LTS                    |
| MCP           | `2026-07-28`                     |
| Agent clients | Codex and Claude Code            |
| Go            | 1.25+                            |

---

## Security

DataPorch keeps the database access boundary outside the agent.

* **Local by default:** The MCP service listens on `127.0.0.1:8080`.
* **Authenticated MCP:** MCP requests require a Bearer token.
* **Local credentials:** Agents receive source IDs instead of credentials, DSNs, or secret references.
* **Encrypted secrets:** DataPorch encrypts stored connector credentials.
* **Read-only queries:** Database adapters execute queries through read-only paths.
* **Bounded execution:** DataPorch limits query time and encoded response size. It limits returned rows by default.

Database permissions still define what the configured database identity can read. Read-only SQL does not make arbitrary database functions free of external side effects.

**Keep the default local network boundary unless you add secure transport and authorization for remote access.**

---

## Development

Run the local quality gate:

```bash
make check
```

Run database integration tests:

```bash
make test-integration-postgres
make test-integration-sqlite
make test-integration-mysql
```

PostgreSQL and MySQL integration tests require their corresponding test connection strings.

---

## License

DataPorch is licensed under [Apache-2.0](LICENSE).
