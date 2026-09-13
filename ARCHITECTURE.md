# agent-teleport architecture

## Status

This document describes the intended version 1 architecture. Codex session storage is
not a public compatibility boundary, so provider details remain version-gated.

## Purpose

`agent-teleport` provides portable, lossless transfer of the recorded history needed
to resume a local coding-agent session on another workstation.

The first provider is OpenAI Codex:

- Project: `agent-teleport`
- Binary: `cxport`
- Archive extension: `.cxport`
- Provider identifier: `openai-codex`

The `.cxport` file is a gzip-compressed tar archive. The custom extension presents a
stable user-facing format while allowing ordinary archive tools to inspect it.

The project name and outer archive format are provider-neutral. A future Claude
adapter may use a separate `clport` binary and an `anthropic-claude` provider
identifier without changing the Codex UX.

## Goals

- Preserve the complete recorded Codex replay stream and resumable turn history.
- Transfer a session between machines without requiring a cloud synchronization
  service.
- Associate an imported session with a different local workspace.
- Make export and import safe, inspectable, scriptable, and predictable.
- Ship as a single CGO-free Go binary.
- Detect unsupported Codex storage layouts and fail without modifying local state.
- Support Linux and macOS first while keeping path behavior testable for Windows.

## Non-goals

- Transferring live processes, shell state, credentials, or external service state.
- Copying the project working tree or uncommitted Git changes.
- Making unavailable models, MCP servers, plugins, skills, or attachments available
  on the destination machine.
- Exporting a session while Codex is writing an incomplete turn.
- Synchronizing concurrent edits to the same session on multiple machines.
- Renaming, cloning, or rewriting session identifiers.
- Providing a generic multi-provider frontend in version 1.

## Fidelity model

A transfer is lossless with respect to the recorded provider event stream: the source
rollout bytes are stored unchanged in the archive. This does not imply that the
destination environment is identical to the source environment.

In particular, tool calls and their recorded results remain in the history, but live
tool state does not move with the archive. Resuming may behave differently when local
files, configuration, permissions, credentials, tools, or provider capabilities
differ.

## Command interface

### List

```text
cxport list [--all] [--json]
```

By default, list sessions associated with the current directory. `--all` includes
sessions from other directories. Human-readable output is the default; `--json`
provides a stable automation interface.

### Export

```text
cxport export [SESSION] [--all] [--last] [-o PATH|-]
```

- With no session selector in an interactive terminal, open a picker filtered to the
  current directory.
- `--all` removes the directory filter from the picker.
- `--last` selects the most recent session associated with the current directory.
- `SESSION` accepts a session UUID or an unambiguous session name.
- `-o PATH` writes to that path.
- `-o -` writes the archive to stdout and all diagnostics to stderr.
- Without `-o`, write `<sanitized-title>-<short-id>.cxport` in the current directory.
- Conflicting selectors are usage errors rather than precedence rules.

Export refuses sessions with an incomplete or actively changing turn. Version 1 does
not provide a force flag for bypassing this check.

### Import

```text
cxport import <ARCHIVE|-> [--cwd PATH | --preserve-cwd]
              [--if-exists error|skip|replace] [--dry-run] [--resume]
```

- The default target workspace is the current directory.
- `--cwd PATH` selects another target workspace.
- `--preserve-cwd` retains the source workspace metadata.
- The default collision policy is `error`.
- `skip` succeeds without changing the existing session.
- `replace` transactionally replaces the existing session after preserving a local
  backup.
- `--dry-run` performs discovery, compatibility, integrity, and collision checks
  without changing state.
- `--resume` launches `codex resume <SESSION_ID>` after a successful import.

Import does not launch Codex by default. On success it prints the imported session ID,
target workspace, and exact command needed to resume it.

An archive read from stdin is first spooled to a private temporary file. Import fully
validates it before modifying Codex state. This enables direct transfer:

```sh
cxport export --last -o - | ssh workstation cxport import -
```

### Inspect

```text
cxport inspect <ARCHIVE|-> [--json]
```

Inspect validates and reports archive metadata, checksums, source provider version,
session identity, original workspace, and local import compatibility. It never changes
Codex state.

### Doctor

```text
cxport doctor [--json]
```

Doctor reports the detected Codex version, Codex home, session store, required App
Server capabilities, and filesystem access. It never changes Codex state.

### Interactive picker

The export picker uses the latest stable Bubble Tea v2 and Bubbles v2 releases pinned
in `go.mod`. It receives normalized session values and returns a session ID; it does
not access Codex storage or create archives. Non-interactive commands, redirected
output, `--json`, and `-o -` never initialize the TUI.

## Archive format

Version 1 uses the following logical layout:

```text
manifest.json
checksums.sha256
providers/openai-codex/rollout.jsonl
```

`manifest.json` is the first tar entry so metadata can be read early. A minimal
manifest resembles:

```json
{
  "format": "agent-teleport",
  "format_version": 1,
  "provider": "openai-codex",
  "provider_version": "0.154.0",
  "session_id": "01a09a34-754a-7343-8d40-37a2af117f2f",
  "created_at": "2026-09-13T09:58:44Z",
  "original_cwd": "/home/alice/src/example",
  "payload": {
    "rollout": "providers/openai-codex/rollout.jsonl"
  }
}
```

