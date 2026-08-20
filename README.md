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

DataPorch currently builds from source. You need Go 1.25 or later.

```bash
git clone https://github.com/adamraziv/dataporch.git
cd dataporch
make build
```

Keep source-build state in the repository directory:

```bash
mkdir -p .dataporch

export DATAPORCH_ADMIN_SOCKET_PATH="$PWD/.dataporch/admin.sock"
export DATAPORCH_MASTER_KEY_PATH="$PWD/.dataporch/master.key"
export DATAPORCH_SECRETS_STORE_PATH="$PWD/.dataporch/secrets.store"
export DATAPORCH_CONNECTIONS_STORE_PATH="$PWD/.dataporch/connections.store"
export DATAPORCH_MCP_TOKEN_STORE_PATH="$PWD/.dataporch/mcp-token.json"
```

Initialize the local secret store:

```bash
./bin/dataporch secrets init
```

Start DataPorch:

```bash
./bin/dataporch
```

DataPorch listens on `127.0.0.1:8080` by default.

### Connect a database

Import a connection through the local administration socket:

| Database   | Connection format                               |
| ---------- | ----------------------------------------------- |
| PostgreSQL | `postgres://user:password@host[:port]/database` |
| MySQL      | `mysql://user:password@host[:port]/database`    |
| SQLite     | `sqlite:///absolute/path/database.db`           |

```bash
./bin/dataporch connections import --id finance --kind postgres
```

DataPorch reads the connection string from a hidden terminal prompt. The MCP interface does not receive the connection string.
Change `--kind` and enter the matching connection format for MySQL or SQLite.

### Create an MCP token

```bash
./bin/dataporch mcp-token create
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
