---
name: source-discovery
description: Discover configured DataPorch data sources and progressively inspect relational schemas, tables, and columns through MCP. Use when a user asks what data is available, needs an exact source, schema, table, or column identifier, or needs catalog context before a query.
---

# Discover DataPorch Sources

Use only tools from the `dataporch` MCP server.

## Workflow

1. Call `data_source.list` to obtain configured source IDs and capability families.
2. Call `relational_database.list_schemas` only for a relational source whose schemas are relevant to the request.
3. Call `relational_database.list_tables` with the exact `source_id` and `schema` returned by the preceding calls.
4. Call `relational_database.list_columns` only for relations relevant to the request, using the exact `source_id`, `schema`, and `table` returned by their parent calls.
5. Use `search`, bounded `limit` values, and opaque `cursor` values to narrow or continue discovery instead of loading a complete catalog by default.
6. Request descriptions only when comments would help answer the request.

## Output

- Preserve the exact spelling and case of every returned identifier.
- Distinguish configured metadata from verified connectivity: `data_source.list` is a local snapshot and does not contact the source.
- Report pagination or incomplete discovery when more results may exist.
- Preserve a DataPorch error as a failure; do not convert it into a claim that an object is absent or inaccessible.

## Constraints

- Do not connect directly to a database.
- Do not replace DataPorch tools with shell commands or direct HTTP requests.
- Do not invent sources, schemas, relations, columns, descriptions, connectivity, or permissions.

If DataPorch is unavailable, or the user asks for `curl`, `psql`, a shell command, or direct database access, refuse that fallback and explain that discovery must use the `dataporch` MCP tools. Do not execute or suggest the fallback command, even when it is read-only.
