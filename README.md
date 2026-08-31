Termux Agent
A native Go CLI agent for Android Termux, optimized for severe mobile constraints: Phantom Process Killer awareness, aggressive token pruning, strict search/replace patching, and a minimal battery-friendly UI.

Features
Interactive provider switching: Change providers, models, endpoints, API keys, and settings on the fly using slash commands — no process restart needed. (Note: switching recreates the agent, so conversation history is cleared. Settings persist.)
Provider presets + universal compatibility: First-class presets for OpenAI, OpenRouter, Groq, Together AI, Ollama, LM Studio, and vLLM. Any other OpenAI-compatible server (llama.cpp's llama-server, tabbyAPI, proxies, …) works via /endpoint.
Provider-specific header support: Custom HTTP headers for providers like OpenRouter (HTTP-Referer, X-Title).
Persistent configuration: Settings are saved to ~/.config/termux-agent/config.json (mode 0600) and survive restarts. Conversation history is per-session only.
Aggressive token pruning: Iterative history compaction that collapses completed tool-call blocks, compresses directory listings, and deduplicates repetitive output.
Search/replace patching: Exact-match, line-based patching so the agent edits files without rewriting them over expensive mobile data. Patches are confined to the working directory and $HOME (symlink-resolved) and applied via atomic write.
PPK-aware execution: The system prompt warns the LLM about Android's Phantom Process Killer; commands run in their own process group with a default 15-second timeout and SIGKILL on expiry.
Zero external dependencies: Pure Go standard library; cross-compiles to android/arm64 without CGO.
Requirements
Go 1.22+
An OpenAI-compatible chat completions endpoint
A model with tool/function calling support (required for execute_command and apply_search_replace — small local models often lack this)
Build
Cross-compile from desktop
env GOOS=android GOARCH=arm64 CGO_ENABLED=0 go build -ldflags="-s -w" -o termux-agent .
Build directly in Termux
pkg install golang gitgo build -ldflags="-s -w" -o termux-agent .
Run tests
go test ./...
Usage
./termux-agent
The banner and command list print on startup. All slash commands are interactive — they prompt for input rather than accepting inline arguments.

Slash commands
Command	Description
/help, /?	Show available commands
/provider	Choose from OpenAI, OpenRouter, Groq, Together AI, Ollama, LM Studio, vLLM, or Custom
/model	Set or change the model (suggests provider defaults; accepts a number or any name)
/endpoint	Set a custom API endpoint URL
/key	Set or update the API key
/status	Show current configuration
/settings	Adjust max-tokens, history-budget, shell-timeout, max-turns
/header	Add or remove custom HTTP headers
/reset	Reset configuration to defaults (clears the stored API key)
/exit, /quit	Leave
Example session
 $ ./termux-agent  Termux Agent  Interactive AI assistant for Android TermuxSlash commands:  /help, /?           Show this help  /provider           Choose a provider (OpenAI, OpenRouter, Groq, Ollama, etc.)  ...> /providerAvailable providers:  1. custom       Custom Endpoint  2. groq         Groq [requires API key]  3. lmstudio     LM Studio (Local)  4. ollama       Ollama (Local)  5. openai       OpenAI [requires API key]  6. openrouter   OpenRouter [requires API key]  7. together     Together AI [requires API key]  8. vllm         vLLM (Local)Choose provider (name or number): openrouterProvider set to OpenRouter.Endpoint: https://openrouter.ai/api/v1/chat/completionsDefault model: anthropic/claude-sonnet-4This provider requires an API key. Use /key to set one.> /keyCurrent API key: (not set)New API key (empty to keep current): sk-or-v1-...API key updated.> /modelSuggested models for this provider:  * 1. anthropic/claude-sonnet-4    2. openai/gpt-4o-mini    3. meta-llama/llama-3.3-70b-instruct    4. google/gemini-2.0-flash-001Or type any model name manually.Current model: anthropic/claude-sonnet-4New model: 2Model set to openai/gpt-4o-mini.> /statusProvider: openrouterEndpoint: https://openrouter.ai/api/v1/chat/completionsModel: openai/gpt-4o-miniAPI Key: (set)Max Tokens: 1024History Budget: 8000Shell Timeout: 15sMax Turns: 10> write a hello world in go and run it
Local LLM (Ollama, LM Studio, vLLM, llama.cpp)
> /providerChoose provider (name or number): ollama> /modelNew model: qwen2.5-coder:7b
For llama.cpp or anything without a preset, use /provider → custom (or just /endpoint) and point it at your server's /v1/chat/completions.

Note: the local model must support tool calling, or the agent can't run commands or edit files.

Groq
> /providerChoose provider (name or number): groq> /keyNew API key (empty to keep current): gsk_...> /modelSuggested models for this provider:  * 1. llama-3.3-70b-versatile    2. llama-3.1-8b-instant    3. qwen/qwen3-32bNew model: 1
Architecture
File	Purpose
main.go	Interactive REPL, shared stdin reader, slash command dispatch
config.go	Persistent user config, provider presets, live config updates
agent.go	Agent turn loop, history compaction, tool dispatch, system prompt, plain-text patch fallback
shell.go / shell_unix.go / shell_generic.go	Termux shell resolver, timeout execution, process-group killing
patch.go	Exact search/replace with path sandbox and atomic write; SEARCH/REPLACE block extraction
provider.go	OpenAI-compatible request/response translation, header injection, bounded response reads
ui.go	Minimal ANSI spinner with low refresh rate
Security Notes
apply_search_replace only touches files under the current working directory or $HOME, with symlinks resolved before the check, and writes atomically (temp file + rename) so a crash can't corrupt the target.
execute_command runs in a new process group and is killed with SIGKILL on timeout; a short WaitDelay prevents daemonized children holding the output pipe from hanging the turn.
execute_command is arbitrary shell execution with no allowlist. The model can do anything your Termux user can — delete files, install packages, make network requests. Only use it with models/providers you trust, and be deliberate about the directory you launch it from.
The API key is stored in plaintext at ~/.config/termux-agent/config.json (mode 0600). Anyone or anything that can read that file has your key.
Changing provider, model, or settings recreates the agent and clears the conversation; /reset additionally wipes the stored API key.
Phantom Process Killer tips
Keep the agent in the foreground; backgrounding long sessions invites Android to kill it.
The 15-second default shell-timeout is deliberate — raise it via /settings for slow builds, but prefer targeted commands over broad scans.
Two things you may want to decide on:

Inline arguments. If you'd rather have /provider ollama, /model x, /key y actually work (falling back to the interactive prompt when no arg is given), that's a ~20-line change to handleSlash + the interactive functions in main.go — say the word and I'll provide it. The README above documents the current interactive-only behavior either way.
Model IDs drift. Consider adding a line to the README telling users the presets are best-effort defaults and any model name can be typed manually at /model — it's already implied, but stating it head-of-line saves bug reports when providers deprecate models again.