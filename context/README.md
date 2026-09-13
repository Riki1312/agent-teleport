# Context system

This directory is lightweight shared memory for contributors and coding agents.

- `current.md` contains the compact current handoff.
- `decisions.md` is an append-only log of durable decisions.
- `sessions/` contains optional notes for substantial work.

Keep entries short and concrete. Separate facts, open questions, and next actions.
Link repository evidence when useful. Never store secrets, credentials, or session
contents here.
