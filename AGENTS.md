# agent-teleport

This repository contains `cxport`, a small Go CLI for transferring resumable OpenAI Codex sessions between workstations.

## Working principles

- Work autonomously when the path is clear; ask before making a foundational choice that is not recorded in the architecture or decisions log.
- Prefer the smallest design that is correct, observable, and easy to remove.
- Treat Codex storage as an unstable provider detail. Unknown layouts fail closed.
- Prefer supported Codex App Server behavior over direct SQLite mutation.
- Keep archive and core concepts provider-neutral, but do not generalize beyond the concrete Codex implementation without a second provider.
- Treat session data as sensitive. Never print rollout content or store secrets in tests, fixtures, logs, or `context/`.
- Keep stdout machine-safe. Diagnostics belong on stderr, especially when exporting an archive with `-o -`.
- Treat destructive or irreversible operations as explicit opt-ins.

## Context recovery

- Read `context/current.md` at the start of a run.
- Update `context/current.md` at the end of a run.
- Record durable decisions in `context/decisions.md`; keep it append-only.
- Add `context/sessions/YYYY-MM-DD.md` only for substantial work.
- Keep context notes short, current, and free of secrets.

## Code and structure

- Follow [`CODE_PRINCIPLES.md`](CODE_PRINCIPLES.md).
- Follow [`ARCHITECTURE.md`](ARCHITECTURE.md); record intentional deviations.
- Keep provider-specific paths, schemas, and event shapes behind the Codex adapter.
- Keep the TUI as a presentation layer over plain session values.
- Use the latest stable dependency versions and pin them in `go.mod`.
- Support Linux and macOS first. Keep path logic isolated and testable on Windows.
- Prefer the standard library unless a dependency removes meaningful complexity.
- Use `gofmt`; keep public APIs and comments minimal.

## Verification

Run the checks relevant to the change. Once Go packages exist, normally run:

```sh
go fmt ./...
go test ./...
go vet ./...
git diff --check
```

Use sanitized fixtures only. Container tests must use isolated `CODEX_HOME` and workspace directories. Tests requiring Codex authentication or model calls must be explicit opt-in smoke tests, not ordinary pull-request gates.

## Commits

Use conventional commits: `feat:`, `fix:`, `refactor:`, `test:`, `docs:`, or `chore:`. Keep each commit focused on one logical change.
