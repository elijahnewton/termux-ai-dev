package main

import (
    "context"
    "encoding/json"
    "errors"
    "fmt"
    "net/http"
    "strings"
    "sync"
    "time"
)

const defaultSystemPrompt = `You are TermuxAgent, a full agentic software development environment running inside Android Termux. You build real, runnable projects directly on the filesystem — you do not describe or paste code in chat, you create it on disk.

ENVIRONMENT CONSTRAINTS:
1. Android Phantom Process Killer (PPK): Termux background processes are aggressively terminated by Android after a few minutes. Never spawn long-running daemons, background jobs, or watchers. Every shell command must be targeted, synchronous, and fast.
2. Network & Memory: mobile data is metered and RAM is limited. Keep your prose short; spend your output budget on files and commands, not commentary.
3. Provider Agnostic: you're connected to an OpenAI-compatible API endpoint (OpenAI, OpenRouter, Groq, Together AI, a local model, or similar). Don't assume capabilities beyond standard tool calling and chat completions.

HOW YOU BUILD PROJECTS — this is the most important part of your job:
- Never paste a full file's contents into your chat reply. Every file you create or change must go through a tool call, so it actually lands in a real project folder the user can cd into, run, and commit to git.
- Use write_file to create every new file. It auto-creates missing parent directories, so writing to "myapp/src/index.html" scaffolds "myapp/src/" too. One write_file call per file — never try to cram a whole multi-file app into one call or one response.
- Use apply_search_replace only for a small, targeted edit to a file that already exists. Call read_file first if you aren't certain of its exact current content — never guess at search text.
- Use list_directory to confirm what's actually on disk, especially after scaffolding a project or before editing one you didn't just create.
- A single response has a limited output budget and cannot fit an entire real-world app. For anything more than a trivial script: first decide the file list, then create files one by one across as many tool calls and turns as it takes. If a single file would be unusually large, split it into smaller modules instead of trying to force it into one response.
- Once everything is written, verify with list_directory, then give the user a short summary plus the exact command to run or open it (e.g. "python3 -m http.server 8000" for a static site, "node index.js", "termux-open index.html").
- Shell commands (execute_command) must use strict timeouts, be non-blocking where possible, and target specific files — avoid broad searches like "find / -type f". If tool output is large, summarize it rather than echoing it verbatim.
- Fallback text format: if a provider ever drops tool-calling mid-conversation, plain text in exactly these forms is auto-applied to disk instead of just sitting in chat:
  New or fully-rewritten file:
    ### path/to/file
    ` + "```" + `
    full file content
    ` + "```" + `
  Small edit to an existing file:
    ### path/to/file
    <<<<<<< SEARCH
    exact old lines
    =======
    exact new lines
    >>>>>>> REPLACE

You have access to:
- write_file: create or fully overwrite a file (auto-creates parent directories).
- read_file: read a file's real current content.
- list_directory: see the real on-disk project layout.
- apply_search_replace: apply a small, exact patch to an existing file.
- execute_command: run a shell command in Termux (installs, builds, tests, git, etc).`

type AgentConfig struct {
    APIKey        string
    Endpoint      string
    Model         string
    MaxTokens     int
    HistoryBudget int
    ShellTimeout  time.Duration
    MaxTurns      int
    ExtraHeaders  map[string]string
}

type Agent struct {
    cfg     AgentConfig
    client  *http.Client
    mu      sync.Mutex
    history []Message
    tools   []Tool
}

