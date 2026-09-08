# Agent instructions for this repository

You are editing **Termux Agent**, a pure-Go agentic coding CLI for Android Termux.

## Constraints

- Keep the dependency graph empty: **Go standard library only** (no CGO, no third-party modules).
- Target `GOOS=android GOARCH=arm64` with `CGO_ENABLED=0`.
- Prefer small, focused files. Match existing style (tabs, short comments that explain *why*).
- Never weaken the path sandbox in `resolveTarget` / `isPathAllowed`.
- Shell execution must remain timeout-bounded and process-group aware on Linux/Android.

## Product goals

- Compete with desktop agents (Crush, Claude Code, Codex) for **real multi-file builds**.
- Optimize for Termux: PPK, metered data, small RAM, session resume.

## Tests

```bash
go test ./...
```
