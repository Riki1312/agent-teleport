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

1. Design and prove multi-file replay support before enabling modern Codex versions.
   Version 1 now rejects non-null `history_base` references to prevent incomplete
   exports; the production compatibility table remains limited to 0.154.0.
2. Add CI coverage with a pinned Codex 0.154.0 installation and require the two-home
   E2E test there.
3. Add the Bubble Tea picker after the non-interactive safety path is mature.

## Open questions

- Which additional Codex versions and rollout layouts pass the repair probe?
- How should supported Codex versions be expanded without overfitting one rollout?

## Modern history findings (2026-11-08)

- A selective workstation migration was completed with separate, version-specific
  scripts. Codex 0.165.0 and 0.167.0 histories were validated through App Server;
  Claude histories were read with the official Claude Agent SDK. These scripts are
  not part of `agent-teleport`, and their success does not expand its version gate.
- Claude Code is a concrete second provider; its production adapter and session
  selection are not implemented yet.
- Modern paginated rollouts refer to physical file objects, including snapshots with
  a second UUID in their filename. The object ID can differ from the logical thread
  ID. Preserve filenames and referenced byte prefixes; rewriting ancestors changes
  `end_byte_offset` boundaries.
- `session_meta.id` is the logical thread ID; a multi-agent v2 child's `session_id`
  can identify its parent's session. Version 1 rejects divergent IDs rather than
  silently exporting a child under its parent's identity.
- A sanitized Codex 0.165.0 probe showed that native `thread/settings/update` can
  persist a new CWD without changing original rollout prefixes. Shared histories
  require ancestor hydration before their forks to rebuild item projections. This
  evidence does not expand the production version gate.
- Native scan-and-repair does not refresh a cached rollout locator for every shared
  snapshot. Disposable migration probes need an exact-schema locator fallback;
  this has not been implemented in the production adapter. Subagent histories are
  inspected through `thread/read`, not resumed as independent running agents.
- Native archiving can move a parent's linked descendant histories too. Track and
  verify all imported physical files after archive operations and restarts.
- Validation must disable automatic goals, hooks, plugins, and shell snapshots:
  resuming a stored thread can otherwise continue an active goal.
- Claude migration needs renamed titles, subagent transcripts, tool results and file
  history as well as the main transcript. Generated titles alone are insufficient
  to decide which sessions contain useful work.
- Keep project files, credentials, fonts, and runtime installation separate from
  session archives. Review local Git state before choosing anything to transfer.
- Publish only the tested production fixes and these general findings. Keep the
  temporary transfer scripts and experimental probe changes outside the release.

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
- `--if-exists replace` uses an fsynced rollback link, App Server deletion and repair,
  and automatic recovery of every pre-commit interruption phase.
- Codex behavior is selected from an exact-version compatibility table covering
  completion, workspace remapping, rollout filenames, and App Server capabilities.
- A credential-free two-home E2E test covers archive fidelity, inspect, piped
  transfer, collisions, replacement recovery, App Server repair, and restart/resume.
- Explicit remapped workspaces now canonicalize symlink ancestors, including missing
  leaf directories. The native 0.154.0 E2E includes a symlinked destination workspace.
