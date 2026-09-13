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

1. Convert the manual isolated-home transfer into a repeatable E2E test without
   committing session contents.
2. Define and fault-test the collision replacement and rollback protocol.
3. Add the Bubble Tea picker over the working non-interactive export path.

## Open questions

- Which additional Codex versions and rollout layouts pass the repair probe?
- How should supported Codex versions be expanded without overfitting one rollout?

## Repository state

- Architecture and repository conventions are documented.
- The Go module targets Go 1.25 or newer.
- The dependency-free production CLI implements `list`, `export`, `inspect`, `import`,
  and `doctor` for Codex 0.154.0.
- The Codex 0.154.0 spike proves rollout-only App Server repair, read, resume, and
  restart persistence in an isolated target home.
- Workspace remapping must cover authoritative workspace metadata throughout the
  rollout; changing only `session_meta.payload.cwd` is insufficient.
- Export accepts only stable rollouts ending in `task_complete`; active turns fail
  closed.
