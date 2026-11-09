package cli

const usage = `agent-teleport moves resumable coding-agent sessions between machines.

Usage:
  agent-teleport <command> [options]

Commands:
  list      List local sessions for this directory
  export    Write a session to a .teleport archive or stdout
  import    Add a session from a .teleport archive or stdin
  inspect   Check an archive without changing anything
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
Only sessions whose last turn has completed can be exported. A forked
session carries the part of each ancestor session it continues.

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
Copies that have diverged need --replace.

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
