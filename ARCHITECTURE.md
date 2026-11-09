# Architecture

`agent-teleport` moves the recorded history of a local coding-agent session to another workstation, or into a repository, so the session can be resumed with the agent's own CLI. This document describes the current design and the planned work it is built for. Durable decisions and their rationale live in [`context/decisions.md`](context/decisions.md).

## Scope

The CLI is a single CGO-free Go binary, `agent-teleport`, for Linux and macOS. OpenAI Codex is the implemented provider; Claude Code is the next one. Commands detect the provider from the session or archive, so users never name it.

Goals:

- Preserve the complete recorded session history byte for byte, apart from documented workspace path rewrites.
- Map an imported session to a different local workspace.
- Make every command safe, inspectable, scriptable, and predictable.
- Fail without changing local state when a storage layout is unknown.

Non-goals:

- Transferring live processes, shell state, credentials, project files, uncommitted changes, or external service state.
- Making models, MCP servers, plugins, or skills available on the destination machine.
- Exporting a session while its agent is writing a turn.
- Merging concurrent edits of one session made on different machines.
- Renaming or cloning session identifiers, except through a provider's native fork.

## Fidelity model

A transfer is lossless with respect to the recorded provider event stream: an archive stores the provider's files unchanged. Importing into another workspace rewrites workspace paths in the metadata events that the provider reads when it rebuilds session state, and nothing else. Recorded tool calls and their results stay as recorded, so a resumed session may refer to paths, tools, or permissions that differ on the destination machine.

## Commands

```text
agent-teleport list    [--all] [--json]
agent-teleport export  (SESSION | --last) [--all] [-o PATH|-]
agent-teleport import  (ARCHIVE | -) [--cwd PATH] [--replace] [--dry-run] [--resume]
agent-teleport inspect (ARCHIVE | -) [--json]
agent-teleport doctor  [--json]
```

- `list` shows the sessions of the current directory, or of every directory with `--all`, newest first. `--json` is the stable automation interface.
- `export` accepts a session ID, a unique ID prefix, or an exact title. Without `-o` it writes `<title>-<id>.teleport` to the current directory; `-o -` writes the archive to stdout and every diagnostic to stderr.
- `import` maps the session to the current directory unless `--cwd` names another one. It prints the command that resumes the session, and runs it with `--resume`.
- `inspect` validates an archive, describes its session, and reports whether it can be imported into the current directory. It never changes local state.
- `doctor` reports the detected agents, their versions, and whether they are supported.

Usage errors exit with status 2 and other failures with status 1. An interactive session picker for `export` is planned; it will receive plain session values and never run when stdout is redirected or JSON is requested.

Direct transfer works over any pipe:

```sh
agent-teleport export --last -o - | ssh workstation agent-teleport import -
```

### Updating an existing session

Provider transcripts only grow at the end, so two copies of one session either match, one extends the other, or they have diverged. Every command that writes a session applies the same rule:

| Local and incoming copies | Result |
| --- | --- |
| Identical | Nothing changes; the command succeeds. |
| Incoming extends local | The local session is updated to the incoming copy. |
| Local extends incoming | The local session is kept; the command succeeds. |
| Diverged, or mapped to another workspace | The command fails unless `--replace` is given. |

Updates and replacements use the recoverable replacement transaction described below.

## Archive format

A packed archive is a gzip-compressed tar file with the `.teleport` extension. Its first entry is `manifest.json`, followed by exactly the payload files the manifest lists, in order:

```json
{
  "format": "agent-teleport",
  "format_version": 1,
  "provider": "openai-codex",
  "provider_version": "0.167.0",
  "session_id": "01a09a34-754a-7343-8d40-37a2af117f2f",
  "workspace": "/home/alice/src/example",
  "created_at": "2026-11-09T09:58:44Z",
  "files": [
    {
      "path": "rollout-2026-11-09T01-37-58-01a1bdbd-….jsonl",
      "size": 39742,
      "sha256": "…"
    },
    {
      "path": "rollout-2026-11-09T13-05-32-01a1c033-….jsonl",
      "size": 1580,
      "sha256": "…"
    }
  ]
}
```

- Payload paths are clean, relative, and slash-separated; the provider chooses them.
- Decoding rejects unknown manifest fields, unsupported format versions, unlisted or missing entries, links and other non-regular files, size limit violations, and digest mismatches.
- Digests detect accidental corruption. They are not signatures and do not establish who created an archive.
- Archives are immutable. Export verifies the encoded archive before writing it, and creates the output file exclusively.

The format is unreleased, so it changes in place without migrations until the first stable release.

## Components

```text
cmd/agent-teleport        process entry point
internal/cli              argument parsing, session selection, output
internal/archive          provider-neutral manifest, packing, validation
internal/codex            Codex adapter: discovery, export, import, repair
internal/codex/appserver  JSON-RPC client for codex app-server --stdio
internal/buildinfo        build version
tools/sanitize-fixture    produces sanitized Codex fixtures
```

The CLI operates on archives and plain session values. Provider paths, event shapes, version rules, and state repair stay inside the provider adapter. A provider interface will be extracted from the Codex and Claude Code adapters once both exist, rather than designed in advance.

## Codex adapter

Codex storage is a private implementation detail. The adapter prefers Codex App Server over direct database access and never writes Codex databases.

### Export

