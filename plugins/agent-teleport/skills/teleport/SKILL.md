---
name: teleport
description: Save this coding session in the Git repository so it can be committed and resumed elsewhere, check which saved sessions are up to date, open a saved session, or export this session to a file, with the agent-teleport CLI. Use when the user asks to save, share, commit, hand off, teleport, export, or move this session or conversation, or to open or check sessions saved in the repository.
---

# Teleport sessions

`agent-teleport` saves coding-agent sessions, of Codex or Claude Code, under `.agent-teleport/sessions/` in the Git repository, so they can be committed and resumed in another clone or on another machine. Run it from inside the repository. It finds the current session on its own.

## Before running a command

Check that the CLI is installed with `agent-teleport --version`. If it is missing, tell the user that it installs with `brew install riki1312/tap/agent-teleport`, or from the binaries of the latest release at https://github.com/Riki1312/agent-teleport/releases/latest, and install it only if they ask you to.

## Save this session

Run `agent-teleport save`. Pass `--name NAME` only when the user names the session; names use lowercase letters, digits, dots, dashes, and underscores. The save holds the session up to the turn you are working on, so tell the user that running the command again later saves the rest.

Report the path it prints. Committing publishes the session to everyone with access to the repository, so suggest `git add .agent-teleport` and a commit, and run them only if the user asks.

When the save refuses possible secrets, show the user the findings it printed: the file, the line, and the kind of credential. Never pass `--allow-secrets` on your own; the user decides, after rotating any real credential.

When the save reports that the saved copy is newer, or that the copies have diverged, relay its explanation and let the user choose; never pass `--replace` without being asked.

## Check and open saved sessions

- `agent-teleport status` lists each saved session as in sync, local ahead, repository ahead, diverged, or not opened.
- `agent-teleport open NAME` adds a saved session to the agent on this machine. It prints how to switch to it in this chat, such as typing `/resume` with the session ID, and the command that resumes it in a new terminal; relay both. Only the user can switch sessions, so never pass `--resume`.

## Preserve a conflicting Codex ancestor

When import or open reports a conflicting or archived Codex ancestor, explain that `--isolated` keeps a separate local history while preserving existing sessions. If the user wants both copies, run `agent-teleport import FILE --isolated` or `agent-teleport open NAME --isolated`. Relay the returned `agent-teleport resume iso-…` command for another terminal; the normal native resume picker cannot see that copy. Never run `resume` or `--resume` inside this agent.

Use the local `iso-…` reference with `export` or `save` when selecting that copy from outside its running agent. Inside the isolated agent, normal current-session selection works. `status --isolated` compares repository sessions with isolated copies. Runtime settings and file login are shared with the normal Codex setup; native keyring-backed login can require a separate sign-in.

## Export to a file

`agent-teleport export -o FILE.teleport` writes this session to an archive that `agent-teleport import` reads on another machine.

## Privacy

Sessions hold prompts, command output, and file contents. Never print the content of saved session files or archives; describe them by their metadata, such as `agent-teleport status` output.
