# DataPorch Security Policy

## Report a vulnerability privately

Send suspected security vulnerabilities to
[adam@dataporch.dev](mailto:adam@dataporch.dev). Do not open a public GitHub
issue, pull request, discussion, or forum post for a suspected vulnerability.

Use a subject such as:

```text
[Security] <short description>
```

This policy covers the open-source DataPorch runtime, connectors, local
secret handling, MCP/API transports, agent plugins, release artifacts, and
supporting build or deployment configuration in this repository. If you are
unsure whether a report is in scope, use the same private address.

## What to include

Include enough information to reproduce and assess the issue without
exposing anyone's data:

- a concise description of the behavior and its security impact;
- the affected release tag, commit, or component;
- operating system, architecture, Go version, and relevant client versions;
- minimal reproduction steps or a safe proof of concept;
- expected behavior and observed behavior;
- sanitized logs or configuration excerpts;
- whether the issue affects confidentiality, integrity, or availability.

Please redact or omit credentials, access tokens, connection strings, DSNs,
secret references, customer data, and raw query results. Do not attach a
database dump or an unredacted log.

## Safe reproduction boundaries

Test only with systems and data that you own or are explicitly authorized to
use. Prefer the checked-in SQLite fixture or a disposable, isolated service.
Never test a suspected issue against a production, customer, shared, or
unprovisioned database.

If a secret is accidentally exposed, stop using it, avoid copying it into
the report, and ask the owner to revoke or rotate it. State only the minimum
facts needed to identify the exposure.

## Examples of security reports

Examples include:

- bypassing MCP authentication or Origin validation;
- exposing credentials, DSNs, tokens, or secret references to an agent;
- bypassing read-only or bounded-execution controls;
- escaping local path or file-permission protections;
- leaking sensitive values through logs, plugin files, artifacts, or errors;
- a dependency or release-process issue that permits tampering or unsafe
  distribution.

Do not use this process for ordinary bugs that do not create a security risk.
Report those through the project's normal issue and pull-request workflow,
without including sensitive data.

## Coordinated disclosure

Keep vulnerability details private while the report is being evaluated and
remediated. Coordinate any public disclosure, affected-version guidance, and
release communication through the private email thread. Please do not publish
a proof of concept or exploit details before that coordination is complete.

The project may request additional reproduction details, affected versions,
or a safer proof of concept. Keep follow-up material sanitized and continue to
avoid live credentials and user data.