func NewAgent(cfg AgentConfig) *Agent {
    tools := []Tool{
        {
            Type: "function",
            Function: ToolDefinition{
                Name:        "write_file",
                Description: "Create a new file, or fully overwrite an existing one, with the given content. Automatically creates any missing parent directories, so this is also how new project folders get scaffolded. Use one call per file; use apply_search_replace instead for a small edit to a file that should otherwise be left alone.",
                Parameters: map[string]interface{}{
                    "type": "object",
                    "properties": map[string]interface{}{
                        "path": map[string]interface{}{
                            "type":        "string",
                            "description": "Relative (to the current directory) or absolute file path, e.g. 'checkers/index.html'.",
                        },
                        "content": map[string]interface{}{
                            "type":        "string",
                            "description": "The full content to write to the file.",
                        },
                    },
                    "required": []string{"path", "content"},
                },
            },
        },
        {
            Type: "function",
            Function: ToolDefinition{
                Name:        "read_file",
                Description: "Read a file's real current on-disk content, so an edit or apply_search_replace patch can be based on what's actually there instead of assumption.",
                Parameters: map[string]interface{}{
                    "type": "object",
                    "properties": map[string]interface{}{
                        "path": map[string]interface{}{
                            "type":        "string",
                            "description": "Relative or absolute file path.",
                        },
                    },
                    "required": []string{"path"},
                },
            },
        },
        {
            Type: "function",
            Function: ToolDefinition{
                Name:        "list_directory",
                Description: "Recursively list files and folders under a directory (skipping .git, node_modules, and vendor) to confirm the real project layout on disk.",
                Parameters: map[string]interface{}{
                    "type": "object",
                    "properties": map[string]interface{}{
                        "path": map[string]interface{}{
                            "type":        "string",
                            "description": "Directory to list. Defaults to the current directory if omitted.",
                        },
                    },
                },
            },
        },
        {
            Type: "function",
            Function: ToolDefinition{
                Name:        "execute_command",
                Description: "Execute a shell command inside Termux. Commands must be fast, targeted, and avoid background processes due to Android Phantom Process Killer.",
                Parameters: map[string]interface{}{
                    "type": "object",
                    "properties": map[string]interface{}{
                        "command": map[string]interface{}{
                            "type":        "string",
                            "description": "The shell command to run.",
                        },
                    },
                    "required": []string{"command"},
                },
            },
        },
        {
            Type: "function",
            Function: ToolDefinition{
                Name:        "apply_search_replace",
                Description: "Apply a line-based search/replace patch to a file. Use this instead of rewriting entire files.",
                Parameters: map[string]interface{}{
                    "type": "object",
                    "properties": map[string]interface{}{
                        "path": map[string]interface{}{
                            "type":        "string",
                            "description": "Relative or absolute file path.",
                        },
                        "search": map[string]interface{}{
                            "type":        "string",
                            "description": "Exact lines to search for.",
                        },
                        "replace": map[string]interface{}{
                            "type":        "string",
                            "description": "Exact lines to replace with.",
                        },
                    },
                    "required": []string{"path", "search", "replace"},
                },
            },
        },
    }

    return &Agent{
        cfg: cfg,
        // Per-request ceiling; main caps each turn at 3 minutes. Matched so
        // slow local (Ollama) generations aren't cut short prematurely.
        client:  &http.Client{Timeout: 180 * time.Second},
        history: make([]Message, 0),
        tools:   tools,
    }
}

func (a *Agent) RunTurn(ctx context.Context, userText string) (string, error) {
    a.mu.Lock()
    defer a.mu.Unlock()

    a.history = append(a.history, Message{Role: "user", Content: userText})
    a.compactHistory()

    maxTurns := a.cfg.MaxTurns
    if maxTurns <= 0 {
        maxTurns = 10
    }

    for turn := 1; turn <= maxTurns; turn++ {
        reqPayload := ChatCompletionRequest{
            Model:     a.cfg.Model,
            Messages:  a.buildMessages(),
            Tools:     a.tools,
            MaxTokens: a.cfg.MaxTokens,
        }

        spinner := NewSpinner(fmt.Sprintf("Thinking... (turn %d/%d)", turn, maxTurns))
        resp, err := a.doRequest(ctx, reqPayload)
        spinner.Stop()
        if err != nil {
            return "", fmt.Errorf("LLM request failed: %w", err)
        }
        if len(resp.Choices) == 0 {
            return "", errors.New("empty response from LLM")
        }

        choice := resp.Choices[0]
        assistantMsg := choice.Message
        sanitizeToolCalls(&assistantMsg)
        a.history = append(a.history, assistantMsg)

        if len(assistantMsg.ToolCalls) == 0 {
            final := assistantMsg.Content
            // The system prompt invites plain-text SEARCH/REPLACE blocks as a
            // fallback; apply them so those edits aren't silently dropped.
            if notes := a.applyExtractedEdits(final); notes != "" {
                final += "\n\n" + notes
            }
            if choice.FinishReason == "length" {
                final += "\n\n(response truncated: max_tokens reached)"
            }
            return final, nil
        }

        for _, tc := range assistantMsg.ToolCalls {
            a.history = append(a.history, a.executeTool(ctx, tc))
        }
        a.compactHistory()
    }

    return "", fmt.Errorf("max tool turns (%d) exceeded without final answer", maxTurns)
}