The manifest schema will be defined formally before the first stable release.
Unknown required fields or unsupported format versions cause a hard failure.

`rollout.jsonl` contains the original rollout bytes. Version 1 does not copy the source
SQLite database. The initial App Server probe reconstructed the thread from this file
alone; additional payload may be introduced only when a supported-version test proves
it necessary.

`checksums.sha256` detects accidental corruption of every payload entry. It is not a
digital signature and does not establish who created the archive.

Archives are immutable after creation. Workspace remapping produces a transformed
copy during import; it never edits the archive or its preserved source rollout.

## Component boundaries

```text
CLI/TUI
  |
  +-- Archive package (provider-neutral)
  |     manifest, tar/gzip, checksums, validation
  |
  +-- Provider interface
        |
        +-- Codex adapter
              discovery
              quiescence checks
              rollout validation
              compatibility detection
              workspace remapping
              state repair and verification
```

Provider-specific paths, event shapes, database schemas, and compatibility rules must
remain inside the provider adapter. The CLI and archive packages operate on normalized
session metadata and opaque provider payloads.

The provider interface should be narrow and derived from implemented workflows rather
than designed for hypothetical providers. Provider-neutral abstractions are introduced
only where the Codex implementation already requires them.

## Codex export

The adapter performs these steps:

1. Locate the active Codex home and supported state store.
2. Discover candidate sessions and resolve the requested selector.
3. Reject an active App Server status, then independently require the rollout to end
   in the version-specific completed-turn event. App Server status alone is not a
   cross-process activity lock.
4. Read a consistent snapshot and verify that its size and modification time did not
   change during the read.
5. Validate its session identity and metadata.
6. Build the archive in a private temporary location.
7. Verify the completed archive and atomically rename it to the output path.

For `-o -`, the verified temporary archive is copied to stdout after construction.
This sacrifices true streaming in exchange for never emitting a partial archive that
appeared successful.

Any compatibility adapter that reads SQLite directly must account for WAL mode and
concurrent Codex activity. It must not copy a live database file directly.

## Codex import

The adapter performs these steps:

1. Spool and validate the entire archive without changing local state.
2. Confirm the provider, archive version, Codex version, and rollout layout are
   supported.
3. Resolve and canonicalize the target workspace.
4. Detect an existing session ID and apply the selected collision policy.
5. Create a private staging directory and construct the destination rollout.
6. Remap the format-specific workspace metadata required by the installed Codex
   version. For Codex 0.154.0 this includes workspace fields in `session_meta`,
   `world_state`, `turn_context`, and `thread_settings_applied` events; changing only
   the first line is insufficient.
7. Atomically place the rollout using an ordering that can be recovered after
   interruption.
8. Ask Codex App Server to scan the session store and repair its owned index state.
9. Verify discovery from state-only listing, read the thread, and verify resume support
   where the target history mode permits it.
10. Use direct transactional state repair only for a known Codex version where tests
    prove App Server repair is insufficient.
11. Update legacy indexes only when a compatibility adapter proves they are required.

The Codex 0.154.0 spike confirmed that normal `thread/list` discovers the placed
rollout, persists repaired state, and supports `thread/read` and `thread/resume` after
an App Server restart. This matches the documented scan-and-repair behavior of
[`useStateDbOnly`](https://learn.chatgpt.com/docs/app-server). It does not yet establish
the commit and rollback protocol. That protocol must be tested with fault injection
before `replace` is considered stable. Import should leave either the old valid
session or the new valid session, never a mixture.

## Compatibility policy

Codex local storage is treated as a versioned private implementation detail.

- Each adapter declares the Codex versions, rollout layouts, and App Server
  capabilities it supports.
- Unknown event layouts or missing capabilities fail closed with a diagnostic from
  `cxport doctor`.
- Import compatibility is directional: a bundle that can be inspected may still be
  too new or too old to import into the installed Codex version.
- Schema introspection is used to recognize known layouts, not to guess how unknown
  columns should be populated.
- Backward-compatibility files such as `session_index.jsonl` are updated only for
  versions known to use them.
- Supported-version fixtures and round-trip tests are kept for every compatibility
  adapter.

Where a supported Codex API can perform an operation safely, it should be preferred
over direct database mutation. Direct storage integration remains an adapter detail.

## Safety and privacy

Rollouts may contain prompts, model output, reasoning records, commands, tool results,
file contents, paths, repository metadata, and secrets accidentally printed during a
session. Therefore:

- New archives and temporary files use owner-only permissions.
- Import rejects absolute archive paths, parent traversal, links, duplicate entries,
  unexpected file types, and entries exceeding configured size limits.
- Import never extracts directly into the Codex store.
- Logs avoid printing rollout contents or secrets.
- `inspect --json` reports metadata, not conversation content.
- Replacing a session requires an explicit flag and creates a recoverable backup.
- Encryption and signing are candidates for a later format version, not implicit
  properties of version 1.

## Design principles

- Safe defaults over clever recovery.
- Explicit failure over inferred compatibility.
- One session per archive.
- One immutable session identity.
- Read-only validation before mutation.
- Atomic, idempotent import wherever possible.
- Human-friendly defaults with machine-readable output for automation.
- Provider-specific internals behind a small adapter boundary.
