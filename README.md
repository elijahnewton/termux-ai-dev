# Termux Agent

**World-class agentic coding on Android.** A pure-Go CLI that runs natively in [Termux](https://termux.dev), built to compete with desktop agents like [Crush](https://github.com/charmbracelet/crush), Claude Code, Codex, and Cline — while staying fast, offline-friendly, and Phantom-Process-Killer aware.

Scaffold APIs, web apps, CLIs, and enterprise backends **on the phone filesystem**, with tool calling, plan mode, session recovery, and aggressive context management for mobile RAM and metered data.

## Why Termux-native?

| Desktop agents | Termux Agent |
| --- | --- |
| Heavy TUI / Electron / Node | Single static Go binary, **no CGO**, no npm |
| Assumes always-on daemons | PPK-aware: timeouts, process groups, no watchers |
| Large context windows | Compaction, grep/glob caps, 64KiB read windows |
| Crash = lost chat | Auto-save sessions under `~/.config/termux-agent/` |

## Features

- **Full coding toolbelt** — `write_file`, `read_file` (offset/limit), `list_directory`, `apply_search_replace`, `delete_file`, `move_file`, `grep_files`, `glob_files`, `execute_command`
- **Enterprise workflow** — `todo_write` / `todo_read`, project `remember` / `recall`, plan mode (`/plan`)
- **Project awareness** — detects Go/Node/Rust/Python/Android Gradle/etc., loads `AGENTS.md` / `.termux-agent.md`
- **Providers** — OpenAI, OpenRouter, Anthropic-compat, Groq, Together, DeepSeek, Ollama, LM Studio, vLLM, custom OpenAI-compatible endpoints
- **Streaming** — SSE token streaming with automatic fallback for stubborn local servers
- **PPK recovery** — auto-saves conversation per working directory; resumes after Android kills the process
- **Safety** — path sandbox (cwd + `$HOME`, symlink-aware), dangerous-command prompts, atomic writes
- **Zero external deps** — Go standard library only

## Requirements

- Go 1.22+
- An OpenAI-compatible chat completions endpoint
- A model with **tool/function calling** (required for real builds)

## Build

### Cross-compile from desktop

```bash
env GOOS=android GOARCH=arm64 CGO_ENABLED=0 go build -ldflags="-s -w" -o termux-agent .
```

### Build directly in Termux

```bash
pkg install golang git
go build -ldflags="-s -w" -o termux-agent .
```

### Install

```bash
cp termux-agent $PREFIX/bin/
```

## Run tests

```bash
go test ./...
```

## Quick start

```bash
./termux-agent
```

```text
❯ /provider openrouter
❯ /key sk-or-v1-...
❯ /model anthropic/claude-sonnet-4
❯ build a Go REST API with chi router, SQLite, and docker-compose — then run the tests
```

Local Ollama:

```text
❯ /provider ollama
❯ /model qwen2.5-coder:7b
```

## Slash commands

| Command | Description |
| --- | --- |
| `/help` | Show commands |
| `/provider [name]` | Switch provider (inline or interactive) |
| `/model [name\|n]` | Set model (any id; presets are suggestions) |
| `/endpoint [url]` | Custom chat completions URL |
| `/key [secret]` | Set API key |
| `/status` | Config + session size |
| `/settings …` | `tokens`, `budget`, `timeout`, `turns`, `stream`, `approve`, `save` |
| `/plan [on\|off]` | Read-only planning mode |
| `/stream [on\|off]` | Token streaming |
| `/approve [on\|off]` | Skip dangerous-command prompts |
| `/clear` | Clear conversation |
| `/compact` | Force history compaction |
| `/save [name]` `/load [name]` | Named sessions |
| `/sessions` | List sessions |
| `/todos` `/memory` `/project` | Inspect agent state |
| `/reset` | Wipe config (clears API key) |
| `/exit` | Quit |

Inline args work: `/provider ollama`, `/model 1`, `/settings turns 120`, `/stream off`.

## Environment variables

| Variable | Effect |
| --- | --- |
| `OPENAI_API_KEY` / `TERMUX_AGENT_API_KEY` | API key (used when no config file yet) |
| `OPENAI_API_ENDPOINT` / `TERMUX_AGENT_ENDPOINT` | Custom endpoint |
| `TERMUX_AGENT_MODEL` | Default model |

Config file: `~/.config/termux-agent/config.json` (mode `0600`).

Project-local state (todos/memory): `./.termux-agent/`.

## Architecture

| File | Purpose |
| --- | --- |
| `main.go` | REPL, slash commands, Ctrl+C cancel, session resume |
| `agent.go` | Tool loop, compaction, system prompt, plan mode |
| `provider.go` | OpenAI JSON + SSE streaming, error unpacking |
| `config.go` | Persistent config + provider presets |
| `files.go` / `fsops.go` / `patch.go` | Sandboxed FS + atomic search/replace |
| `search.go` | Pure-Go grep + glob |
| `shell.go` (+ unix/generic) | Termux shell, timeouts, process-group kill |
| `session.go` | PPK-safe conversation persistence |
| `todo.go` | Todos + project memory |
| `project.go` | Stack detection + `AGENTS.md` |
| `approve.go` | Dangerous command heuristics |
| `ui.go` | Low-refresh spinner + tool activity lines |

## Building large apps (how the agent thinks)

1. **Plan** (`/plan on`) — explore with grep/glob/read, write todos, no mutations  
2. **Act** (`/plan off`) — `todo_write` milestones, then `write_file` one file at a time  
3. **Verify** — `execute_command` for `go test`, `npm test`, etc. (keep commands short)  
4. **Survive PPK** — keep the session foregrounded; auto-save restores history after a kill  
5. **Continue** — if truncated, say `continue`; todos + memory keep continuity  

Drop an `AGENTS.md` in the repo root for project-specific rules (same idea as Crush / Claude).

## Security notes

- Filesystem tools only touch **cwd** or **`$HOME`** (symlink-resolved), with atomic writes.
- `execute_command` can do anything your Termux user can. Use trusted models; keep `/approve` off unless you know what you're doing.
- API keys are stored in plaintext config (`0600`). Protect your device unlock.

## Phantom Process Killer tips

- Keep Termux in the foreground during long builds.
- Default shell timeout is 60s (`/settings timeout 180` for heavy compiles).
- Prefer targeted commands over `find /` or unbounded watches.
- Session auto-save means a kill loses the process, not the conversation.

## License

MIT — see [LICENSE](LICENSE).
