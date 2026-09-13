# agent-teleport

`agent-teleport` is a lightweight toolkit for moving resumable local coding-agent
sessions between workstations. Its first binary, `cxport`, targets OpenAI Codex.

The project preserves the recorded provider event stream rather than flattening a
session into a human-readable transcript. An imported session can be associated with
a different workspace and resumed through the native agent CLI.

```sh
cxport export --last -o - | ssh workstation cxport import -
```

The project is in its architecture and compatibility-spike phase. See
[`ARCHITECTURE.md`](ARCHITECTURE.md) for the intended command interface, archive
format, fidelity model, and safety boundaries.

## Initial scope

- OpenAI Codex through the `cxport` binary
- Immutable `.cxport` archives
- Linux and macOS support
- Local and piped transfer without a synchronization service
- Explicit failure for unsupported Codex storage versions

Live processes, credentials, project files, and external tool state are not included
in a session archive.

## Development

The module requires Go 1.25 or newer. Repository conventions live in
[`AGENTS.md`](AGENTS.md), and current work is tracked in
[`context/current.md`](context/current.md).
