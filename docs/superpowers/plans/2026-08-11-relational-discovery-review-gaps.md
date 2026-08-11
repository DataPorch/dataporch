# Relational Discovery Review Gaps Implementation Plan

> **For agentic workers:** REQUIRED SUB-SKILL: Use superpowers:subagent-driven-development (recommended) or superpowers:executing-plans to implement this plan task-by-task. Steps use checkbox (`- [ ]`) syntax for tracking.

**Goal:** Close all seven supplied relational-discovery review findings with backward-compatible PostgreSQL behavior, regression coverage, clean ordinary and integration-tagged quality gates, and focused Conventional Commits.

**Architecture:** Keep metadata discovery in the PostgreSQL connector, exact-name validation and authorization in the execution boundary, and end-to-end traversal at the app/MCP seam. Use catalog SQL that parses on every accepted PostgreSQL release, preserve parameterized identifier handling, and split the integration scenario into focused helpers without changing transport behavior.

**Tech Stack:** Go 1.25+, pgx v5, PostgreSQL 14/18, MCP Go SDK, golangci-lint v2, GitHub Actions.

## Global Constraints

- Work only in `/home/ubuntu/projects/dataporch/build-dir` on `feat/relational-database-discovery`.
- Preserve public API compatibility and the modular hexagonal boundaries in `AGENTS.md` and accepted ADRs.
- Keep SQL values parameterized; catalog SQL must parse on PostgreSQL 14 and retain PostgreSQL 15+ `NULLS NOT DISTINCT` metadata.
- Accept non-empty, valid UTF-8 PostgreSQL identifiers exactly as returned by parent discovery, including control characters.
- Treat only unique integration canaries as forbidden output; application/database names are not secrets.
- Break calls with four or more arguments and lines over approximately 120 characters at semantic boundaries.
- Add no dependencies and preserve unrelated work.
- Each independently reviewable task ends in a Conventional Commit.

---

### Task 1: PostgreSQL Cross-Version Constraint Metadata

**Files:**
- Modify: `internal/connection/postgres/constraint_discovery.go`
- Modify: `internal/connection/postgres/constraint_discovery_test.go`

**Interfaces:**
- Consumes: `listConstraints(parentCtx, queryCtx, pool, relationOID, attnums)` and the existing `execution.Constraint.NullsNotDistinct` pointer field.
- Produces: catalog SQL that parses on PostgreSQL 14 while returning `false` for unique constraints there and the real `pg_index.indnullsnotdistinct` value on PostgreSQL 15+.

- [ ] **Step 1: Add a regression assertion for version-neutral SQL**

  Add a test that asserts `listConstraintsSQL` does not directly reference `constraint_index.indnullsnotdistinct` and does use `to_jsonb(constraint_index) ->> 'indnullsnotdistinct'`. Keep the existing row-scanning fixture as the independent contract for nullable boolean metadata.

- [ ] **Step 2: Run the focused test and verify red**

  Run: `go test -count=1 ./internal/connection/postgres -run 'TestListConstraints'`

  Expected: FAIL because the current query directly references the PostgreSQL 15-only field.

- [ ] **Step 3: Make the catalog query version-neutral**

  Replace the unique-constraint expression with:

  ```sql
  CASE WHEN con.contype = 'u'
    THEN COALESCE(
      (pg_catalog.to_jsonb(constraint_index) ->> 'indnullsnotdistinct')::boolean,
      false
    )
  END
  ```

  This avoids parse-time lookup of the absent PostgreSQL 14 field and preserves the real flag on newer servers.

- [ ] **Step 4: Break the long helper signature and call**

  Format `listConstraints` and its call from `ListColumns` with one parameter or argument per line.

- [ ] **Step 5: Verify and commit**

  Run: `gofmt -w internal/connection/postgres/constraint_discovery.go internal/connection/postgres/constraint_discovery_test.go`

  Run: `go test -count=1 ./internal/connection/postgres`

  Commit: `fix(postgres): support pre-15 constraint discovery`

### Task 2: Generated and Domain Column Metadata

**Files:**
- Modify: `internal/connection/postgres/column_discovery.go`
- Modify: `internal/connection/postgres/column_discovery_test.go`

**Interfaces:**
- Consumes: `Discoverer.ListColumns`, `scanColumn`, and `columnGenerated`.
- Produces: `Generated{Kind: "virtual"}` for `attgenerated = 'v'` and `Nullable: false` for a column whose domain has `pg_type.typnotnull = true`.

- [ ] **Step 1: Add failing virtual-generation coverage**

  Extend `TestTypeCategoryAndColumnMetadataValidation` with `columnGenerated("v", stringPointer("a + b"))` and require kind `virtual` plus expression `a + b`.

- [ ] **Step 2: Add failing domain-nullability query coverage**

  Add a focused SQL contract test requiring the selected nullable expression to account for both `a.attnotnull` and `t.typnotnull` when `t.typtype = 'd'`.

- [ ] **Step 3: Run focused tests and verify red**

  Run: `go test -count=1 ./internal/connection/postgres -run 'Test(TypeCategoryAndColumnMetadataValidation|ListColumnsSQL)'`

  Expected: FAIL for virtual generated metadata and the current `NOT a.attnotnull` expression.

