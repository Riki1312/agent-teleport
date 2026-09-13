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

1. Turn the spike's event classification into sanitized fixtures and adapter tests.
2. Define the import commit, collision, and rollback protocol with fault injection.
3. Build the smallest production skeleton around App Server discovery and archive
   inspection.

## Open questions

- Which additional Codex versions and rollout layouts pass the repair probe?
- What is the smallest reliable quiescence check for export?

## Repository state

- Architecture and repository conventions are documented.
- The Go module targets Go 1.25 or newer.
- No production packages or dependencies have been added yet.
- The Codex 0.154.0 spike proves rollout-only App Server repair, read, resume, and
  restart persistence in an isolated target home.
- Workspace remapping must cover authoritative workspace metadata throughout the
  rollout; changing only `session_meta.payload.cwd` is insufficient.