// sanitizeToolCalls repairs tool_calls emitted by non-conformant providers
// before they are stored in history and later replayed verbatim in a
// subsequent request. Two failure modes are handled defensively:
//
//  1. Empty, null-derived, or truncated "arguments" strings. Per the
//     OpenAI tool-calling contract, function.arguments must always be a
//     string that itself parses as a JSON object. Some provider/model
//     combinations (seen via OpenRouter and some local models) instead
//     send "" or a JSON null for arguments — which Go's decoder silently
//     turns into "" on a string field, with no unmarshal error — or they
//     truncate mid-object when generation is cut short. Replaying that
//     string on the next turn is exactly what produces errors like:
//       "tool arguments must be a stringified JSON object"
//     Repairing it to "{}" instead lets executeTool's own required-field
//     checks (e.g. "Error: missing command") surface a normal, recoverable
//     tool result that the model can react to, instead of a hard,
//     unrecoverable request failure that kills the whole turn.
//  2. A missing/empty tool_call id. Several OpenAI-compatible providers
//     require a non-empty, unique id on every tool call and reject a
//     replayed empty one outright.
func sanitizeToolCalls(msg *Message) {
    for i := range msg.ToolCalls {
        tc := &msg.ToolCalls[i]
        if tc.Type == "" {
            tc.Type = "function"
        }
        if strings.TrimSpace(tc.ID) == "" {
            tc.ID = fmt.Sprintf("call_repaired_%d_%d", time.Now().UnixNano(), i)
        }
        args := strings.TrimSpace(tc.Function.Arguments)
        var probe map[string]interface{}
        // A JSON literal "null" unmarshals into a nil map with no error, so
        // it needs its own check alongside the empty-string and malformed/
        // truncated cases that Unmarshal does reject.
        if err := json.Unmarshal([]byte(args), &probe); err != nil || probe == nil {
            tc.Function.Arguments = "{}"
        }
    }
}

// applyExtractedEdits applies "### path" SEARCH/REPLACE patches and
// "### path" + fenced-code file blocks that the model emitted as plain
// text instead of via apply_search_replace/write_file (a fallback for
// providers that occasionally drop tool-calling mid-conversation).
// Returns a human-readable summary, or "" if nothing applicable was found.
func (a *Agent) applyExtractedEdits(text string) string {
    var b strings.Builder
    applied, failed := 0, 0

    for _, p := range ExtractPatches(text) {
        // Require a single-token path to skip examples the model echoes.
        if p.File == "" || len(strings.Fields(p.File)) != 1 {
            continue
        }
        if err := ApplySearchReplace(p.File, p.Search, p.Replace); err != nil {
            failed++
            fmt.Fprintf(&b, "patch to %s failed: %v\n", p.File, err)
        } else {
            applied++
            fmt.Fprintf(&b, "patched %s\n", p.File)
        }
    }

    for _, fb := range ExtractFileBlocks(text) {
        if _, err := WriteFile(fb.File, fb.Content); err != nil {
            failed++
            fmt.Fprintf(&b, "write %s failed: %v\n", fb.File, err)
        } else {
            applied++
            fmt.Fprintf(&b, "wrote %s\n", fb.File)
        }
    }

    if applied == 0 && failed == 0 {
        return ""
    }
    return fmt.Sprintf("[auto-applied %d edit(s), %d failed]\n%s", applied, failed, b.String())
}

func (a *Agent) buildMessages() []Message {
    sys := Message{Role: "system", Content: defaultSystemPrompt}
    out := make([]Message, 0, len(a.history)+1)
    out = append(out, sys)
    out = append(out, a.history...)
    return out
}