- [ ] **Step 4: Implement minimal metadata changes**

  Select nullability as:

  ```sql
  NOT (
    a.attnotnull
    OR (t.typtype = 'd' AND t.typnotnull)
  )
  ```

  Add the `"v"` switch case returning `&execution.Generated{Kind: "virtual", Expression: value}`.

- [ ] **Step 5: Break long discovery calls and signatures in the modified file**

  Put every parameter/argument of `ListColumns`, `resolveRelation`, `client.pool.Query`, and `listConstraints` on its own line where the call has four or more arguments.

- [ ] **Step 6: Verify and commit**

  Run: `gofmt -w internal/connection/postgres/column_discovery.go internal/connection/postgres/column_discovery_test.go`

  Run: `go test -count=1 ./internal/connection/postgres`

  Commit: `fix(postgres): report virtual and domain metadata`

### Task 3: Round-Trip PostgreSQL Identifiers

**Files:**
- Modify: `internal/execution/discovery_service.go`
- Modify: `internal/execution/discovery_service_test.go`

**Interfaces:**
- Consumes: `Service.ListRelationalTables` and `Service.ListRelationalColumns` exact schema/table names.
- Produces: routing of every non-empty, valid UTF-8 PostgreSQL identifier, including tabs/newlines, without weakening source-ID validation.

- [ ] **Step 1: Replace the rejection test with routing tests**

  Remove the `control schema` case expecting `ErrInvalidRequest`. Add observable service tests that call table discovery with schema `"sales\narchive"` and column discovery with table `"orders\t2026"`, then assert the recording discoverer receives those exact strings.

- [ ] **Step 2: Run focused execution tests and verify red**

  Run: `go test -count=1 ./internal/execution -run 'TestServiceRelational'`

  Expected: FAIL because `validateIdentifier` rejects Unicode control characters.

- [ ] **Step 3: Narrow identifier validation**

  Keep only the empty-string and invalid-UTF-8 checks in `validateIdentifier`; remove the `unicode` import and control-character loop. Connector catalog queries already bind schema/table values as parameters.

- [ ] **Step 4: Wrap long constructor and discovery calls in the modified tests**

  Format `New(Dependencies{...})`, `ListRelationalTables`, and `ListRelationalColumns` calls at semantic boundaries with one argument per line when applicable.

- [ ] **Step 5: Verify and commit**

  Run: `gofmt -w internal/execution/discovery_service.go internal/execution/discovery_service_test.go`

  Run: `go test -count=1 ./internal/execution`

  Commit: `fix(discovery): accept PostgreSQL control identifiers`

### Task 4: Secret-Canary Integration Assertion

**Files:**
- Modify: `internal/app/discovery_integration_test.go`

**Interfaces:**
- Consumes: serialized MCP discovery output and application logs.
- Produces: leak checks for the full admin/reader DSNs and unique generated reader-role/password canaries only.

- [ ] **Step 1: Add a focused helper regression test**

  Add table-driven cases for a new pure `containsSensitiveValue(data []byte, values ...string) bool` helper, proving `"dataporch listening"` is accepted while a generated password, role, or full DSN is rejected.

- [ ] **Step 2: Run the focused test and verify red**

  Run: `go test -count=1 -tags=integration ./internal/app -run 'TestContainsSensitiveValue'`

  Expected: FAIL until the helper exists and callers stop passing generic database/user/port values.

- [ ] **Step 3: Implement the helper and restrict the integration call sites**

  Implement `containsSensitiveValue` and make `assertIntegrationSecretsAbsent` fail only when it returns true. Pass only `dsn`, `readerDSN`, `names.password`, and `names.role` to both leak assertions. Remove the now-unused admin-config parsing and port formatting from the test body.

- [ ] **Step 4: Verify and commit**

  Run: `gofmt -w internal/app/discovery_integration_test.go`

  Run: `go test -count=1 -tags=integration ./internal/app -run 'TestContainsSensitiveValue'`

  Commit: `test(app): check only unique secret canaries`

### Task 5: Integration-Test Structure and Tagged Lint Gate

**Files:**
- Modify: `internal/app/discovery_integration_test.go`
- Modify: `Makefile`
- Modify: `.github/workflows/ci.yml`

**Interfaces:**
- Consumes: the existing app/MCP integration behavior.
- Produces: the same scenario split across focused fixture/startup/schema/table/column/error/security helpers, plus an every-PR integration-tag lint command.

- [ ] **Step 1: Split the 303-line scenario at behavior boundaries**

  Introduce an `integrationHarness` holding `t`, `names`, `admin`, `session`, `runtime`, `logs`, `dsn`, and `readerDSN`. Move startup/import into `newIntegrationHarness`, then move assertions into `assertDataSources`, `listSchemas`, `listTables`, `listColumns`, `assertLiteralSearch`, `assertCompositeColumns`, `assertColumnGrant`, `assertDiscoveryFailures`, and `assertNoSecrets`. Keep `TestDiscoveryImportToMCPPostgresIntegration` as a straight-line orchestrator whose complexity is below 13.