1. Discover sessions through App Server `thread/list`, asking for every user-started source explicitly: App Server lists only interactive sources by default, which hides `codex exec` sessions.
2. Require an idle or unloaded thread. App Server cannot see turns running in another Codex process, so every turn started in the rollout must also have ended, by completing or being aborted.
3. Read the rollout and fail if its size or modification time changed during the read.
4. Follow the history. A forked rollout starts with a `history_base` that references a byte prefix of its parent's rollout, which may itself be a fork. Read exactly that prefix of each ancestor, root first.
5. Validate every file and the history as a whole: consistent identities and references, and every started turn ended. Reject reverted threads, whose current history is a snapshot file that Codex locates through its own database, and multi-agent child threads, whose session ID names their parent.
6. Build the archive in memory with the files root first under their native names, verify it, and write it.

### Import

1. Decode and validate the whole archive in memory.
2. Require the archive's Codex version and the installed version to share a rollout layout.
3. Canonicalize the target workspace, resolving symlinks in its existing ancestors.
4. Rewrite workspace paths in every file: every mention of the session's workspace, at path boundaries, inside the JSON strings of workspace metadata events. This covers plain paths, paths below the workspace, `file://` URIs, and paths embedded in Codex-generated text. Other bytes are preserved, and the import fails unless rewriting back would restore the original bytes. Rewriting an ancestor changes its length, so each descendant's `history_base` byte offset is set to the rewritten ancestor's length.
5. Place missing ancestors. An ancestor that exists locally is reused when it starts with the referenced prefix; otherwise the import fails, because replacing an ancestor would change the history of its other forks, and Codex refuses to delete a thread that forks reference.
6. Find the session's files in the active and archived session stores, including recovery files of an interrupted replacement, and recover that replacement first. Codex lists only active threads, so the stores are the source of truth. An existing active rollout is updated where it is; a new one is named after its local creation time, as Codex names rollouts.
7. Apply the update rule. A new rollout is written to a stage file under its native name and hard-linked into place, so Codex never sees a partial file and an existing one is never overwritten.
8. Let App Server scan and repair its state, then verify. A self-contained session must appear with the target workspace in the scan, the state database, and `thread/read`. Codex lists a fork only once it has turns of its own and builds a fork's turns only after loading its ancestors, so a fork is verified by resuming its history root first in a separate App Server with goals, hooks, plugins, shell snapshots, and local automations disabled; the session must replay every turn of its history. A failed verification deletes the new session again; placed ancestors remain, as they are complete sessions of their own.

A dry run performs every check up to the update decision and never starts App Server.

### Replacement transaction

App Server does not refresh an indexed thread when its rollout is overwritten, so updates and replacements delete the thread through App Server and repair it from the new rollout:

1. Recover any rollback link left by an interrupted replacement.
2. Stage the new rollout, hard-link the old one as a rollback copy, and sync the directory.
3. Delete the old thread through App Server and confirm that its rollout is gone.
4. Move the staged rollout into place and verify its repair.
5. Remove the rollback link and sync the directory; this is the commit point.

Any failure or interruption before the commit point restores the old rollout, deletes partial state, and repairs the old thread, either immediately or on the next import. Fault-injection tests interrupt the transaction after every persisted phase.

### Supported versions

Each rollout layout covers a range of Codex versions: its turn boundary events and its workspace metadata events. The current layout starts at Codex 0.165.0 and is tested through 0.167.0. Older versions fail closed. Newer versions are accepted with a `doctor` warning, because Codex releases every few days, while structural validation still rejects rollouts it does not recognize. CI runs the two-home test against every tested version, and raising the tested range requires it to pass against the new release. Older layouts are removed rather than kept.

Self-contained sessions and forks are supported. Reverted threads and multi-agent child threads are rejected. Codex App Server forks a thread natively with `thread/fork`, which repository sessions will use for `save --as`, and `export` accepts the full ID of a fork that Codex does not list yet.

## Repository sessions (planned)

Sessions will also be kept in a repository, next to the code they discuss:

```text
agent-teleport save   [SESSION | --last] [--name NAME] [--as NAME]
agent-teleport open   NAME [--resume]
agent-teleport status [--json]
```

- A saved session is an unpacked archive under `.agent-teleport/sessions/<name>/`: the same manifest, followed by the payload files as plain JSONL, which Git stores and diffs efficiently because transcripts only grow at the end.
- Saved paths are relative to the repository root, and `open` maps them to the local checkout.
- `save` records the Git commit and branch, and applies the update rule; a diverged session is saved under a new name with `--as`, through the provider's native fork.
- `status` reports each saved session as in sync, local ahead, repository ahead, diverged, or not opened.
- Saving runs a secret scan and refuses findings unless explicitly accepted, because committing a session publishes it to everyone with repository access.
- The first save creates `.agent-teleport/` and a `.gitattributes` file that keeps payload diffs out of code review.

## Safety and privacy

Session histories can contain prompts, model output, commands, tool results, file contents, paths, and secrets that were printed during a session. Therefore:

- Archives and staged files use owner-only permissions.
- No command prints session content; `inspect --json` reports metadata only.
- Import validates everything before it changes local state and never extracts into an agent's store directly.
- Replacing a diverged session requires `--replace` and keeps a recoverable rollback until the replacement is verified.
- Encryption and signing are candidates for a later format version.

## Testing

- Unit tests cover archive validation, rollout validation and remapping, the update rule, replacement recovery, and CLI parsing.
- A two-home end-to-end test drives the built binary against a native Codex CLI and a sanitized fixture, without credentials or model calls: export, inspect, piped transfer, every update outcome, replacement recovery, archived sessions, and resume after repair. CI installs every tested Codex version on Linux and macOS and requires this test against each.
- Tests that need authentication or model calls are opt-in smoke tests, never pull-request gates.

## Design principles

- Safe defaults over clever recovery.
- Explicit failure over inferred compatibility.
- One session per archive, with one immutable identity.
- Read-only validation before mutation.
- Recoverable, idempotent writes.
- Human-friendly defaults with machine-readable output for automation.
- Provider details behind a small adapter boundary.
