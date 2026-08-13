---
name: bounded-query
description: Answer data questions by discovering missing relational context and executing one bounded read-only PostgreSQL statement through DataPorch MCP. Use when a user asks to retrieve, summarize, compare, or inspect rows from a configured DataPorch source.
---

# Run a Bounded DataPorch Query

Use only tools from the `dataporch` MCP server.

## Workflow

1. Identify the data question and any known `source_id`, schema, relation, columns, filters, grouping, or ordering.
2. When required identifiers are missing, call `data_source.list`, `relational_database.list_schemas`, `relational_database.list_tables`, and `relational_database.list_columns` progressively until the query can use exact returned identifiers.
3. Construct one complete row-producing PostgreSQL statement. Select only useful columns, apply relevant filters, and add stable ordering when order or truncation matters.
4. Call `relational_database.query` with exactly `kind: postgres`, the selected `source_id`, and the complete statement in `query`.
5. Execute directly when the request and identifiers are sufficiently clear; do not add a mandatory confirmation step.

## Result Interpretation

- Pair each positional row value with the corresponding ordered entry in `columns`.
- Preserve SQL `NULL` as null or an explicit missing value; never convert it to an empty string or zero.
- Report `row_count` as the number of returned rows.
- Report `truncated` accurately. When it is true, state that the result is partial and do not present it as a complete answer.
- Preserve structured query failures, including retryability and database metadata, without inventing rows or a successful result.

## Constraints

- Do not submit mutations, multiple statements, transaction control, or a statement that does not return rows.
- Do not connect directly to PostgreSQL.
- Do not replace DataPorch tools with shell commands or direct HTTP requests.
- Do not infer identifiers, permissions, connectivity, rows, or completeness that DataPorch did not return.
- Resolve genuine ambiguity through normal agent behavior; this skill does not prescribe a conversation script.

If DataPorch is unavailable, or the user asks for `curl`, `psql`, a shell command, or direct database access, refuse that fallback and explain that queries must use the `dataporch` MCP tools. If the user asks for a mutation, refuse it without calling `relational_database.query`; do not rewrite it as an unrequested update workflow.