- [ ] **Step 2: Make resource cleanup explicit**

  Replace unchecked deferred closes with cleanup functions that report `response.Body.Close`, `session.Close`, and admin connection close errors through `t.Errorf`. Use `net.ListenConfig.Listen(t.Context(), "tcp", "127.0.0.1:0")` and context-bound `http.NewRequestWithContext` plus `client.Do` in readiness helpers.

- [ ] **Step 3: Simplify metadata assertion complexity**

  Replace the closure-heavy `assertColumnMetadata` loop with a `columnByName` lookup helper and direct assertions for `id`, `amount`, `code`, `state`, `tags`, `created_at`, and `generated_amount`. Keep each helper below cyclomatic complexity 13.

- [ ] **Step 4: Apply configured formatting and fix every tagged finding**

  Run: `golangci-lint run --fix --build-tags=integration ./...`

  Review the diff, then run: `golangci-lint run --build-tags=integration ./...`

  Expected: PASS with zero findings; do not add blanket lint suppressions.

- [ ] **Step 5: Add local and CI tagged-lint entry points**

  Add `lint-integration` to `.PHONY`, implement it as `$(GOLANGCI_LINT) run --build-tags=integration ./...`, list it in `help`, and add a second lint-job step using `golangci/golangci-lint-action@v9` with `args: --timeout=5m --build-tags=integration`.

- [ ] **Step 6: Verify and commit**

  Run: `go test -count=1 ./internal/app`

  Run: `golangci-lint run ./...`

  Run: `golangci-lint run --build-tags=integration ./...`

  Commit: `ci: lint integration-tagged tests`

### Task 6: Real PostgreSQL 14 and 18 Regression Matrix

**Files:**
- Modify: `internal/app/discovery_integration_test.go`
- Modify: `.github/workflows/ci.yml`

**Interfaces:**
- Consumes: PostgreSQL catalog behavior through the full import-to-MCP flow.
- Produces: explicit real-server evidence for PostgreSQL 14 constraint parsing, PostgreSQL 18 virtual generated columns, NOT NULL domains, and control-character identifier traversal.

- [ ] **Step 1: Extend integration names and fixtures**

  Add `controlSchema` and `controlTable` names containing a newline and tab. Create a `customer_code` domain with `NOT NULL`. Detect `server_version_num`: create `generated_amount` as `GENERATED ALWAYS AS (amount * 2) STORED` before PostgreSQL 18 and without `STORED` on PostgreSQL 18 so the default virtual form is exercised.

- [ ] **Step 2: Assert metadata through MCP**

  Require `code.Nullable == false`. Require `generated_amount.Generated.Kind` to be `stored` before 18 and `virtual` on 18. List the control-character schema from the parent endpoint, pass its exact returned name to table discovery, pass the exact returned table name to column discovery, and require its `id` column.

- [ ] **Step 3: Matrix the CI service versions**

  Add `postgres: ["14", "18"]` to the test matrix, set the service image to `postgres:${{ matrix.postgres }}-alpine`, and include the PostgreSQL value in the job name. Keep `fail-fast: false` and `-count=1` integration execution.

- [ ] **Step 4: Run all locally available validation**

  Run: `go test -count=1 ./internal/connection/postgres ./internal/execution ./internal/app`

  If `DATAPORCH_TEST_POSTGRES_DSN` is set, run: `go test -race -count=1 -tags=integration ./internal/connection/postgres ./internal/app`

  If Docker is available, run equivalent integration tests once against PostgreSQL 14 and once against PostgreSQL 18 with unique temporary containers and clean them up afterward.

- [ ] **Step 5: Verify lint and commit**

  Run: `golangci-lint run ./...`

  Run: `golangci-lint run --build-tags=integration ./...`

  Commit: `test(postgres): cover supported discovery versions`

### Task 7: Final Style and Quality Gate

**Files:**
- Modify only files already touched above if final formatting exposes a remaining issue.

**Interfaces:**
- Consumes: all changes from Tasks 1-6.
- Produces: a clean branch with no line over approximately 120 characters in the changed discovery/MCP calls and all required validators passing.

- [ ] **Step 1: Audit long lines in changed Go files**

  Inspect every line over 120 characters in the touched discovery and integration files. Break calls and signatures at argument boundaries; do not split string literals solely to satisfy a column count.

- [ ] **Step 2: Run the complete local gate**

  Run: `make fmt-check`

  Run: `make tidy-check`

  Run: `go vet ./...`

  Run: `go test -race -shuffle=on ./...`

  Run: `golangci-lint run ./...`

  Run: `golangci-lint run --build-tags=integration ./...`

  Run: `go build -trimpath ./cmd/dataporch`

- [ ] **Step 3: Commit any final mechanical cleanup**

  If Step 1 required changes, commit: `style(go): wrap discovery calls`

- [ ] **Step 4: Inspect the final history and worktree**

  Run: `git status --short --branch`

  Run: `git log --oneline --decorate -8`

  Expected: clean worktree, focused Conventional Commits, and all seven review findings mapped to passing evidence.
