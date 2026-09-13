# Code principles

## Priorities

When goals compete, prefer:

1. correctness
2. simplicity
3. user experience
4. measured performance

Correctness includes faithful session data, atomic state changes, precise diagnostics,
and honest rejection of unsupported formats.

## Principles

### Keep concepts small

Prefer direct code and one source of truth. Add an abstraction only when it names a
real boundary or has a concrete second use. Code, files, dependencies, compatibility
paths, and test infrastructure all carry maintenance cost.

### Make unsafe states difficult to express

Validate untrusted archives before mutation. Keep staging and committed state
distinct. Use explicit types for provider identity, format versions, collision policy,
and validated paths instead of loosely related strings or booleans.

### Fail honestly

Reject unknown Codex storage layouts, archive versions, and inconsistent session
records with actionable errors. Do not guess, silently repair, or report partial
success.

### Preserve boundaries

Archive handling is provider-neutral. Codex discovery, rollout transformation, and
state repair are provider-specific. The CLI and TUI orchestrate those layers without
owning their rules.

### Keep the main path visible

Order files and functions around the workflow: discover, validate, stage, commit,
verify. Keep state narrow and validate input at the boundary where it enters.

### Comments explain constraints

Comment invariants, compatibility constraints, recovery behavior, and non-obvious
tradeoffs. Do not narrate code that clear names already explain.

### Test guarantees, not implementation shape

Prefer behavioral tests for archive compatibility, CLI output, atomic import, and real
Codex discovery. Use focused unit tests for parsers, transformations, and security
boundaries. A refactor that preserves behavior should not require broad test rewrites.

### Remove obsolete paths

When a design replaces an older path, remove the old code, tests, terminology, and
documentation unless compatibility is an explicit requirement.

## Review questions

- Does this preserve the recorded session exactly except for documented transforms?
- Can interruption leave either the old valid state or the new valid state?
- Is unsupported behavior rejected before mutation?
- Does stdout remain safe for scripts and binary archives?
- Is this logic in the layer that owns the concept?
- Did this change add more concepts than the problem requires?
- Are sensitive session contents kept out of logs and fixtures?
