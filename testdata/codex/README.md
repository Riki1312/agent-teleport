These sanitized Codex rollouts keep their native file names, which Codex derives from the local creation time and thread ID:

- `rollout-2026-11-09T01-37-58-01a1bdbd-….jsonl` was written by Codex 0.167.0: three completed turns, the first of which runs a shell command. It contains no credentials, account identifiers, connected apps, or original conversation text, and its workspace is `/workspace/source`.
- `rollout-2026-11-09T13-05-32-01a1c033-….jsonl` is a fork of it created natively by Codex 0.167.0 `thread/fork`, without a model call. Its `history_base` references the whole first rollout.
- `multi-agent/` is one session written by Codex 0.167.0: a root thread whose first turn spawned a sub-agent, two more turns, a native revert of the third turn, and a fourth turn recorded after the revert. It holds the root's original rollout, its revert snapshot, and the sub-agent's rollout, sanitized together so their `history_base` offsets stay consistent.

Regenerate them only from a throwaway Codex home: sanitize rollouts with `tools/sanitize-fixture`, listing referenced rollouts before the ones that reference them; create the fork from the unchanged sanitized rollout; review every file manually; and verify them against every Codex version in CI with the required end-to-end test:

```sh
make e2e
```
