# App Server repair spike

This disposable probe tests whether Codex App Server discovers a rollout placed in an
otherwise empty `CODEX_HOME` and persists the reconstructed metadata to its state
database.

It performs this sequence against one running App Server:

1. State-only thread listing before rollout placement.
2. Atomic rollout placement with its session CWD remapped.
3. State-only listing before repair.
4. Normal listing, which permits Codex's documented scan-and-repair behavior.
5. State-only listing after repair.
6. Metadata-only `thread/read` and `thread/resume` probes.
7. App Server restart followed by state-only listing and `thread/read`.

By default the probe remaps workspace-bearing metadata observed in Codex 0.154.0:
`session_meta`, `world_state`, `turn_context`, and `thread_settings_applied`. It does
not rewrite paths embedded in recorded tool results. Pass `-remap session-meta` to
reproduce the original first-line-only hypothesis; Codex then repairs the thread with
its original workspace.

Run it only with a newly created empty target home. The source rollout is read locally
and is never added to this repository.

```sh
go run ./spikes/app-server-repair \
  -rollout /path/to/rollout.jsonl \
  -target-home /tmp/empty-codex-home \
  -target-cwd /tmp/target-workspace
```

The probe does not start a turn or call a model, so it should not require Codex
authentication.

Pass `-allow-existing` with an already imported copy of the same rollout to probe
whether scan-and-repair refreshes an indexed thread after atomic file replacement.
Add `-delete-existing` to clear the existing rollout and index through App Server
before placing the replacement.

## Result

With Codex 0.154.0, scan-and-repair made the placed rollout available to state-only
listing, `thread/read`, and `thread/resume`, including after an App Server restart. No
SQLite mutation or legacy index update was needed. This is evidence for a
version-specific adapter, not a compatibility guarantee for other Codex releases or
rollout layouts.

Replacing an already indexed rollout without deleting its App Server state retained
the old workspace metadata. Calling `thread/delete`, placing the replacement, and
then scanning repaired the new workspace correctly and persisted it across restart.
