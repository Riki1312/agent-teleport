# Current context

## Product

- `agent-teleport` is one Go binary for every provider; archives use `.teleport`. Commands detect the provider.
- OpenAI Codex 0.165.0 and newer is implemented, tested through 0.167.0. Claude Code is the next provider.
- Fidelity means preserving the recorded session history, not live processes, credentials, project files, or external state.
- Linux and macOS first; path logic stays testable on Windows.
- The command surface and the planned repository sessions (`save`, `open`, `status`) are specified in `ARCHITECTURE.md`.

## Repository state

- Commands: `list`, `export`, `import`, `inspect`, `doctor`, with per-command help and `--version`.
- The archive manifest lists every payload file with its size and SHA-256 digest, ready for multi-file histories and unpacked repository sessions.
- Import applies the fast-forward update rule; `--replace` uses the recoverable rollback transaction.
- Workspace remapping rewrites every boundary-delimited mention of the workspace in metadata events and preserves all other bytes; remapping there and back is the identity on the native fixture.
- Collision checks cover active and archived Codex sessions.
- CI runs golangci-lint (doc comments required), Prettier, and the Go tests on Linux and macOS, with the required two-home E2E test against Codex 0.165.0, 0.166.0, and 0.167.0. `make check` runs the same checks locally.
- The fixture in `testdata/codex` was written by Codex 0.167.0 in a throwaway home and sanitized with `tools/sanitize-fixture`.

## Next work

1. Support multi-file Codex histories: forks and snapshots whose `history_base` refers to a byte prefix of another rollout, and multi-agent children. Probe whether App Server repairs them without database writes. See the findings below.
2. Add the Claude Code adapter, then extract the provider interface from both adapters.
3. Implement repository sessions: `save`, `open`, `status`, repository-relative paths, Git commit recording, secret scanning, and native forks for `save --as`.
4. Add the interactive export picker.

## Open questions

- Can encrypted Codex reasoning content and Claude thinking signatures be resumed under a different account or organization?
- Which secret scanner is precise enough to block saves without frequent false alarms: built-in patterns, or gitleaks when installed?

## Modern history findings

- Modern paginated rollouts refer to physical file objects, including snapshots with a second UUID in their filename; the object ID can differ from the logical thread ID. Preserve filenames and referenced byte prefixes, because rewriting ancestors changes `end_byte_offset` boundaries.
- `session_meta.id` is the logical thread ID; a multi-agent v2 child's `session_id` can identify its parent's session. Version 1 rejects divergent IDs.
- Native `thread/settings/update` (probed on 0.165.0) can persist a new workspace without changing original rollout prefixes. Shared histories need ancestor hydration before their forks to rebuild item projections.
- Native scan-and-repair does not refresh a cached rollout locator for every shared snapshot. Subagent histories are inspected through `thread/read`, not resumed as running agents.
- Native archiving can move a parent's descendant histories too; verify every imported physical file after archive operations and restarts.
- Validation must disable automatic goals, hooks, plugins, and shell snapshots, or resuming a stored thread can continue an active goal.
- Claude transfers need renamed titles, subagent transcripts, tool results, and file history as well as the main transcript.
- On Codex 0.154.0, resume derived its live sandbox from the configured permission profile and the new workspace, not from recorded workspace paths.
- Codex 0.167.0 `thread/fork` writes a two-line rollout whose `history_base` holds the parent thread ID, an ordinal, and a byte offset into the parent's rollout. Forks need no model call, so `save --as` can use them.
- App Server `thread/list` returns only interactive sources unless `sourceKinds` is given, and Codex names rollout files after local time.
- Rollouts often end with `thread_settings_applied` after `task_complete`. Codex 0.167.0 also keeps `thread_history_1.sqlite`, which is likely the cached rollout locator.
