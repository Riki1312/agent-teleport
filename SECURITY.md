# Security

`agent-teleport` handles coding-agent sessions, which can hold prompts, command output, file contents, and credentials printed during a session. Security reports are welcome, and fixes go to the latest version on `main`.

## Reporting a vulnerability

Report privately through GitHub: open the repository's **Security** tab and choose **Report a vulnerability**. Please do not open a public issue, and do not include real session data; describe the problem and, where possible, how to reproduce it with a made-up session.

You can expect an acknowledgement within a week. Once a fix is available, the report is disclosed together with it.

## Scope

These matter most:

- Session content exposed where it should not be, such as printed output, logs, or files outside the archive, the repository's `.agent-teleport` directory, and the agents' own session stores.
- Writes outside those locations, for example through crafted archive paths or links.
- Loss or corruption of an agent's existing sessions.
- Credentials that `save` should refuse but lets through, for the formats it documents.

`agent-teleport` makes no network requests of its own. Leaks of secrets in formats the scanner does not claim to detect, such as passwords, are known limitations rather than vulnerabilities: committing a session publishes what it recorded.