// executeTool runs one tool call, showing a spinner while it's in flight
// and then printing a one-line result to the terminal — so the person
// watching sees files actually being written and commands actually being
// run, not just silence until a chat reply appears.
func (a *Agent) executeTool(ctx context.Context, tc ToolCall) Message {
    spinner := NewSpinner(fmt.Sprintf("Running %s...", tc.Function.Name))
    result, detail, ok, note := a.runTool(ctx, tc)
    spinner.Stop()
    printToolActivity(tc.Function.Name, detail, ok, note)

    if estimateTokens(result) > 800 {
        result = truncateLines(result, 30) + "\n... (output truncated by agent)"
    }

    return Message{
        Role:       "tool",
        ToolCallID: tc.ID,
        Content:    result,
    }
}

// runTool executes one tool call and returns: the full result to send back
// to the model, a short "detail" identifying what it acted on (a path or a
// command) for the console line, whether it succeeded, and a short
// human-readable outcome note for that same line.
func (a *Agent) runTool(ctx context.Context, tc ToolCall) (result, detail string, ok bool, note string) {
    switch tc.Function.Name {
    case "execute_command":
        var args struct {
            Command string `json:"command"`
        }
        if err := json.Unmarshal([]byte(tc.Function.Arguments), &args); err != nil {
            return fmt.Sprintf("Error parsing arguments: %v", err), "", false, err.Error()
        }
        detail = args.Command
        if args.Command == "" {
            return "Error: missing command", detail, false, "missing command"
        }
        out, err := ExecuteCommand(ctx, args.Command, a.cfg.ShellTimeout)
        if err != nil {
            return fmt.Sprintf("Error: %v\nOutput: %s", err, out), detail, false, err.Error()
        }
        return compressContent(out), detail, true, ""

    case "write_file":
        var args struct {
            Path    string `json:"path"`
            Content string `json:"content"`
        }
        if err := json.Unmarshal([]byte(tc.Function.Arguments), &args); err != nil {
            return fmt.Sprintf("Error parsing arguments: %v", err), "", false, err.Error()
        }
        detail = args.Path
        if args.Path == "" {
            return "Error: missing path", detail, false, "missing path"
        }
        msg, err := WriteFile(args.Path, args.Content)
        if err != nil {
            return fmt.Sprintf("Error writing file: %v", err), detail, false, err.Error()
        }
        return msg, detail, true, fmt.Sprintf("(%d bytes)", len(args.Content))

    case "read_file":
        var args struct {
            Path string `json:"path"`
        }
        if err := json.Unmarshal([]byte(tc.Function.Arguments), &args); err != nil {
            return fmt.Sprintf("Error parsing arguments: %v", err), "", false, err.Error()
        }
        detail = args.Path
        if args.Path == "" {
            return "Error: missing path", detail, false, "missing path"
        }
        content, err := ReadFile(args.Path)
        if err != nil {
            return fmt.Sprintf("Error reading file: %v", err), detail, false, err.Error()
        }
        return content, detail, true, ""

    case "list_directory":
        var args struct {
            Path string `json:"path"`
        }
        _ = json.Unmarshal([]byte(tc.Function.Arguments), &args)
        detail = args.Path
        if detail == "" {
            detail = "."
        }
        listing, err := ListDirectory(args.Path)
        if err != nil {
            return fmt.Sprintf("Error listing directory: %v", err), detail, false, err.Error()
        }
        return listing, detail, true, ""

    case "apply_search_replace":
        var args struct {
            Path    string `json:"path"`
            Search  string `json:"search"`
            Replace string `json:"replace"`
        }
        if err := json.Unmarshal([]byte(tc.Function.Arguments), &args); err != nil {
            return fmt.Sprintf("Error parsing arguments: %v", err), "", false, err.Error()
        }
        detail = args.Path
        if args.Path == "" {
            return "Error: missing path", detail, false, "missing path"
        }
        if err := ApplySearchReplace(args.Path, args.Search, args.Replace); err != nil {
            return fmt.Sprintf("Error applying patch: %v", err), detail, false, err.Error()
        }
        return "Patch applied successfully.", detail, true, ""

    default:
        return fmt.Sprintf("Error: unknown tool %s", tc.Function.Name), tc.Function.Name, false, "unknown tool"
    }
}

