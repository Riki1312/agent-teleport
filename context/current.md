# Current context

## Product

- Project: `agent-teleport`.
- First binary: `cxport` for OpenAI Codex.
- Archive: immutable `.cxport` gzip-compressed tar file using a provider-neutral outer
  manifest and provider-specific payload.
- Fidelity means preserving the complete recorded replay stream, not live processes,
  credentials, project files, or external service state.
- Initial platforms are Linux and macOS; path handling should remain testable for
  future Windows support.

## Settled direction

- Keep commands minimal: `list`, `export`, `import`, `inspect`, and `doctor`.
- Import maps to the current directory by default and fails on an existing session ID
  unless the user explicitly selects `skip` or `replace`.
- The interactive export picker uses the latest stable Bubble Tea v2 and Bubbles v2.
  Non-interactive commands never initialize a TUI.
- Pin latest stable dependencies in `go.mod`; use automated updates rather than
  unpinned builds.
- Prefer Codex App Server discovery and state repair over direct SQLite integration.
  Keep version-specific SQLite mutation only as a fallback proven necessary by tests.
- Use layered tests: unit, sanitized fixture integration, two-home container E2E, and
  opt-in authenticated continuation smoke tests.

## Next work

1. Build an isolated App Server repair spike with separate source and target
   `CODEX_HOME` directories.
2. Determine whether placing a valid rollout followed by `thread/list` repairs all
   state required by `thread/read` and `thread/resume`.
3. Record the observed compatibility boundary and update `ARCHITECTURE.md` before
   building the full archive workflow.

## Open questions

- Which Codex versions and history modes can be imported through App Server repair?
- Which session-related state, if any, cannot be reconstructed from the rollout?
- What is the smallest reliable quiescence check for export?

## Repository state

- Architecture and repository conventions are documented.
- The Go module targets Go 1.25 or newer.
- No production packages or dependencies have been added yet.
