# agent-teleport

`agent-teleport` is a lightweight toolkit for moving resumable local coding-agent
sessions between workstations. Its first binary, `cxport`, targets OpenAI Codex.

The project preserves the recorded provider event stream rather than flattening a
session into a human-readable transcript. An imported session can be associated with
a different workspace and resumed through the native agent CLI.

```sh
cxport export --last -o - | ssh workstation cxport import -
```

The dependency-free CLI core is under active development. It can list, export,
inspect, and import Codex 0.154.0 sessions, including direct stdin/stdout transfer.
See [`ARCHITECTURE.md`](ARCHITECTURE.md) for the command interface, archive format,
fidelity model, and safety boundaries.

```sh
go build ./cmd/cxport
./cxport doctor
./cxport list
./cxport export --last
./cxport inspect ./session.cxport
./cxport import ./session.cxport
```

The interactive picker is intentionally deferred. Until it lands, pass a session ID
or `--last` to `export`. Existing sessions fail closed by default; import supports
explicit `skip` and recoverable `replace` policies.

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

The ordinary test suite includes a credential-free, two-home transfer test when a
supported Codex CLI is installed; otherwise that test skips. To require the installed
Codex version to match the compatibility table, run:

```sh
CXPORT_E2E_REQUIRED=1 go test -v ./test/e2e
```

This test uses a sanitized completed rollout and never starts a model turn.
