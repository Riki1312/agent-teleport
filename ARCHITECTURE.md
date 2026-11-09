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
  "provider_version": "0.154.0",
  "session_id": "01a09a34-754a-7343-8d40-37a2af117f2f",
  "workspace": "/home/alice/src/example",
  "created_at": "2026-11-09T09:58:44Z",
  "files": [{ "path": "rollout.jsonl", "size": 48213, "sha256": "…" }]
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

1. Discover sessions through App Server `thread/list`.
2. Require an idle or unloaded thread. App Server cannot see turns running in another Codex process, so the rollout must also end with the version's completed-turn event.
3. Read the rollout and fail if its size or modification time changed during the read.
4. Validate the session identity and reject layouts that one archive cannot preserve: a non-null `history_base`, which refers to other rollout files, and divergent thread and session IDs, which multi-agent child threads use.
5. Build the archive in memory, verify it, and write it.

### Import

1. Decode and validate the whole archive in memory.
2. Require the archive's Codex version to equal the installed version.
3. Canonicalize the target workspace, resolving symlinks in its existing ancestors.
4. Rewrite workspace paths: every JSON string in a workspace metadata event that equals the source workspace or lies below it. Other bytes are preserved, and a workspace path in any other form fails the import.
5. Recover any interrupted replacement, then find existing copies in both the active and archived session stores. Codex lists only active threads, so an archived copy must be found on disk.
6. Apply the update rule. A new rollout is written to a stage file and hard-linked into place, so Codex never sees a partial file and an existing one is never overwritten.
7. Let App Server scan and repair its state, then require the scan, the state database, and `thread/read` to agree on the session and its workspace. A failed verification deletes the new session again.

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

Each supported Codex version maps to a verified rollout layout: its completed-turn event and its workspace metadata events. Unknown versions fail closed with a diagnostic from `doctor`. Support targets the latest and recent Codex releases; adding a version requires a sanitized fixture that passes the two-home test against that exact Codex CLI, and older versions are removed rather than kept.

Codex 0.154.0 is supported today. Modern Codex histories span several physical rollout files and need multi-file payloads before newer versions can be enabled.

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
- A two-home end-to-end test drives the built binary against a native Codex CLI and a sanitized fixture, without credentials or model calls: export, inspect, piped transfer, every update outcome, replacement recovery, archived sessions, and resume after repair. CI installs the pinned supported Codex version on Linux and macOS and requires this test.
- Tests that need authentication or model calls are opt-in smoke tests, never pull-request gates.

## Design principles

- Safe defaults over clever recovery.
- Explicit failure over inferred compatibility.
- One session per archive, with one immutable identity.
- Read-only validation before mutation.
- Recoverable, idempotent writes.
- Human-friendly defaults with machine-readable output for automation.
- Provider details behind a small adapter boundary.
