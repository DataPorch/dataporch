# Contributing to DataPorch

Fork the repository at https://github.com/DataPorch/dataporch on GitHub before
making changes. Work from your fork, then open a pull request against the
upstream `main` branch.

## Set up a fork

Clone your fork and keep the upstream repository as a separate remote:

```bash
git clone https://github.com/<your-github-user>/dataporch.git
cd dataporch
git remote add upstream https://github.com/DataPorch/dataporch.git
git fetch upstream
git switch -c <type>/<short-kebab-case-description> upstream/main
```

Use a branch name such as `feat/sqlite-discovery`,
`fix/mcp-origin-check`, `docs/contributing-guide`, or
`test/mysql-read-only`. Do not develop directly on `main`. Before starting
work, check existing issues and documentation so that substantial changes have
one clear outcome and do not duplicate work.

## Project boundaries

DataPorch is an open-source data plane. It owns data access, validation,
bounded execution, connectors, local secrets, MCP/API transports, plugin
metadata, and skills. It does not own an AI application's reasoning,
planning, conversation, or model selection.

Keep the implementation as one Go module and one deployable binary. Preserve
the boundary between transports, execution capabilities, and connector
implementations. Define interfaces in the package that consumes them, wire
dependencies explicitly through constructors, and do not add service
locators, mutable global registries, or dependency-injection frameworks.

New integrations and product scope should fit the open-source data plane.
Managed control-plane features, additional demand-gated connectors, and
unrelated website work need separate scope and review.

## Prerequisites

- Git
- Go 1.25 or later
- Make
- Docker for service-backed integration tests
- `golangci-lint`, for linting
- `govulncheck`, for vulnerability scanning
- GoReleaser, only when validating release artifacts locally

The repository's `go.mod` and CI configuration are the source of truth for
the supported Go toolchain.

## Local verification

Run the normal quality gate before opening a pull request:

```bash
make check
make install-check
```

`make check` verifies formatting and module tidiness, runs `go vet`, runs
the shuffled race-enabled test suite, runs the configured linter, and builds
the binary. The equivalent ordinary test policy is:

```bash
go test -race -shuffle=on ./...
```

Useful focused checks include:

```bash
make test-cgo-disabled
make audit
make test-integration-sqlite
make test-integration-postgres
make test-integration-mysql
make test-integration-lifecycle
```

Integration targets run with the race detector, bypass the test cache, and
use the `integration` build tag. Run only the adapter targets whose
dependencies you have provisioned. PostgreSQL, SQLite, and MySQL integration
coverage is intentionally kept separate.

When a change affects plugin distribution, also validate both plugin
manifests, the skills, the MCP declaration, and the documented install,
update, and removal flows. Record the exact commands and observed results in
the pull request.

## Data safety

Never commit or publish credentials, access tokens, connection strings, DSNs,
secret references, customer data, or raw query results. Redact sensitive
values from terminal output, test logs, issues, and pull requests.

## Code and documentation standards

- Keep changes small, explicit, and within the owning capability.
- Preserve public API and MCP-tool compatibility unless a breaking change is
  intentional and documented.
- Add focused tests for behavior changes. Prefer consumer-owned fakes when a
  dependency can be isolated without weakening the behavior under test.
- Exercise a real external database when the behavior depends on
  database-specific semantics; do not replace that coverage with a generic
  mock.
- Format Go code with `gofmt` and keep module files tidy.
- Add useful documentation for exported Go symbols and update user-facing
  documentation when commands, configuration, security behavior, or
  compatibility changes.
- Avoid unnecessary dependencies, hidden network calls, telemetry, and
  comments that merely restate code.
- Do not commit generated binaries, local runtime state, credentials, or
  unrelated workspace artifacts.

## Commits

Use Conventional Commits for every commit:

```text
<type>(<optional-scope>): <description>
```

Use a standard type such as `feat`, `fix`, `docs`, `refactor`,
`test`, `build`, `ci`, `chore`, `perf`, `style`, or `revert`.
Keep the subject concise and describe the user-visible outcome when
possible. Mark breaking changes with `!` and include a
`BREAKING CHANGE:` footer.

Examples:

```text
feat(sqlite): add bounded discovery queries
fix(mcp): reject invalid Origin headers
docs(security): publish private vulnerability reporting
test(mysql): cover read-only transaction behavior
```

Keep commits focused and reviewable. Do not combine unrelated refactors,
generated artifacts, dependency changes, or formatting churn with a feature.

## Open a pull request

Push your branch to your fork:

```bash
git push --set-upstream origin <type>/<short-kebab-case-description>
```

Open a pull request from that branch to
`DataPorch/dataporch:main`. Use a Conventional Commit title, link the
relevant issue, and include:

- the problem and intended outcome;
- the files or capabilities changed;
- exact verification commands and results;
- compatibility, security, and data-safety considerations;
- documentation or migration notes, when applicable;
- any known limitation or follow-up.

Do not put secrets, tokens, DSNs, connection strings, customer data, or raw
query results in the pull request. Do not claim a security guarantee,
compliance result, customer commitment, or release date without evidence.

Wait for required checks and review before merging. Maintainers decide
whether a change is ready to merge and whether it belongs in a release.

## Releases

Release tags use the form `vMAJOR.MINOR.PATCH`. The tag is the source of
truth for the published runtime version: release artifacts use the numeric
version without the leading `v`, while the CLI reports
`dataporch v<version>`. Untagged or dirty builds report
`dataporch devel`.

Contributors should not create or push release tags. Release preparation,
artifact publication, checksums, provenance, container tags, and release-note
verification are maintainer-controlled steps performed after the relevant
changes have been reviewed and merged.

## License

DataPorch is licensed under the [Apache License 2.0](LICENSE). By submitting
a contribution, you agree that it is provided under the terms of that
license.
