# agent-teleport

[![CI](https://github.com/Riki1312/agent-teleport/actions/workflows/ci.yml/badge.svg)](https://github.com/Riki1312/agent-teleport/actions/workflows/ci.yml)

Keep your coding-agent sessions in the repository, next to the code they produced.

The conversation behind a change is often the best record of why the code looks the way it does: what was tried, what was ruled out, what the agent found along the way. `agent-teleport` saves [Codex](https://github.com/openai/codex) and [Claude Code](https://github.com/anthropics/claude-code) sessions into your Git repository as plain files, so teammates, reviewers, and your other machines can open them and pick up where you left off, with the full history the agent recorded, not a pasted transcript.

## How it feels

In Claude Code or Codex, with the plugin installed, ask:

> save this session to the repo

The agent runs `agent-teleport save`, which writes the session to `.agent-teleport/sessions/<name>/`, and suggests the commit. In another clone, on another machine, or on a teammate's laptop:

```sh
git pull
agent-teleport open fix-login
```

`open` adds the session to the local agent, mapped to that checkout, and tells you how to continue it: `claude --resume <id>`, or `/resume <id>` without leaving a running Claude Code chat. The agent sees the whole conversation and the paths of the new checkout.

Everything also works from a terminal:

```sh
agent-teleport save                 # choose a session of this directory
agent-teleport status               # compare saved sessions with your local copies
git add .agent-teleport && git commit -m "Share the fix-login session"
```

## Install

With Homebrew, on macOS or Linux, which also installs shell completion:

```sh
brew install riki1312/tap/agent-teleport
agent-teleport doctor               # checks the installed agents
```

Or download the binary for your platform from the [latest release](https://github.com/Riki1312/agent-teleport/releases/latest): `darwin_arm64`, `darwin_amd64`, `linux_amd64`, or `linux_arm64`.

```sh
curl -fsSL https://github.com/Riki1312/agent-teleport/releases/latest/download/agent-teleport_darwin_arm64.tar.gz | tar -xz agent-teleport
sudo mv agent-teleport /usr/local/bin/
```

The binaries are not notarized, so on macOS a copy downloaded through a browser needs `xattr -d com.apple.quarantine agent-teleport` once; `curl` downloads need nothing. Checksums are in each release's `SHA256SUMS`. With Go 1.25 or newer, `go install github.com/Riki1312/agent-teleport/cmd/agent-teleport@latest` builds it instead.

The plugin teaches both agents when and how to run it. Claude Code and Codex install it from the same marketplace:

```sh
/plugin install agent-teleport --marketplace Riki1312/agent-teleport   # inside Claude Code
codex plugin marketplace add Riki1312/agent-teleport                  # in a shell, for Codex
codex plugin add agent-teleport@agent-teleport
```

Without Homebrew, add `eval "$(agent-teleport completion bash)"` to `~/.bashrc`, or `source <(agent-teleport completion zsh)` to `~/.zshrc`, for tab completion.

## Sessions in the repository

A saved session is a directory of plain files: a manifest, and the agent's own session files, byte for byte apart from paths. Agents only ever append to their session files, so saving a continued session adds lines at the end, and Git diffs stay small. The first save adds a `.gitattributes` that keeps the files exact in every checkout and collapses them in code review.

- **Whole sessions.** A Codex session carries the sub-agents it spawned, its reverts, and the part of any session it was forked from. A Claude Code session carries its rewinds, sub-agents, and large tool outputs.
- **Relative paths.** Every mention of the repository's path, in tool calls and outputs too, is stored relative to the repository and mapped to the local checkout on `open`, so saved sessions name no machine paths inside the repository.
- **Safe updates.** Saving again updates the saved copy when you continued the session. `status` reports each session as in sync, local ahead (save it), repository ahead (open it), diverged, or not opened. A diverged session is never overwritten silently: open the saved copy with `--replace`, or fork the session in your agent and save the fork under another name.
- **The current session.** Run by an agent, `save` saves the session it runs in, up to the turn in progress. Each save records the commit and branch it was made at.

Committing a session publishes everything it recorded, prompts, command output, and file contents, to everyone with access to the repository. Saving therefore refuses likely credentials, such as private keys and API tokens from AWS, GitHub, Anthropic, OpenAI, Slack, Google, and Stripe, unless you pass `--allow-secrets`. It checks only distinctive formats, so read what you commit.

## Moving sessions between machines

Sessions also travel as single `.teleport` archives, without a repository:

```sh
agent-teleport export fix -o -  | ssh workstation agent-teleport import -
agent-teleport import session.teleport --cwd ~/src/project
agent-teleport inspect session.teleport    # validates without changing anything
```

Importing is safe to repeat: a longer archive updates the local session, a longer local one is kept, identical copies are left alone, and a diverged copy is replaced only with `--replace`. A session that is open in its agent is never changed.

## What travels, and what does not

An archive or a saved session holds the recorded history and nothing else. Project files, uncommitted changes, credentials, running processes, and tool state stay where they were, so check out the same code before resuming. Paths outside the workspace, such as your home directory, stay as recorded.

## Compatibility

| Agent       | Supported        | Tested through |
| ----------- | ---------------- | -------------- |
| Codex       | 0.165.0 or newer | 0.169.0        |
| Claude Code | 2.1.317 or newer | 2.1.323        |

Linux and macOS are supported. The agents' session files are private formats that change often, so `agent-teleport` checks every file against the layouts it knows and refuses what it does not recognize instead of guessing; `doctor` warns about agent versions newer than the tested ones. The project is young, and the archive format may still change before its first stable release.

How it works, file by file, is in [`ARCHITECTURE.md`](ARCHITECTURE.md). To report a security issue, see [`SECURITY.md`](SECURITY.md).

## Development

```sh
make check   # lint, tests, Markdown formatting, whitespace
make format  # gofmt and Prettier
make e2e     # require the end-to-end tests against both agents
```

`make check` needs Go and Node. The end-to-end tests drive the installed Codex and Claude Code CLIs against scripted local model APIs, so they need no credentials and make no model calls; they skip an agent that is not installed. Contributor and agent conventions live in [`AGENTS.md`](AGENTS.md).
