# Decisions

This file records the current durable product, architecture, and workflow decisions. When a decision changes, edit or replace its entry; Git keeps the history.

Each entry uses this shape:

- `decision`: What was decided.
- `rationale`: Why it was selected.
- `impact`: What follows from it.

## Product

### One binary for every provider

- `decision`: Ship one `agent-teleport` binary. Archives use the `.teleport` extension. Commands detect the provider from the session or archive; provider identifiers are `openai-codex` and `anthropic-claude`.
- `rationale`: One command vocabulary is simpler than a binary per provider.
- `impact`: `list` and `export` consult every installed provider, skipping missing ones silently and reporting broken ones on stderr; `import` and `inspect` use the provider named in the manifest.

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
- `impact`: Repeated imports are safe. Each adapter applies the rule per thread or file.

### Keep sessions in repositories as unpacked archives

- `decision`: Planned `save`, `open`, and `status` commands keep sessions under `.agent-teleport/sessions/<name>/` with the same manifest as a packed archive. Saved paths are relative to the repository root, each save records the Git commit and branch, and saving runs a secret scan that refuses findings unless explicitly accepted. There is no read-only rendering fallback.
- `rationale`: Append-only JSONL produces small Git diffs, repository-relative paths avoid leaking machine paths, and committing a session publishes it to everyone with repository access.
- `impact`: The manifest lists payload files, and workspace path rewriting must be complete and reversible.

## Providers

### Support recent provider versions only

- `decision`: Map each rollout layout to a range of provider versions, starting at a minimum version and tested through the newest release verified by a sanitized fixture and the two-home test in CI. Older versions fail closed; newer versions are accepted with a `doctor` warning while structural validation still rejects unknown layouts. Drop older layouts, their fixtures, and their code once newer support lands. Unreleased formats change without migrations.
- `rationale`: Codex releases every few days; exact-version gating would break with every release, while structural validation and the CI matrix keep support honest.
- `impact`: Codex 0.165.0 and newer are supported, tested through 0.167.0, and Claude Code 2.1.317 and newer, tested through 2.1.319. CI pairs one version of each provider per job.

### Let the provider repair its own state

- `decision`: Place session files, then let the provider rebuild and verify its state through a supported interface, such as Codex App Server scan-and-repair, `thread/read`, and `thread/delete`. Never write provider databases. A provider without such an interface, like Claude Code, is changed only through the session files it reads.
- `rationale`: Providers own their evolving schemas; a rollout-only import persisted and resumed correctly across App Server restarts, and Claude Code resumes any transcript it finds.
- `impact`: Codex imports are verified through the provider's own listing, state, and read results; Claude Code imports by reading the placed files back, and by the two-home test.

### Export only completed turns

- `decision`: Export requires an idle or unloaded thread, a stable read, and a rollout in which every started turn has completed or been aborted.
- `rationale`: App Server reports sessions owned by another CLI process as unloaded, so runtime status alone cannot prove a turn is complete.
- `impact`: Archives always end at a resumable turn boundary.

### Transfer whole Codex sessions

- `decision`: A Codex session archive holds every rollout object of the root thread and of the sub-agent threads that share its session ID, plus the referenced byte prefix of every ancestor they fork from, ordered so that referenced objects come first and named as Codex names them. On import, rewrite the workspace in every file and move every `history_base` reference to the same record boundary. Replay reverts through App Server `thread/revert` and append the snapshot's later records, instead of placing snapshot files. Reuse local ancestors outside the session only when they contain the referenced prefix, and never modify them. Verify by loading the session in order in an App Server with goals, hooks, plugins, shell snapshots, and local automations disabled.
- `rationale`: Codex records which snapshot is a thread's current history only in its own database, which only a native revert updates; it builds a fork's turns only after loading its ancestors, loads sub-agents through their parent, and refuses to delete referenced threads.
- `impact`: Forked, reverted, and multi-agent sessions transfer, with a replayed snapshot header that differs from the archived one in its object ID and timestamps. A fork cannot move to another workspace while its ancestor exists locally in the old one.

### Append updates under Codex's writer lock