func (a *Agent) compactHistory() {
    budget := a.cfg.HistoryBudget
    if budget <= 0 {
        budget = 4000
    }

    // Compress oversized content. Never touch the newest message: it is
    // either the user's current input or a tool result we're about to use.
    for i := 0; i < len(a.history)-1; i++ {
        a.history[i].Content = compressContent(a.history[i].Content)
    }

    // Evict oldest assistant+tool-result pairs first (most expensive),
    // then plain oldest messages, until the budget fits.
    for estimateMessages(a.history) > budget && len(a.history) > 4 {
        if !a.removeOldestToolPair() {
            a.history = a.history[1:]
        }
    }

    // Last resort: hard-truncate everything except the newest message.
    if estimateMessages(a.history) > budget {
        for i := 0; i < len(a.history)-1; i++ {
            if len(a.history[i].Content) > 200 {
                a.history[i].Content = a.history[i].Content[:200] + "...[truncated]"
            }
        }
    }
}

// removeOldestToolPair collapses the oldest assistant-with-tool-calls message
// and its following tool results into a single summary message, preserving
// the API's tool_calls/tool pairing invariant. Reports whether it removed one.
func (a *Agent) removeOldestToolPair() bool {
    for i := 0; i < len(a.history)-1; i++ {
        if a.history[i].Role == "assistant" && len(a.history[i].ToolCalls) > 0 {
            j := i + 1
            for j < len(a.history) && a.history[j].Role == "tool" {
                j++
            }
            if j > i+1 {
                a.history[i].Content = fmt.Sprintf("<executed %d tool calls>", j-i-1)
                a.history[i].ToolCalls = nil
                a.history = append(a.history[:i+1], a.history[j:]...)
                return true
            }
        }
    }
    return false
}

func estimateTokens(text string) int {
    if text == "" {
        return 0
    }
    n := len(text) / 4
    if n < 1 {
        return 1
    }
    return n
}

func estimateMessages(msgs []Message) int {
    total := 0
    for _, m := range msgs {
        total += estimateTokens(m.Content)
        for _, tc := range m.ToolCalls {
            total += estimateTokens(tc.Function.Name)
            total += estimateTokens(tc.Function.Arguments)
        }
    }
    return total
}

func compressContent(text string) string {
    if text == "" {
        return text
    }
    lines := strings.Split(text, "\n")
    if len(lines) > 40 {
        dirLike := 0
        for _, l := range lines {
            if strings.Contains(l, "/") || strings.Contains(l, ".go") || strings.Contains(l, ".md") || strings.Contains(l, ".txt") {
                dirLike++
            }
        }
        if dirLike > len(lines)/3 {
            head := strings.Join(lines[:15], "\n")
            return fmt.Sprintf("<listing: %d lines>\n%s\n... (%d more items)", len(lines), head, len(lines)-15)
        }

        if isRepetitive(text) {
            return "<repetitive output compressed>\n" + uniqueLines(text)
        }
    }
    return text
}

func isRepetitive(text string) bool {
    lines := strings.Split(text, "\n")
    if len(lines) < 10 {
        return false
    }
    counts := make(map[string]int)
    for _, l := range lines {
        trimmed := strings.TrimSpace(l)
        if trimmed == "" {
            continue
        }
        counts[trimmed]++
    }
    for _, c := range counts {
        if c > len(lines)/3 {
            return true
        }
    }
    return false
}

func uniqueLines(text string) string {
    lines := strings.Split(text, "\n")
    seen := make(map[string]bool)
    var out []string
    for _, l := range lines {
        trimmed := strings.TrimSpace(l)
        if !seen[trimmed] {
            seen[trimmed] = true
            out = append(out, l)
        }
    }
    if len(out) > 20 {
        out = out[:20]
        out = append(out, "...")
    }
    return strings.Join(out, "\n")
}

func truncateLines(text string, max int) string {
    lines := strings.Split(text, "\n")
    if len(lines) <= max {
        return text
    }
    return strings.Join(lines[:max], "\n")
}