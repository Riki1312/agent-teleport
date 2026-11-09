# agent-teleport

`agent-teleport` moves resumable coding-agent sessions between workstations. It preserves the agent's recorded history instead of flattening it into a transcript, maps the session to a local workspace, and leaves you one command away from resuming it with the agent's own CLI.

```sh
agent-teleport export --last -o - | ssh workstation agent-teleport import -
```

Supported agents are OpenAI Codex 0.165.0 or newer, tested through 0.167.0, and Claude Code 2.1.317 or newer, tested through 2.1.319; `agent-teleport doctor` warns about newer, untested releases. Commands find the agent on their own. Keeping sessions in a repository next to their code is in progress; see [`ARCHITECTURE.md`](ARCHITECTURE.md).

## Install

Building requires Go 1.25 or newer:

```sh
go install github.com/Riki1312/agent-teleport/cmd/agent-teleport@latest
agent-teleport doctor
```

## Usage

```sh
agent-teleport list                            # sessions for this directory
agent-teleport export --last                   # writes <title>-<id>.teleport
agent-teleport inspect session.teleport        # validates without changing anything
agent-teleport import session.teleport         # maps the session to this directory
agent-teleport import session.teleport --cwd ~/src/project --resume
```

Run `agent-teleport <command> --help` for every option.

Importing a session that already exists is safe to repeat: a longer archive updates the local session, a longer local session is kept, and identical copies are left alone. Copies that have diverged are only overwritten with `--replace`.

## What moves

An archive contains the session's recorded history and nothing else. Project files, uncommitted changes, credentials, running processes, and tool state stay on the source machine, so check out the same code before resuming. Archives can contain prompts, command output, file contents, and secrets printed during the session: treat them as sensitive.

A session travels whole. A Codex session brings the sub-agent threads it spawned, the reverts it went through, and the part of each session it was forked from; a Claude Code session brings its rewinds, sub-agent transcripts, and saved tool outputs. Workspace paths in the recorded environment are moved to the target directory, so the agent sees where it now runs. Close a session in its agent before importing a newer copy of it; agent-teleport refuses to change a session that is open.

## Development

```sh
make check   # lint, tests, Markdown formatting, whitespace
make format  # gofmt and Prettier
make e2e     # require the native two-home test
```

`make check` needs Go and Node. The end-to-end tests run against the installed, supported Codex and Claude Code CLIs without credentials or model calls, and skip an agent that is not installed; `make e2e` and CI require both. Contributor and agent conventions live in [`AGENTS.md`](AGENTS.md).
