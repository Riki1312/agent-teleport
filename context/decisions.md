# Decisions log

This append-only file records durable product, architecture, and workflow decisions.
Chronological implementation detail belongs in `sessions/`.

## Entry template

### YYYY-MM-DD - Short title

- `decision`: What was decided.
- `rationale`: Why it was selected.
- `impact`: What changes because of it.
- `evidence`: Relevant files, commands, or commits.

### 2026-09-13 - Establish product and provider naming

- `decision`: Use `agent-teleport` for the project, `cxport` for the Codex binary,
  `.cxport` for archives, and `openai-codex` for the provider identifier.
- `rationale`: The project and format leave room for other agents while the binary has
  a concise Codex-specific identity.
- `impact`: A future Claude implementation can use `clport` and its own provider
  payload without changing the outer archive contract.
- `evidence`: `ARCHITECTURE.md`, `README.md`.

### 2026-09-13 - Preserve recorded history with honest fidelity

- `decision`: Define lossless transfer as preserving the complete recorded provider
  event stream. Do not claim to transfer live tools, processes, credentials, project
  files, or external state.
- `rationale`: Recorded tool calls can be replayed as history, but the source runtime
  environment cannot be made identical by copying session data.
- `impact`: Archives preserve source rollout bytes and document every transformation.
- `evidence`: `ARCHITECTURE.md`.

### 2026-09-13 - Keep session identity and collision behavior predictable

- `decision`: Transfer one immutable session identity per archive. Import defaults to
  collision error and supports only explicit `skip` or `replace`; version 1 does not
  rename or clone sessions.
- `rationale`: Rewriting identifiers can leave inconsistent references and makes
  collision behavior harder to reason about.
- `impact`: Existing sessions are never modified without an explicit replace request.
- `evidence`: `ARCHITECTURE.md`.

### 2026-09-13 - Prefer Codex-managed state repair

- `decision`: Test whether Codex App Server can discover an imported rollout and
  repair its own state before implementing direct SQLite writes.
- `rationale`: Codex owns its evolving storage schema, while App Server exposes
  supported thread listing, reading, status, and resume behavior.
- `impact`: Direct SQLite integration remains a version-specific fallback rather than
  the default architecture.
- `evidence`: `ARCHITECTURE.md`, `context/current.md`.

### 2026-09-13 - Use latest stable pinned dependencies

- `decision`: Adopt the latest stable library versions at implementation time and pin
  exact module versions. Use Bubble Tea v2 and Bubbles v2 for the interactive picker.
- `rationale`: Pinning makes builds reproducible while routine update automation keeps
  dependencies current.
- `impact`: The module requires Go 1.25 or newer. TUI dependencies stay out of the
  non-interactive domain model.
- `evidence`: `go.mod`, `AGENTS.md`, `context/current.md`.

### 2026-09-13 - Test from deterministic core to live continuation

- `decision`: Use unit tests, sanitized fixture integration tests, isolated two-home
  container tests, and opt-in authenticated model-continuation smoke tests.
- `rationale`: Storage migration should be deterministic and credential-free in pull
  requests; live continuation is valuable but networked, costly, and nondeterministic.
- `impact`: Container tests use Codex App Server as the black-box oracle and keep live
  model calls outside the ordinary PR gate.
- `evidence`: `AGENTS.md`, `context/current.md`.

### 2026-09-13 - Support Linux and macOS first

- `decision`: Ship Linux and macOS support first while isolating and testing path
  behavior needed for future Windows support.
- `rationale`: Codex storage and path remapping need real platform validation; limiting
  the first support claim keeps the compatibility surface honest.
- `impact`: Windows-specific behavior is not a version 1 release requirement.
- `evidence`: `README.md`, `ARCHITECTURE.md`, `context/current.md`.

### 2026-09-13 - Use App Server repair for Codex 0.154.0

- `decision`: Import the rollout atomically, ask App Server to scan and repair its
  owned state, then verify the result through state-only list, read, and resume. Do
  not mutate SQLite or legacy indexes for Codex 0.154.0.
- `rationale`: The isolated spike persisted a usable remapped thread across an App
  Server restart using only the rollout. First-line-only CWD replacement failed;
  format-specific workspace metadata throughout the rollout is authoritative.
- `impact`: The initial adapter stays rollout-only and version-gated. Workspace
  remapping covers known metadata events but does not rewrite recorded tool results.
- `evidence`: `spikes/app-server-repair`, `ARCHITECTURE.md`.

### 2026-09-13 - Export only completed-turn snapshots

- `decision`: For Codex 0.154.0, require a stable rollout read whose final event is
  `event_msg/task_complete`.
- `rationale`: A separate App Server reports sessions owned by another CLI process as
  unloaded, so runtime status alone cannot prove that a turn is complete.
- `impact`: Export fails closed during an active turn. The archive always ends at a
  replayable turn boundary, even when the source CLI remains open.
- `evidence`: `internal/codex`, `internal/codex/codex_test.go`.

### 2026-09-13 - Replace through a recoverable rollback link

- `decision`: Replace a colliding Codex 0.154.0 session by hard-linking its rollout as
  a rollback copy, deleting the old thread through App Server, placing and repairing
  the new rollout, then removing the rollback link as the commit point.
- `rationale`: App Server does not refresh an indexed thread when only its file is
  overwritten. The rollback link preserves the old rollout across deletion without
  copying or mutating its bytes.
- `impact`: Any pre-commit interruption is automatically rolled back on the next
  import. Dry-run detects recovery markers but never performs recovery or starts App
  Server.
- `evidence`: `internal/codex/replace.go`, `internal/codex/replace_test.go`,
  `spikes/app-server-repair`.

### 2026-09-13 - Gate Codex behavior through exact-version fixtures

- `decision`: Select completion, workspace remapping, rollout filename, and App
  Server capabilities from an exact Codex version table. Validate each entry with a
  sanitized, credential-free rollout fixture and two-home black-box test.
- `rationale`: Codex storage is private and evolving; explicit fingerprints make
  support observable and prevent optimistic handling of unknown layouts.
- `impact`: Unknown versions fail closed. Adding support requires both a table entry
  and a fixture proven against that exact Codex CLI.
- `evidence`: `internal/codex/compatibility.go`, `test/e2e/transfer_test.go`,
  `testdata/codex`.
