# Decisions

This file records the current durable product, architecture, and workflow decisions. When a decision changes, edit or replace its entry; Git keeps the history.

Each entry uses this shape:

- `decision`: What was decided.
- `rationale`: Why it was selected.
- `impact`: What follows from it.

## Product

### One binary for every provider

- `decision`: Ship one `agent-teleport` binary. Archives use the `.teleport` extension. Commands detect the provider from the session or archive; provider identifiers are `openai-codex` and, next, `anthropic-claude`.
- `rationale`: One command vocabulary is simpler than a binary per provider.
- `impact`: `list` shows sessions from every installed provider.

### Preserve recorded history with honest fidelity

- `decision`: A transfer preserves the complete recorded provider event stream, apart from documented workspace path rewrites. It does not transfer live tools, processes, credentials, project files, or external state.
- `rationale`: Recorded tool calls replay as history, but copying session data cannot reproduce the source environment.
- `impact`: Archives store provider files unchanged, and every transformation is documented in `ARCHITECTURE.md`.

### Keep one immutable session identity

- `decision`: An archive holds one session with one identity. Sessions are never renamed or cloned, except through a provider's native fork.
- `rationale`: Rewriting identifiers can leave inconsistent references inside provider state.
- `impact`: A diverged session is either replaced explicitly or saved as a native fork.

### Apply one fast-forward rule to every session update

- `decision`: When a session already exists, compare the two copies. Identical copies succeed without changes, a longer incoming copy updates the existing one, and a longer local copy is kept. Diverged copies, or copies mapped to another workspace, fail unless the user passes `--replace` (`import`, `open`) or saves a native fork with `--as` (`save`).
- `rationale`: Provider transcripts only grow at the end, so prefix comparison identifies safe updates without a collision policy.
- `impact`: Repeated imports are safe, and updates use the recoverable replacement transaction.

### Keep sessions in repositories as unpacked archives

- `decision`: Planned `save`, `open`, and `status` commands keep sessions under `.agent-teleport/sessions/<name>/` with the same manifest as a packed archive. Saved paths are relative to the repository root, each save records the Git commit and branch, and saving runs a secret scan that refuses findings unless explicitly accepted. There is no read-only rendering fallback.
- `rationale`: Append-only JSONL produces small Git diffs, repository-relative paths avoid leaking machine paths, and committing a session publishes it to everyone with repository access.
- `impact`: The manifest lists payload files, and workspace path rewriting must be complete and reversible.

## Providers

### Support recent provider versions only

- `decision`: Map each rollout layout to a range of provider versions, starting at a minimum version and tested through the newest release verified by a sanitized fixture and the two-home test in CI. Older versions fail closed; newer versions are accepted with a `doctor` warning while structural validation still rejects unknown layouts. Drop older layouts, their fixtures, and their code once newer support lands. Unreleased formats change without migrations.
- `rationale`: Codex releases every few days; exact-version gating would break with every release, while structural validation and the CI matrix keep support honest.
- `impact`: Codex 0.165.0 and newer are supported, tested through 0.167.0; Codex 0.154.0 support is removed.

### Let the provider repair its own state

- `decision`: Place session files, then let the provider rebuild and verify its state through a supported interface, such as Codex App Server scan-and-repair, `thread/read`, and `thread/delete`. Never write provider databases.
- `rationale`: Providers own their evolving schemas; a rollout-only import persisted and resumed correctly across App Server restarts.
- `impact`: Import is verified through the provider's own listing, state, and read results.

### Export only completed turns

- `decision`: Export requires an idle or unloaded thread, a stable read, and a rollout in which every started turn has completed or been aborted.
- `rationale`: App Server reports sessions owned by another CLI process as unloaded, so runtime status alone cannot prove a turn is complete.
- `impact`: Archives always end at a resumable turn boundary.

### Reject histories that one payload cannot preserve

- `decision`: Reject Codex rollouts with a non-null `history_base` or with divergent thread and session IDs until multi-file histories are implemented and tested.
- `rationale`: Paginated histories and multi-agent children depend on other rollout files; a single file would appear valid while omitting most of the conversation.
- `impact`: Modern Codex support starts with multi-file payloads.

### Replace through a recoverable rollback link

- `decision`: Update or replace a session by hard-linking its rollout as a rollback copy, deleting the thread through App Server, placing and repairing the new rollout, then removing the rollback link as the commit point.
- `rationale`: App Server does not refresh an indexed thread when its file is overwritten; the link preserves the old rollout without copying it.
- `impact`: Any interruption before the commit point is rolled back on the next import. Dry runs detect recovery markers but never recover.

### Detect existing Codex sessions from rollout files

- `decision`: Decide whether a session exists by searching the active and archived Codex session stores for its rollout and recovery files, in imports and dry runs alike. Update an existing rollout where it is, and name new rollouts after their local creation time, as Codex does.
- `rationale`: App Server `thread/list` omits archived threads, and relying on it once duplicated an archived session and deleted it during cleanup. Rollout names carry local time, so a computed path cannot identify a natively created rollout.
- `impact`: Import never updates archived or moved copies; the user restores or deletes them in Codex first.

### Remap workspace paths string by string

- `decision`: In workspace metadata events, rewrite every mention of the source workspace at path boundaries inside JSON strings, keep all other bytes, and fail unless rewriting back restores the original bytes. Canonicalize target workspaces, resolving symlinks in existing ancestors.
- `rationale`: Codex records the workspace in sandbox entries, workspace roots, `file://` URIs, and generated text as well as `cwd` keys, and native Codex canonicalizes paths such as `/var` and `/private/var` on macOS.
- `impact`: Remapping is reversible, which repository-relative sessions rely on.

## Engineering

### Support Linux and macOS first

- `decision`: Ship Linux and macOS support first, with path logic isolated and testable for Windows.
- `rationale`: Storage and path behavior need real platform validation before a support claim.
- `impact`: CI runs on Linux and macOS.

### Test from deterministic core to native providers

- `decision`: Use unit tests, sanitized fixtures, and a two-home end-to-end test against the native provider CLI, required in CI. Tests that need authentication or model calls are opt-in smoke tests.
- `rationale`: Pull-request gates must be deterministic and credential-free; the native CLI is the black-box oracle for storage changes.
- `impact`: Every supported version has a fixture that passes the two-home test.

### Pin dependencies and tools

- `decision`: Prefer the standard library. Pin the latest stable version of every dependency and tool, update them automatically, and use Bubble Tea v2 for the planned picker.
- `rationale`: Pinned versions keep builds reproducible; automated updates keep them current.
- `impact`: The module requires Go 1.25 or newer; Dependabot updates Go modules and GitHub Actions.

### Enforce doc comments and Markdown formatting

- `decision`: Require doc comments on exported Go identifiers through golangci-lint, and format Markdown with a pinned Prettier through `npx`, with prose left unwrapped.
- `rationale`: Doc comments keep package boundaries explicit; unwrapped prose produces clean diffs.
- `impact`: `make check` and CI run lint, tests, and formatting checks. Node is a development-only requirement.
