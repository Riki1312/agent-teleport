# Architecture

`agent-teleport` moves the recorded history of a local coding-agent session to another workstation, or into a repository, so the session can be resumed with the agent's own CLI. This document describes the current design. Durable decisions and their rationale live in [`context/decisions.md`](context/decisions.md).

## Scope

The CLI is a single CGO-free Go binary, `agent-teleport`, for Linux and macOS. It supports OpenAI Codex and Claude Code. Commands detect the provider from the session or archive, so users never name it: `list` and `export` consult every installed provider, and `import` and `inspect` use the provider named in the manifest.

Goals:

- Preserve the complete recorded session history byte for byte, apart from documented workspace path moves.
- Map an imported session to a different local workspace.
- Make every command safe, inspectable, scriptable, and predictable.
- Fail without changing local state when a storage layout is unknown.

Non-goals:

- Transferring live processes, shell state, credentials, project files, uncommitted changes, or external service state.
- Making models, MCP servers, plugins, or skills available on the destination machine.
- Exporting the turn an agent is still working on.
- Merging concurrent edits of one session made on different machines.
- Renaming or cloning session identifiers, except through a provider's native fork.

## Fidelity model

A transfer is lossless with respect to the recorded provider event stream: an archive stores the provider's files unchanged. Importing into another directory moves the workspace path, and nothing else: every mention of the source workspace at path boundaries, inside every JSON string of every record and in saved tool outputs, becomes the same path inside the target workspace, and moving back restores the original bytes. Paths outside the workspace stay as recorded, so a resumed session may still refer to files, tools, or permissions that differ on the destination machine.

## Commands

```text
agent-teleport list    [--all] [--json]
agent-teleport export  [SESSION | --last] [--all] [-o PATH|-]
agent-teleport import  (ARCHIVE | -) [--cwd PATH] [--replace] [--dry-run] [--resume]
agent-teleport inspect (ARCHIVE | -) [--json]
agent-teleport save    [SESSION | --last] [--all] [--name NAME] [--allow-secrets]
agent-teleport open    NAME [--replace] [--dry-run] [--resume]
agent-teleport status  [--json]
agent-teleport doctor  [--json]
```

