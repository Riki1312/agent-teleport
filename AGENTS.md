# agent-teleport

This repository contains `agent-teleport`, a Go CLI that moves resumable coding-agent sessions between workstations and, in planned work, into repositories. OpenAI Codex is the implemented provider; Claude Code is next.

## Working principles

- Work autonomously when the path is clear; ask before making a foundational choice that is not recorded in `ARCHITECTURE.md` or `context/decisions.md`.
- Prefer the smallest design that is correct, observable, and easy to remove. Remove replaced code, tests, and documentation instead of keeping compatibility paths; nothing has been released yet.
- Treat provider storage as an unstable private detail. Unknown layouts fail closed.
- Prefer supported provider interfaces, such as Codex App Server, over writing provider databases.
- Keep archive and CLI concepts provider-neutral. Extract shared provider abstractions from concrete adapters, not in advance.
- Treat session data as sensitive. Never print session content or store it, or secrets, in tests, fixtures, logs, or `context/`.
- Keep stdout machine-safe. Diagnostics belong on stderr, especially when an archive is written to stdout.
- Treat destructive or irreversible operations as explicit opt-ins.

## Context recovery

- Read `context/current.md` at the start of a run and update it at the end.
- Record durable decisions in `context/decisions.md`; keep it append-only and supersede old entries with new ones.
- Add `context/sessions/YYYY-MM-DD.md` only for substantial work.
- Keep context notes short, current, and free of secrets.

## Code and structure

- Follow [`CODE_PRINCIPLES.md`](CODE_PRINCIPLES.md) and [`ARCHITECTURE.md`](ARCHITECTURE.md); record intentional deviations.
- Keep provider paths, schemas, event shapes, and version rules inside the provider adapter.
- Keep the planned TUI a presentation layer over plain session values.
- Prefer the standard library unless a dependency removes meaningful complexity. Pin the latest stable version of every dependency and tool.
- Support Linux and macOS first. Keep path logic isolated and testable on Windows.
- Document every exported identifier; lint enforces it.
- Write Markdown prose without hard line breaks; Prettier enforces it.

## Verification

Run the checks relevant to the change; before committing, normally run:

```sh
make check
```

It runs golangci-lint, the Go tests, the Prettier check, and `git diff --check`. Use `make format` to apply gofmt and Prettier. `make e2e` requires the two-home test against an installed, supported Codex CLI, as CI does; the test otherwise skips.

Use sanitized fixtures only, produced with `tools/sanitize-fixture` and reviewed manually. End-to-end tests use isolated agent homes and workspaces. Tests that need authentication or model calls are explicit opt-in smoke tests, never pull-request gates.

## Commits

Use conventional commits: `feat:`, `fix:`, `refactor:`, `test:`, `docs:`, or `chore:`, with `!` for breaking changes. Keep each commit focused on one logical change.
