# Current context

## Product

- `agent-teleport` is one Go binary for every provider; archives use `.teleport`. Commands detect the provider.
- OpenAI Codex 0.165.0 and newer is implemented, tested through 0.167.0, and Claude Code 2.1.317 and newer, tested through 2.1.319.
- Fidelity means preserving the recorded session history, not live processes, credentials, project files, or external state.
- Linux and macOS first; path logic stays testable on Windows.
- The command surface, including repository sessions (`save`, `open`, `status`), is specified in `ARCHITECTURE.md`.

## Repository state

- Commands: `list`, `export`, `import`, `inspect`, `save`, `open`, `status`, `doctor`, with per-command help and `--version`.
- The archive manifest lists every payload file with its size and SHA-256 digest.
- Import applies the fast-forward update rule. `--replace` uses the recoverable rollback transaction for single-thread Codex sessions and atomic renames for Claude Code sessions; other diverged Codex sessions must be deleted in Codex first.
- Moving a session rewrites every boundary-delimited mention of the workspace in every record and saved tool output and preserves all other bytes; moving there and back is the identity on the native fixtures.
- Collision checks cover active and archived Codex sessions.
- CI runs golangci-lint (doc comments required), Prettier, and the Go tests on Linux and macOS, with the required two-home E2E tests; each job pairs one Codex version (0.165.0 to 0.167.0) with one Claude Code version (2.1.317 to 2.1.319). `make check` runs the same checks locally.
- Whole sessions transfer: sub-agent threads, forks with their ancestor prefixes, and reverts, which import replays through `thread/revert`. Updates append records under Codex's thread writer lock.
- The CLI works through `provider.Adapter` and `provider.Store`; path rewriting, durable file writes, and version ordering are shared packages.
- Repository sessions are unpacked archives with placeholder paths; `save`, `open`, and `status` compare through a dry-run import, and `save` runs the built-in secret scan. Their E2E tests run against both native CLIs with Git clones.
- Claude Code sessions transfer as files: transcript, sub-agent files, and saved tool outputs, with the workspace and project folder moved everywhere. The Claude Code E2E test records sessions against a scripted local Messages API.
- The fixtures in `testdata/codex` are a sanitized Codex 0.167.0 rollout from a throwaway home, a native fork of it, and a sanitized session with a sub-agent and a revert followed by another turn.

## Next work

1. Add the interactive session picker for `export` and `save`.

## Open questions

- Can encrypted Codex reasoning content and Claude thinking signatures be resumed under a different account or organization?

## Modern history findings

- Modern paginated rollouts refer to physical file objects, including snapshots with a second UUID in their filename; the object ID can differ from the logical thread ID. Preserve filenames and referenced byte prefixes, because rewriting ancestors changes `end_byte_offset` boundaries.
- `session_meta.id` is the logical thread ID; a multi-agent v2 child's `session_id` can identify its parent's session. Version 1 rejects divergent IDs.
- Native `thread/settings/update` (probed on 0.165.0) can persist a new workspace without changing original rollout prefixes. Shared histories need ancestor hydration before their forks to rebuild item projections.
- Native scan-and-repair does not refresh a cached rollout locator for every shared snapshot. Subagent histories are inspected through `thread/read`, not resumed as running agents.
- Native archiving can move a parent's descendant histories too; verify every imported physical file after archive operations and restarts.
- Validation must disable automatic goals, hooks, plugins, and shell snapshots, or resuming a stored thread can continue an active goal.
- On Codex 0.154.0, resume derived its live sandbox from the configured permission profile and the new workspace, not from recorded workspace paths.
- Codex 0.167.0 `thread/fork` writes a two-line rollout whose `history_base` holds the parent thread ID, an ordinal, and a byte offset into the parent's rollout. Forks need no model call, so `save --as` can use them.
- App Server `thread/list` returns only interactive sources unless `sourceKinds` is given, and Codex names rollout files after local time.
- Rollouts often end with `thread_settings_applied` after `task_complete`. Codex 0.167.0 also keeps `thread_history_1.sqlite`, which is likely the cached rollout locator.
- On 0.167.0, `thread/read` returns no turns until the thread is loaded, for example by `thread/resume`, which leaves the rollout unchanged. Production import cannot count turns without resuming, which could continue automatic goals in a real home; the E2E test checks turns after resume in its isolated home.
- A fork placed without its ancestor fails `thread/read` with "missing source rollout". With the ancestor present, the fork is not listed until it has turns of its own, and it replays its turns only after its ancestors have been resumed once, in any process. Rewriting ancestors and adjusting byte offsets behaves exactly like unchanged files.
- `thread/turns/list` returns no turns for unloaded threads. Goals, hooks, plugins, shell snapshots, and local automations can be disabled with `--disable` feature flags, present in 0.165.0 through 0.167.0; an unknown flag stops App Server from starting.
- `thread/revert` writes a two-record snapshot `rollout-<time>-<thread>_<object>.jsonl` that references a prefix of the thread's previous object; later turns are appended to it, and a second revert references the first snapshot's object ID. Only the state database's `threads.rollout_path` marks the snapshot as current, so a placed snapshot is ignored in a fresh home, and a scanning `thread/list` reports a reverted thread twice.
- Codex picks up records appended to a rollout when it next loads the thread, and holds an exclusive `flock` on `thread-writer-locks/<thread>.lock` while a process has the thread loaded.
- A sub-agent rollout names its parent's session in `session_id`; the scan rebuilds `thread_spawn_edges`, and a sub-agent can be resumed only through its parent.
- `thread/delete` refuses a thread that forks reference.

## Claude Code findings

- Claude Code resumes `--resume <id>` from a transcript in any project folder and appends to it there; `--continue` and the picker use the project folder of the current directory. Folder names replace every UTF-16 unit other than ASCII letters and digits with `-`; names over 200 characters are cut to 200 and suffixed with `-` and base 36 of the absolute 32-bit Java-style string hash.
- Resuming from another directory makes Claude Code add an `environment` attachment and a system message naming the new and old working directories. The recorded `prompt_snapshot` system prompt is replayed as is, outside `--bare`.
- `api-request`, `api-request-shape`, and `api-request-blob` records are content-addressed request logs; resume does not read them.
- Every record but file-history records carries the session ID; sub-agent records also carry `agentId` and `isSidechain`. `--fork-session` writes a full copy under a new ID. `--resume-session-at <uuid>` creates a rewind branch non-interactively.
- `sessions/<pid>.json` lists running processes with `sessionId` and `status` (`busy` during a turn) and disappears on exit.
- Claude Code 2.1.317 through 2.1.319 write the same record and attachment types and session folder layout.
