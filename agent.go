package main

import (
    "context"
    "encoding/json"
    "errors"
    "fmt"
    "net/http"
    "strconv"
    "strings"
    "sync"
    "time"
)

const defaultSystemPrompt = `You are TermuxAgent, an expert systems programming assistant operating inside Android Termux. Your environment is severely resource-constrained.

CRITICAL CONSTRAINTS:
1. Android Phantom Process Killer (PPK): Termux background processes are aggressively terminated by Android after a few minutes. Never spawn long-running daemons, background jobs, or watchers. Every command must be targeted, synchronous, and fast.
2. Network & Memory: Mobile data is metered and RAM is limited. Keep responses concise. Avoid redundant tool calls.
3. File Editing: You MUST use the apply_search_replace tool to edit files. Never rewrite an entire file unless it is new or trivially small (<20 lines).
4. Shell Commands: All execute_command calls must use strict timeouts, non-blocking flags where possible, and target specific files. Avoid broad searches like "find / -type f".
5. Output Compaction: If tool output is large, summarize it for the user. Do not echo raw hex dumps or long build logs verbatim unless asked.
6. Provider Agnostic: You are connected to an OpenAI-compatible API endpoint. The provider may be OpenAI, OpenRouter, Groq, Together AI, a local LLM, or any other compatible service. Do not assume provider-specific capabilities beyond standard tool calling and chat completions.

When editing files, prefer this exact format in your thoughts or when not using tools:
### path/to/file
<<<<<<< SEARCH
exact old lines
=======
exact new lines
>>>>>>> REPLACE

You have access to:
- execute_command: Run a shell command in Termux.
- apply_search_replace: Apply a search/replace patch to a file.`

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
        assistantMsg.ToolCalls = normalizeToolCalls(assistantMsg.ToolCalls)
        a.history = append(a.history, assistantMsg)

        if len(assistantMsg.ToolCalls) == 0 {
            final := assistantMsg.Content
            // The system prompt invites plain-text SEARCH/REPLACE blocks as a
            // fallback; apply them so those edits aren't silently dropped.
            if notes := a.applyExtractedPatches(final); notes != "" {
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

func normalizeToolCalls(calls []ToolCall) []ToolCall {
    if len(calls) == 0 {
    	return nil
    }
    out := make([]ToolCall, 0, len(calls))
    for _, tc := range calls {
    	tc.Function.Arguments = normalizeToolCallArguments(tc.Function.Name, tc.Function.Arguments)
    	out = append(out, tc)
    }
    return out
}

func normalizeToolCallArguments(toolName, raw string) string {
    raw = strings.TrimSpace(raw)
    if raw == "" {
    	return "{}"
    }

    var decoded interface{}
    if err := json.Unmarshal([]byte(raw), &decoded); err == nil {
    	switch v := decoded.(type) {
    	case map[string]interface{}:
    		b, err := json.Marshal(v)
    		if err == nil {
    			return string(b)
    		}
    	case string:
    		if toolName == "execute_command" && strings.TrimSpace(v) != "" {
    			return `{"command":` + strconv.Quote(v) + `}`
    		}
    	default:
    		b, err := json.Marshal(map[string]interface{}{"value": v})
    		if err == nil {
    			return string(b)
    		}
    	}
    }

    if toolName == "execute_command" {
    	return `{"command":` + strconv.Quote(raw) + `}`
    }
    return `{"_raw":` + strconv.Quote(raw) + `}`
}

// applyExtractedPatches applies ### path SEARCH/REPLACE blocks that the
// model emitted in its final text instead of via the tool. Returns a
// human-readable summary, or "" if no applicable patches were found.
func (a *Agent) applyExtractedPatches(text string) string {
    patches := ExtractPatches(text)
    if len(patches) == 0 {
        return ""
    }
    var b strings.Builder
    applied, failed := 0, 0
    for _, p := range patches {
        // Require a single-token path to skip examples the model echoes.
        if p.File == "" || len(strings.Fields(p.File)) != 1 {
            continue
        }
        if err := ApplySearchReplace(p.File, p.Search, p.Replace); err != nil {
            failed++
            fmt.Fprintf(&b, "patch to %s failed: %v\n", p.File, err)
        } else {
            applied++
        }
    }
    if applied == 0 && failed == 0 {
        return ""
    }
    return fmt.Sprintf("[auto-applied %d patch(es), %d failed]\n%s", applied, failed, b.String())
}

func (a *Agent) buildMessages() []Message {
    sys := Message{Role: "system", Content: defaultSystemPrompt}
    out := make([]Message, 0, len(a.history)+1)
    out = append(out, sys)
    out = append(out, a.history...)
    return out
}

func (a *Agent) executeTool(ctx context.Context, tc ToolCall) Message {
    var result string
    switch tc.Function.Name {
    case "execute_command":
        var args struct {
            Command string `json:"command"`
        }
        if err := json.Unmarshal([]byte(tc.Function.Arguments), &args); err != nil {
            result = fmt.Sprintf("Error parsing arguments: %v", err)
        } else if args.Command == "" {
            result = "Error: missing command"
        } else {
            out, err := ExecuteCommand(ctx, args.Command, a.cfg.ShellTimeout)
            if err != nil {
                result = fmt.Sprintf("Error: %v\nOutput: %s", err, out)
            } else {
                result = out
            }
            result = compressContent(result)
        }
    case "apply_search_replace":
        var args struct {
            Path    string `json:"path"`
            Search  string `json:"search"`
            Replace string `json:"replace"`
        }
        if err := json.Unmarshal([]byte(tc.Function.Arguments), &args); err != nil {
            result = fmt.Sprintf("Error parsing arguments: %v", err)
        } else if args.Path == "" {
            result = "Error: missing path"
        } else {
            if err := ApplySearchReplace(args.Path, args.Search, args.Replace); err != nil {
                result = fmt.Sprintf("Error applying patch: %v", err)
            } else {
                result = "Patch applied successfully."
            }
        }
    default:
        result = fmt.Sprintf("Error: unknown tool %s", tc.Function.Name)
    }

    if estimateTokens(result) > 800 {
        result = truncateLines(result, 30) + "\n... (output truncated by agent)"
    }

    return Message{
        Role:       "tool",
        ToolCallID: tc.ID,
        Content:    result,
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