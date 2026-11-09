package cli

const usage = `agent-teleport moves resumable coding-agent sessions between machines.

Usage:
  agent-teleport <command> [options]

Commands:
  list      List local sessions for this directory
  export    Write a session to a .teleport archive or stdout
  import    Add a session from a .teleport archive or stdin
  inspect   Check an archive without changing anything
  save      Save a session in this Git repository
  open      Add a session saved in this repository to its agent
  status    Compare saved sessions with their local copies
  doctor    Check installed agents and compatibility

Options:
  -h, --help   Show help
  --version    Show the agent-teleport version

Run "agent-teleport <command> --help" for command options.
`

const listUsage = `List local sessions for this directory, most recent first.

Usage:
  agent-teleport list [--all] [--json]

Options:
  --all    List sessions from every directory
  --json   Print machine-readable JSON
`

const exportUsage = `Write a session to a .teleport archive.

Usage:
  agent-teleport export (SESSION | --last) [--all] [-o PATH|-]

SESSION is a session ID, a unique ID prefix, or an exact session title.
A session cannot be exported while its agent is working on a turn. A
session carries its sub-agents, and a forked Codex session the part of
each ancestor session it continues.

Options:
  --last            Export the most recently updated session
  --all             Select from every directory, not only this one
  -o, --output      Write to PATH, or to stdout with -
                    (default: <title>-<id>.teleport in this directory)

Examples:
  agent-teleport export --last
  agent-teleport export --last -o - | ssh workstation agent-teleport import -
`

const importUsage = `Add a session from a .teleport archive, or from stdin with -.

Usage:
  agent-teleport import (ARCHIVE | -) [--cwd PATH] [--replace] [--dry-run] [--resume]

The session is mapped to the current directory unless --cwd is given. When
the session already exists, a copy that extends the other wins: a longer
archive updates the local session, and a longer local session is kept.
Copies that have diverged need --replace. Sessions open in their agent are
never changed; close them first.

Options:
  --cwd PATH   Map the session to PATH instead of the current directory
  --replace    Overwrite a diverged local copy, or one in another workspace
  --dry-run    Check the import without changing anything
  --resume     Resume the session after importing it
`

const inspectUsage = `Check an archive and describe the session it contains.

Usage:
  agent-teleport inspect (ARCHIVE | -) [--json]

Inspect validates every file in the archive and reports whether it can be
imported into the current directory. It never changes local state.

Options:
  --json   Print machine-readable JSON
`

const doctorUsage = `Check installed agents and compatibility.

Usage:
  agent-teleport doctor [--json]

Options:
  --json   Print machine-readable JSON
`

const saveUsage = `Save a session in this Git repository, next to its code.

Usage:
  agent-teleport save (SESSION | --last) [--all] [--name NAME] [--allow-secrets]

The session is written to .agent-teleport/sessions/NAME/ with every path
inside the repository made relative to it, together with the checked-out
commit and branch. Commit the directory to share the session. Saving again
updates the saved copy when the local session extends it; a newer saved
copy must be opened first, and a diverged one is kept: fork the session in
its agent and save the fork under another name.

Saving refuses likely credentials, such as private keys and API tokens,
because committing a session publishes it to everyone with access to the
repository. Only what a save adds is scanned.

Options:
  --last            Save the most recently updated session
  --all             Select from every directory, not only this one
  --name NAME       Save under NAME (default: derived from the title)
  --allow-secrets   Save even when likely credentials are found
`

const openUsage = `Add a session saved in this repository to its agent.

Usage:
  agent-teleport open NAME [--replace] [--dry-run] [--resume]

The session is mapped to this checkout and follows the same rule as
import: a newer saved copy updates the local session, and a newer local
copy is kept.

Options:
  --replace    Overwrite a diverged local copy, or one in another workspace
  --dry-run    Check the open without changing anything
  --resume     Resume the session after opening it
`

const statusUsage = `Compare the sessions saved in this repository with their local copies.

Usage:
  agent-teleport status [--json]

Each saved session is in sync, local ahead (save it), repository ahead
(open it), diverged, or not opened.

Options:
  --json   Print machine-readable JSON
`