- `list` shows the sessions of the current directory, or of every directory with `--all`, newest first. `--json` is the stable automation interface.
- `export` accepts a session ID, a unique ID prefix, or an exact title; inside Codex or Claude Code, the current session is the default. A turn in progress is left out. Without `-o` it writes `<title>-<id>.teleport` to the current directory; `-o -` writes the archive to stdout and every diagnostic to stderr.
- `import` maps the session to the current directory unless `--cwd` names another one. It prints the command that resumes the session, and runs it with `--resume`.
- `inspect` validates an archive, describes its session, and reports whether it can be imported into the current directory. It never changes local state.
- `save`, `open`, and `status` keep sessions in the current Git repository; see [Repository sessions](#repository-sessions).
- `doctor` reports the detected agents, their versions, and whether they are supported.

Usage errors exit with status 2 and other failures with status 1.

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

The rule applies to every file or thread of a session separately; each adapter below describes how it updates and replaces them. A session that is open in the agent is never changed.

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
  "home": "/home/alice/.codex",
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

- `home` is the provider's storage directory on the source machine, for providers that record paths into it.
- Payload paths are clean, relative, and slash-separated; the provider chooses them.
- Decoding rejects unknown manifest fields, unsupported format versions, unlisted or missing entries, links and other non-regular files, size limit violations, and digest mismatches.
- Digests detect accidental corruption. They are not signatures and do not establish who created an archive.
- Archives are immutable. Export verifies the encoded archive before writing it, and creates the output file exclusively.

The format is unreleased, so it changes in place without migrations until the first stable release.

## Components

```text
cmd/agent-teleport        process entry point
internal/cli              argument parsing, session selection, output
internal/provider         provider-neutral sessions, import outcomes, adapters
internal/archive          provider-neutral manifest, packing, validation
internal/codex            Codex adapter: discovery, export, import, repair
internal/codex/appserver  JSON-RPC client for codex app-server --stdio
internal/claude           Claude Code adapter: discovery, export, import
internal/repository       sessions saved in a Git repository
internal/secrets          credential scan before saving
internal/pathmap          reversible workspace path moves in JSON records and text
internal/fsutil           stable reads and atomic writes
internal/semver           provider version ordering
internal/buildinfo        build version
tools/sanitize-fixture    produces sanitized Codex fixtures
plugins/agent-teleport    agent plugin with the teleport skill, for Claude Code and Codex
```

The CLI operates on archives and plain session values through `provider.Adapter` and `provider.Store`, which were extracted from the two adapters. Provider paths, record shapes, version rules, and state repair stay inside each adapter.

## Codex adapter

Codex storage is a private implementation detail. The adapter prefers Codex App Server over direct database access and never writes Codex databases.

### Sessions and rollout objects

Codex stores each thread in rollout files under `sessions/` (and `archived_sessions/`). A rollout's first record, `session_meta`, names its thread and its session; they are equal for a session's root thread, while a sub-agent thread names the session of the root that spawned it. A rollout may start with a `history_base` that references a byte prefix of another rollout object:

- A fork references a prefix of another thread's rollout.
- A revert snapshot, named `rollout-<time>-<thread>_<object>.jsonl`, references a prefix of an earlier object of the same thread and becomes the thread's current history. Later turns are appended to it, and a second revert references the first snapshot's object.

An archived Codex session therefore holds every object of the root thread and of its sub-agent threads, plus the referenced prefix of every ancestor they fork from, ordered so that each referenced object precedes the files that reference it. Payloads keep their native file names.

### Export

1. Discover sessions through App Server `thread/list`, asking for every user-started source and every model provider explicitly: App Server lists only interactive sources and the configured provider by default, which hides `codex exec` sessions and sessions of other providers. A scanning listing reports a reverted thread twice, so results are deduplicated.
2. Collect the session from its rollout files, without App Server, which cannot write its state inside Codex's sandbox: the root thread's objects, every thread whose session ID names the root, and the objects their references lead to. Each thread's current object is determined from its local files, and ancestors outside the session contribute only the referenced prefix.
3. Read the complete records of every file; Codex only appends, so they are a consistent snapshot while a process writes. Cut each thread's current object before the `task_started` record of a turn in progress, such as the turn running agent-teleport, so every remaining turn has completed or been aborted.
4. Validate identities, references at record boundaries, and turn boundaries across each thread's logical history.
5. Build the archive in memory, verify it, and write it.

`export` accepts the full ID of a thread that Codex does not list, such as a fork without turns of its own. Exporting a sub-agent thread fails with the session to export instead.

### Import

1. Decode and validate the whole archive in memory, and require the archive's Codex version and the installed version to share a rollout layout.
2. Canonicalize the target workspace, resolving symlinks in its existing ancestors.
3. Move the workspace in every file, as described in the fidelity model. This covers plain paths, paths below the workspace, `file://` URIs, paths embedded in generated text, and tool calls and outputs. Every reference is moved to the same record boundary of the rewritten object.
4. Recover writes an earlier import left unfinished.
5. Plan every thread before changing anything. Codex's stores are the source of truth, because Codex lists neither archived threads nor forks without turns. A local thread is compared segment by segment, where a segment is the original rollout or the records a snapshot holds after its revert header, and the update rule decides whether to keep it, append the incoming records, or replay missing reverts. Ancestors outside the session are reused when they contain the referenced prefix and are never modified; Codex also refuses to delete a thread that forks reference.
6. Execute the plan in archive order. A new original rollout is written to a stage file under its native name and hard-linked into place, so Codex never sees a partial file and an existing one is never overwritten. A revert is replayed through App Server `thread/revert` on the same turn, so that Codex writes its own snapshot and records it as the thread's current history; the archived snapshot's later records are then appended to it. References to replayed snapshots are pointed at Codex's new objects.
7. Let App Server scan and repair its state, then verify. A session of one self-contained thread must appear with the target workspace in the scan, the state database, and `thread/read`. Otherwise the session is loaded in order in a separate App Server with goals, hooks, plugins, shell snapshots, and local automations disabled, because Codex builds a fork's turns only after loading its ancestors and loads sub-agents through their parent: the root thread must replay every turn, and every sub-agent thread must be readable in its workspace.

A dry run performs every check up to the plan and never starts App Server.

### Appending and coordination

Codex picks up records appended to a rollout when it next loads the thread, so updates append records instead of rewriting files. Codex 0.165 and later hold an exclusive `flock` on `thread-writer-locks/<thread>.lock` while any process has the thread loaded. Import takes that lock before appending, and checks it before replacing or replaying a thread, so a session open in Codex is never changed. An append first records the rollout's original and final sizes in a marker file; an interrupted append is completed or truncated back on the next import.

### Replacement transaction

App Server does not refresh an indexed thread when its rollout is overwritten, so replacing a diverged single-thread session deletes the thread through App Server and repairs it from the new rollout:

1. Recover any rollback link left by an interrupted replacement.
2. Stage the new rollout, hard-link the old one as a rollback copy, and sync the directory.
3. Delete the old thread through App Server and confirm that its rollout is gone.
4. Move the staged rollout into place and verify its repair.
5. Remove the rollback link and sync the directory; this is the commit point.

Any failure or interruption before the commit point restores the old rollout, deletes partial state, and repairs the old thread, either immediately or on the next import. Fault-injection tests interrupt the transaction after every persisted phase. Sessions with forks, reverts, or sub-agents cannot be replaced; diverged copies must be deleted in Codex first.

### Supported versions

Each rollout layout covers a range of Codex versions: its turn boundary events, its workspace metadata events, and the size of its revert header. The current layout starts at Codex 0.165.0 and is tested through 0.167.0. Older versions fail closed. Newer versions are accepted with a `doctor` warning, because Codex releases every few days, while structural validation still rejects rollouts it does not recognize. CI runs the two-home test against every tested version, and raising the tested range requires it to pass against the new release. Older layouts are removed rather than kept.

## Claude Code adapter

Claude Code keeps each session as an append-only JSONL transcript, `projects/<folder>/<session>.jsonl`, under its configuration directory (`CLAUDE_CONFIG_DIR`, by default `~/.claude`). The folder is named after the workspace the session started in: every UTF-16 code unit other than an ASCII letter or digit becomes `-`, and a name longer than 200 characters is cut to 200 and suffixed with `-` and a base-36 hash of the path. Beside the transcript, the session folder `<session>/` holds sub-agent transcripts and metadata in `subagents/` and tool outputs too large for the transcript in `tool-results/`. Rewinds are branches of the record tree inside the same transcript, and a native fork is a separate, self-contained session.

Claude Code has no index and no server: it resumes a session by finding its transcript in any project folder, appends to it in place, and keeps a registry of running processes in `sessions/<pid>.json`. The adapter therefore reads and writes the files directly and never touches other Claude Code state.

### Export

1. List sessions from the transcripts of the workspace's project folder, or of every folder with `--all`, reading only complete records. A session's title is its custom title, else its generated title, else its first prompt.
2. Read the complete records of the transcript and the sub-agent transcripts, and every other file of the session folder, failing on a file that is not part of the known layout. Every record must belong to the session, and every sub-agent record to its agent.
3. When a running Claude Code process reports the session as anything but idle, cut the transcript before the queue records and prompt of its last turn, and leave out the sub-agents that turn started.
4. Build the archive with the payload paths relative to the project folder. File history backups are project files and are not exported.

### Import

1. Validate the archive and require the archive's Claude Code version and the installed version to share a transcript layout.
2. Move two directories in every record and saved tool output: the source workspace to the target workspace, and the source project folder to the target's, which the recorded system prompt names as the memory folder and tool results name for saved outputs. Claude Code replays the recorded system prompt and environment when it resumes, so the model sees only target paths.
3. Compare every file with the local copy in the target's project folder. Transcripts follow the update rule by prefix; tool outputs must match; sub-agent metadata follows the rest of the session. A copy in another project folder is moved only with `--replace`.
4. Refuse any change while a running process has the session open. The registry is checked, not locked, so a Claude Code process that starts during an import is not detected.
5. Write each file to a stage file and rename it into place, the transcript last, then remove the files a replacement drops and verify the result.

### Supported versions

The current layout starts at Claude Code 2.1.317 and is tested through 2.1.319, with the same rules as Codex: older versions fail closed, newer versions are accepted with a `doctor` warning, and structural validation rejects unknown files.

## Repository sessions

Sessions can be kept in a Git repository, next to the code they discuss, and shared by committing them.

- A saved session is an unpacked archive under `.agent-teleport/sessions/<name>/`: the manifest, followed by the payload files at their payload paths, which Git stores and diffs efficiently because transcripts only grow at the end. The manifest also records the commit and branch checked out when it was saved.
- Saved paths are relative to the repository. `save` moves the repository root to the placeholder `/agent-teleport/repository` and the provider's home to `/agent-teleport/home`, in every record, and `open` moves them to the local checkout and agent home. The session's workspace must be inside the repository.
- `save` applies the update rule against the saved copy: a longer local session replaces it, an equal one is left alone, a longer saved copy must be opened first, and a diverged one is refused with two ways out: `open --replace`, or a fork made in the agent and saved under another `--name`.
- `open` imports the saved copy into its place in the checkout, with the same rule and options as `import`.
- `status` reports each saved session as in sync, local ahead, repository ahead, diverged, or not opened. Like `save`, it compares through a dry-run import, so it never changes a session.
- Saving refuses likely credentials in what it adds, unless `--allow-secrets` is given; see `context/decisions.md` for the patterns.
- A save is written to a hidden sibling directory, verified, and swapped in with renames; the next command restores the previous copy if a swap was interrupted. The first save creates `.agent-teleport/.gitattributes`, which keeps sessions byte for byte in every checkout and marks them as generated, so code review collapses their diffs.

## Agent plugin

One plugin, `plugins/agent-teleport`, serves both agents. `.claude-plugin/marketplace.json` at the repository root lists it, and Codex reads that Claude Code marketplace and plugin manifest as well as Claude Code does. Its `teleport` skill tells the agent when and how to run `agent-teleport save`, `status`, `open`, and `export` for the current session, and what to leave to the user: committing, accepting possible secrets, and replacing a diverged copy. An end-to-end test installs the plugin into isolated homes of both agents and requires each to offer the skill.

## Safety and privacy

Session histories can contain prompts, model output, commands, tool results, file contents, paths, and secrets that were printed during a session. Therefore:

- Archives and staged files use owner-only permissions.
- No command prints session content; `inspect --json` reports metadata only.
- Import validates everything before it changes local state and never extracts into an agent's store directly.
- Replacing a diverged session requires `--replace`; Codex replacements keep a recoverable rollback until they are verified.
- Encryption and signing are candidates for a later format version.

## Testing

- Unit tests cover archive validation and directories, rollout and transcript validation, path moves, the update rule, replacement recovery, secret patterns, saved session names, and CLI parsing.
- A Codex two-home end-to-end test drives the built binary against a native Codex CLI and sanitized fixtures, without credentials or model calls: export, inspect, piped transfer, every update outcome, replacement recovery, archived sessions, forks, reverts, sub-agents, and resume after repair.
- A Claude Code two-home end-to-end test records sessions with the native Claude Code CLI against a scripted local Messages API, so it needs no credentials and makes no model calls: sub-agents, saved tool outputs, every update outcome, open and busy sessions, moves between workspaces, a hashed project folder, and resumes whose requests must name only target paths.
- Repository end-to-end tests save sessions of both providers in a Git repository, open them in a clone with another agent home, and follow them through every status, a secret refusal, and a divergence.
- CI installs every tested version of both providers on Linux and macOS and requires these tests against each.
- Tests that need authentication or model calls are opt-in smoke tests, never pull-request gates.

## Design principles

- Safe defaults over clever recovery.
- Explicit failure over inferred compatibility.
- One session per archive, with one immutable identity.
- Read-only validation before mutation.
- Recoverable, idempotent writes.
- Human-friendly defaults with machine-readable output for automation.
- Provider details behind a small adapter boundary.
