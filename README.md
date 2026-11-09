# agent-teleport

`agent-teleport` moves resumable coding-agent sessions between workstations. It preserves the agent's recorded history instead of flattening it into a transcript, maps the session to a local workspace, and leaves you one command away from resuming it with the agent's own CLI.

```sh
agent-teleport export --last -o - | ssh workstation agent-teleport import -
```

Supported agents are OpenAI Codex 0.165.0 or newer, tested through 0.167.0, and Claude Code 2.1.317 or newer, tested through 2.1.319; `agent-teleport doctor` warns about newer, untested releases. Commands find the agent on their own.

## Install

Building requires Go 1.25 or newer:

```sh
go install github.com/Riki1312/agent-teleport/cmd/agent-teleport@latest
agent-teleport doctor
```

To save and open sessions by asking your agent, add the `agent-teleport` plugin. Claude Code and Codex install it from the same marketplace in this repository:

```sh
/plugin install agent-teleport --marketplace Riki1312/agent-teleport   # in Claude Code
codex plugin marketplace add Riki1312/agent-teleport                  # in a shell, for Codex
codex plugin add agent-teleport@agent-teleport
```

Then ask, for example, "save this session to the repo" or "open the fix-login session". After opening, switch to the session in the same chat with `/resume`. Inside an agent, `agent-teleport save` and `export` select the current session on their own, up to the turn that runs them.

## Usage

```sh
agent-teleport list                            # sessions for this directory
agent-teleport export --last                   # writes <title>-<id>.teleport
agent-teleport inspect session.teleport        # validates without changing anything
agent-teleport import session.teleport         # maps the session to this directory
agent-teleport import session.teleport --cwd ~/src/project --resume
```

Run `agent-teleport <command> --help` for every option. Without a session, `export` and `save` let you choose one from a numbered list, and a part of a title selects a session too. For tab completion, add `eval "$(agent-teleport completion bash)"` to `~/.bashrc`, or `source <(agent-teleport completion zsh)` to `~/.zshrc`.

### Sessions in a repository

A session can live next to the code it discusses, so that teammates, or your next machine, pick it up with the checkout:

```sh
agent-teleport save --last --name fix-login   # writes .agent-teleport/sessions/fix-login/
git add .agent-teleport && git commit -m "Share the fix-login session"
agent-teleport status                          # in sync, local ahead, repository ahead, ...
agent-teleport open fix-login --resume         # in another clone, on another machine
```

Saved sessions record paths relative to the repository, so they name no machine paths under it, and `open` maps them to the local checkout. Saving again updates the saved copy when you continued the session. Saving refuses likely credentials, such as private keys and API tokens, unless you pass `--allow-secrets`: committing a session publishes everything it recorded to everyone with access to the repository.

Importing a session that already exists is safe to repeat: a longer archive updates the local session, a longer local session is kept, and identical copies are left alone. Copies that have diverged are only overwritten with `--replace`.

## What moves

An archive contains the session's recorded history and nothing else. Project files, uncommitted changes, credentials, running processes, and tool state stay on the source machine, so check out the same code before resuming. Archives can contain prompts, command output, file contents, and secrets printed during the session: treat them as sensitive.

A session travels whole. A Codex session brings the sub-agent threads it spawned, the reverts it went through, and the part of each session it was forked from; a Claude Code session brings its rewinds, sub-agent transcripts, and saved tool outputs. Every mention of the workspace path, including in tool calls and outputs, is moved to the target directory, so the agent sees files where they now are. Close a session in its agent before importing a newer copy of it; agent-teleport refuses to change a session that is open.

## Development

```sh
make check   # lint, tests, Markdown formatting, whitespace
make format  # gofmt and Prettier
make e2e     # require the native two-home test
```

`make check` needs Go and Node. The end-to-end tests run against the installed, supported Codex and Claude Code CLIs without credentials or model calls, and skip an agent that is not installed; `make e2e` and CI require both. Contributor and agent conventions live in [`AGENTS.md`](AGENTS.md).
