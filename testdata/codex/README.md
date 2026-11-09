`rollout.jsonl` is a sanitized Codex rollout written by Codex 0.167.0: three completed turns, the first of which runs a shell command. It contains no credentials, account identifiers, connected apps, or original conversation text, and its workspace is `/workspace/source`.

Regenerate it only from a throwaway Codex home, sanitize it with `tools/sanitize-fixture`, review the result manually, and verify it against every Codex version in CI with the required end-to-end test:

```sh
make e2e
```