- `decision`: Update a thread that the archive extends by appending the new records, while holding the exclusive `flock` that Codex keeps on `thread-writer-locks/<thread>.lock` for loaded threads. Record the original and final sizes in a marker until the append is durable. Refuse to append to, replace, or replay a thread that a Codex process has loaded.
- `rationale`: Codex picks up appended records when it next loads a thread, and its writer lock is the only cross-process signal that a session is open; App Server status cannot see other processes.
- `impact`: Updates no longer delete threads, so threads that forks reference can be updated. The listing's update time stays at its old value until Codex itself writes to the thread.

### Transfer whole Claude Code sessions as files

- `decision`: A Claude Code session archive holds the transcript and the known files of its session folder, sub-agent transcripts and metadata and saved tool outputs, at paths relative to the project folder; unknown files fail closed, and file history backups are not exported. Import places them in the project folder named after the target workspace, replacing each file through a stage file and a rename with the transcript last. A copy in another project folder is moved only with `--replace`.
- `rationale`: Claude Code has no index: it resumes a transcript found in any project folder, keeps rewinds as branches inside the transcript, and writes native forks as self-contained sessions. Its project folder name is derived from the workspace path, so `--continue` and the session picker find only transcripts in the right folder.
- `impact`: Rewinds, forks, and sub-agents need no special handling. Imports reproduce Claude Code's folder naming, including the hash of long paths.

### Move Claude Code environment paths

- `decision`: On import, move the source workspace and the source project folder, recorded through the manifest's `home`, to the target's. Rewrite each record's `cwd` and every boundary-delimited mention in `environment`, `instructions`, and `prompt_snapshot` attachments, reversibly; leave messages, tool calls, tool results, and request logs as recorded.
- `rationale`: Claude Code replays the recorded system prompt and environment when it resumes, and without the rewrite the model would see the old workspace and memory folder, with only a native note that the directory moved.
- `impact`: Resumed requests name only target paths in their environment, and moving a session back restores its original bytes. Saved tool outputs keep their source paths in tool results.

### Coordinate with Claude Code through its session registry

- `decision`: Refuse to export a session that a live process in Claude Code's `sessions/<pid>.json` registry reports as anything but idle, and refuse to change a session that a live process has open.
- `rationale`: Claude Code takes no lock on transcripts; the registry is the only cross-process signal of an open session.
- `impact`: The check is not atomic, so a Claude Code process that starts during an import is not detected. Platforms without a process check treat every registered process as alive.

### Replace through a recoverable rollback link

- `decision`: Replace a diverged single-thread Codex session by hard-linking its rollout as a rollback copy, deleting the thread through App Server, placing and repairing the new rollout, then removing the rollback link as the commit point.
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

- `decision`: Use unit tests and a two-home end-to-end test against each native provider CLI, required in CI. Codex sessions come from sanitized fixtures; Claude Code sessions are recorded during the test against a scripted local Messages API through `ANTHROPIC_BASE_URL`, which also captures what a resumed session sends. Tests that need authentication or model calls are opt-in smoke tests.
- `rationale`: Pull-request gates must be deterministic and credential-free; the native CLI is the black-box oracle for storage changes.
- `impact`: Every supported version passes the two-home test of its provider.

### Pin dependencies and tools

- `decision`: Prefer the standard library. Pin the latest stable version of every dependency and tool, update them automatically, and use Bubble Tea v2 for the planned picker.
- `rationale`: Pinned versions keep builds reproducible; automated updates keep them current.
- `impact`: The module requires Go 1.25 or newer; Dependabot updates Go modules and GitHub Actions.

### Extract shared code from both adapters

- `decision`: Keep provider-neutral values and adapter descriptions in `internal/provider`, and shared mechanisms in `internal/pathmap`, `internal/fsutil`, and `internal/semver`, each extracted once both adapters needed it.
- `rationale`: Abstractions drawn from two concrete adapters fit both without speculative extension points.
- `impact`: The CLI imports no adapter-specific types; a new provider implements `provider.Store` and registers a `provider.Adapter`.

### Enforce doc comments and Markdown formatting

- `decision`: Require doc comments on exported Go identifiers through golangci-lint, and format Markdown with a pinned Prettier through `npx`, with prose left unwrapped.
- `rationale`: Doc comments keep package boundaries explicit; unwrapped prose produces clean diffs.
- `impact`: `make check` and CI run lint, tests, and formatting checks. Node is a development-only requirement.
