These sanitized Codex rollouts keep their native file names, which Codex derives from the local creation time and thread ID:

- `rollout-2026-11-09T01-37-58-01a1bdbd-….jsonl` was written by Codex 0.167.0: three completed turns, the first of which runs a shell command. It contains no credentials, account identifiers, connected apps, or original conversation text, and its workspace is `/workspace/source`.
- `rollout-2026-11-09T13-05-32-01a1c033-….jsonl` is a fork of it created natively by Codex 0.167.0 `thread/fork`, without a model call. Its `history_base` references the whole first rollout.

Regenerate them only from a throwaway Codex home: sanitize the first with `tools/sanitize-fixture`, create the fork from the unchanged sanitized rollout, review both manually, and verify them against every Codex version in CI with the required end-to-end test:

```sh
make e2e
```
