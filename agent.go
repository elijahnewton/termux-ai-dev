package main

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"net/http"
	"os"
	"strings"
	"sync"
	"time"
)

const defaultSystemPrompt = `You are TermuxAgent — a world-class agentic software engineer that runs natively inside Android Termux. You compete with desktop coding agents (Claude Code, Crush, Codex, Cline) but you are optimized for phones: metered data, limited RAM, and Android's Phantom Process Killer.

IDENTITY & GOAL:
- You BUILD real, runnable projects on disk. You do not paste large code blocks into chat as the deliverable.
- You can scaffold and iterate large enterprise applications: APIs, web apps, CLIs, mobile backends, data pipelines, monorepos — across as many tool turns as needed.
- Prefer small, correct, verified steps over giant speculative dumps.

ENVIRONMENT CONSTRAINTS (CRITICAL):
1. Phantom Process Killer (PPK): never spawn long-lived daemons, watchers, or background jobs. Prefer foreground, synchronous commands with timeouts.
2. Mobile: keep prose short. Spend tokens on tool calls (files/commands), not narration.
3. Provider-agnostic OpenAI-compatible tool calling. Do not assume vendor-specific features.

HOW YOU WORK ON BIG PROJECTS:
1. For non-trivial work: create a short todo list (todo_write) with concrete milestones, then execute them one by one, updating statuses.
2. Discover before editing: list_directory / glob_files / grep_files / read_file. Never invent file contents when you can read them.
3. Create files with write_file (one file per call; parents auto-created). Use apply_search_replace for small edits to existing files.
4. After scaffolding: verify with list_directory/glob_files, then run the minimal build/test command via execute_command.
5. Persist important project decisions with remember (architecture notes, ports, credentials locations — never store secrets themselves).
6. If truncated or blocked: continue next turn from todos/memory — do not restart from scratch.

TOOLS:
- write_file, read_file, list_directory, apply_search_replace
- delete_file, move_file
- grep_files, glob_files
- execute_command (Termux shell; PPK-aware timeouts)
- todo_write, todo_read
- remember, recall

PLAIN-TEXT FALLBACK (only if tool calling fails mid-conversation):
### path/to/file
` + "```" + `
full content
` + "```" + `

### path/to/file
<<<<<<< SEARCH
exact old lines
=======
exact new lines
>>>>>>> REPLACE

SAFETY:
- Stay inside the working directory / HOME sandbox enforced by tools.
- Prefer targeted rm of specific files over recursive deletes.
- Never print API keys or secrets into files or chat.`

const planModeExtra = `

PLAN MODE IS ON:
- Do NOT modify the filesystem or run mutating shell commands.
- You may use read-only tools: read_file, list_directory, grep_files, glob_files, todo_read, recall.
- Produce a concrete implementation plan with file list, order of work, risks, and verification commands.
- Use todo_write to capture the plan as pending items the user can later execute in agent mode.`

type AgentConfig struct {
	APIKey        string
	Endpoint      string
	Model         string
	MaxTokens     int
	HistoryBudget int
	ShellTimeout  time.Duration
	MaxTurns      int
	ExtraHeaders  map[string]string
	Stream        bool
	PlanMode      bool
	AutoApprove   bool
	AutoSave      bool
	Provider      string
}

type Agent struct {
	cfg          AgentConfig
	client       *http.Client
	mu           sync.Mutex
	history      []Message
	tools        []Tool
	todos        *TodoStore
	memory       *MemoryStore
	approver     Approver
	onStreamToken func(string)
	project      ProjectContext
}

func NewAgent(cfg AgentConfig) *Agent {
	a := &Agent{
		cfg: cfg,
		client: &http.Client{
			Timeout: 300 * time.Second,
		},
		history:  make([]Message, 0),
		todos:    NewTodoStore(),
		memory:   NewMemoryStore(),
		approver: DefaultCLIApprover,
		project:  DetectProject(),
	}
	a.tools = a.buildTools()
	a.onStreamToken = func(tok string) {
		fmt.Fprint(os.Stdout, tok)
	}
	return a
}

func (a *Agent) SetHistory(msgs []Message) {
	a.mu.Lock()
	defer a.mu.Unlock()
	a.history = append([]Message(nil), msgs...)
}

func (a *Agent) History() []Message {
	a.mu.Lock()
	defer a.mu.Unlock()
	out := make([]Message, len(a.history))
	copy(out, a.history)
	return out
}

func (a *Agent) ClearHistory() {
	a.mu.Lock()
	defer a.mu.Unlock()
	a.history = nil
}

func (a *Agent) SetPlanMode(on bool) {
	a.mu.Lock()
	defer a.mu.Unlock()
	a.cfg.PlanMode = on
	a.tools = a.buildTools()
}

func (a *Agent) PlanMode() bool {
	a.mu.Lock()
	defer a.mu.Unlock()
	return a.cfg.PlanMode
}

func (a *Agent) SetAutoApprove(on bool) {
	a.mu.Lock()
	defer a.mu.Unlock()
	a.cfg.AutoApprove = on
}

func (a *Agent) SnapshotSession(provider, model string) *Session {
	a.mu.Lock()
	defer a.mu.Unlock()
	s := NewEmptySession(provider, model)
	s.History = append([]Message(nil), a.history...)
	s.PlanMode = a.cfg.PlanMode
	if len(a.history) > 0 {
		for _, m := range a.history {
			if m.Role == "user" && m.Content != "" {
				title := m.Content
				if len(title) > 60 {
					title = title[:60] + "…"
				}
				s.Title = title
				break
			}
		}
	}
	return s
}

func (a *Agent) buildTools() []Tool {
	readTools := []Tool{
		toolDef("read_file", "Read a file's on-disk content. Optional offset (1-based line) and limit (line count) for large files.", map[string]interface{}{
			"type": "object",
			"properties": map[string]interface{}{
				"path":   map[string]interface{}{"type": "string", "description": "Relative or absolute file path."},
				"offset": map[string]interface{}{"type": "integer", "description": "Optional 1-based start line."},
				"limit":  map[string]interface{}{"type": "integer", "description": "Optional max number of lines."},
			},
			"required": []string{"path"},
		}),
		toolDef("list_directory", "Recursively list files/folders (skips .git, node_modules, vendor, etc.).", map[string]interface{}{
			"type": "object",
			"properties": map[string]interface{}{
				"path": map[string]interface{}{"type": "string", "description": "Directory to list. Defaults to cwd."},
			},
		}),
		toolDef("grep_files", "Search file contents under a directory for a literal string or regex. Returns path:line:text hits.", map[string]interface{}{
			"type": "object",
			"properties": map[string]interface{}{
				"pattern":  map[string]interface{}{"type": "string", "description": "Search pattern."},
				"path":     map[string]interface{}{"type": "string", "description": "Root directory or file. Defaults to cwd."},
				"regex":    map[string]interface{}{"type": "boolean", "description": "If true, treat pattern as regex."},
				"max_hits": map[string]interface{}{"type": "integer", "description": "Max matches (default 80)."},
			},
			"required": []string{"pattern"},
		}),
		toolDef("glob_files", "Find files by glob pattern relative to cwd (supports **). Examples: '**/*.go', 'src/**/*.ts'.", map[string]interface{}{
			"type": "object",
			"properties": map[string]interface{}{
				"pattern": map[string]interface{}{"type": "string", "description": "Glob pattern."},
			},
			"required": []string{"pattern"},
		}),
		toolDef("todo_read", "Read the current task list for this project.", map[string]interface{}{
			"type":       "object",
			"properties": map[string]interface{}{},
		}),
		toolDef("recall", "Recall project memory notes. Omit key to list all.", map[string]interface{}{
			"type": "object",
			"properties": map[string]interface{}{
				"key": map[string]interface{}{"type": "string", "description": "Optional memory key."},
			},
		}),
	}

	writeTools := []Tool{
		toolDef("write_file", "Create or overwrite a file (auto-creates parent dirs). One file per call.", map[string]interface{}{
			"type": "object",
			"properties": map[string]interface{}{
				"path":    map[string]interface{}{"type": "string"},
				"content": map[string]interface{}{"type": "string"},
			},
			"required": []string{"path", "content"},
		}),
		toolDef("apply_search_replace", "Exact search/replace patch on an existing file. Prefer over full rewrites.", map[string]interface{}{
			"type": "object",
			"properties": map[string]interface{}{
				"path":    map[string]interface{}{"type": "string"},
				"search":  map[string]interface{}{"type": "string"},
				"replace": map[string]interface{}{"type": "string"},
			},
			"required": []string{"path", "search", "replace"},
		}),
		toolDef("delete_file", "Delete a single file (not a directory).", map[string]interface{}{
			"type": "object",
			"properties": map[string]interface{}{
				"path": map[string]interface{}{"type": "string"},
			},
			"required": []string{"path"},
		}),
		toolDef("move_file", "Move or rename a file/directory within the sandbox.", map[string]interface{}{
			"type": "object",
			"properties": map[string]interface{}{
				"src": map[string]interface{}{"type": "string"},
				"dst": map[string]interface{}{"type": "string"},
			},
			"required": []string{"src", "dst"},
		}),
		toolDef("execute_command", "Run a shell command in Termux. Keep it fast and foreground-only (PPK).", map[string]interface{}{
			"type": "object",
			"properties": map[string]interface{}{
				"command": map[string]interface{}{"type": "string"},
			},
			"required": []string{"command"},
		}),
		toolDef("todo_write", "Create or update the project todo list for multi-step work. Status: pending|in_progress|completed|cancelled.", map[string]interface{}{
			"type": "object",
			"properties": map[string]interface{}{
				"merge": map[string]interface{}{"type": "boolean", "description": "If true, merge by id; else replace list."},
				"todos": map[string]interface{}{
					"type": "array",
					"items": map[string]interface{}{
						"type": "object",
						"properties": map[string]interface{}{
							"id":      map[string]interface{}{"type": "string"},
							"content": map[string]interface{}{"type": "string"},
							"status":  map[string]interface{}{"type": "string"},
						},
						"required": []string{"content", "status"},
					},
				},
			},
			"required": []string{"todos"},
		}),
		toolDef("remember", "Persist a short project note across sessions. Empty value deletes the key.", map[string]interface{}{
			"type": "object",
			"properties": map[string]interface{}{
				"key":   map[string]interface{}{"type": "string"},
				"value": map[string]interface{}{"type": "string"},
			},
			"required": []string{"key"},
		}),
	}

	if a.cfg.PlanMode {
		// Plan mode: allow todo_write + remember (non-destructive planning aids) plus reads.
		planWrites := []Tool{}
		for _, t := range writeTools {
			switch t.Function.Name {
			case "todo_write", "remember":
				planWrites = append(planWrites, t)
			}
		}
		return append(readTools, planWrites...)
	}
	return append(readTools, writeTools...)
}

func toolDef(name, desc string, params map[string]interface{}) Tool {
	return Tool{
		Type: "function",
		Function: ToolDefinition{
			Name:        name,
			Description: desc,
			Parameters:  params,
		},
	}
}

func (a *Agent) RunTurn(ctx context.Context, userText string) (string, error) {
	a.mu.Lock()
	defer a.mu.Unlock()

	a.history = append(a.history, Message{Role: "user", Content: userText})
	a.compactHistory()
	a.persistLocked()

	maxTurns := a.cfg.MaxTurns
	if maxTurns <= 0 {
		maxTurns = 40
	}

	for turn := 1; turn <= maxTurns; turn++ {
		if err := ctx.Err(); err != nil {
			a.persistLocked()
			return "", err
		}

		tools := a.tools
		reqPayload := ChatCompletionRequest{
			Model:     a.cfg.Model,
			Messages:  a.buildMessagesLocked(),
			Tools:     tools,
			MaxTokens: a.cfg.MaxTokens,
			Stream:    a.cfg.Stream,
		}

		var spinner *SimpleSpinner
		if !a.cfg.Stream {
			spinner = NewSpinner(fmt.Sprintf("Thinking… (turn %d/%d)", turn, maxTurns))
		} else {
			fmt.Fprintf(os.Stderr, "\033[2mthinking (turn %d/%d)…\033[0m\n", turn, maxTurns)
		}

		resp, err := a.doRequest(ctx, reqPayload)
		if spinner != nil {
			spinner.Stop()
		}
		if err != nil {
			a.persistLocked()
			return "", fmt.Errorf("LLM request failed: %w", err)
		}
		if len(resp.Choices) == 0 {
			return "", errors.New("empty response from LLM")
		}

		choice := resp.Choices[0]
		assistantMsg := choice.Message
		if assistantMsg.Role == "" {
			assistantMsg.Role = "assistant"
		}
		sanitizeToolCalls(&assistantMsg)
		a.history = append(a.history, assistantMsg)
		a.persistLocked()

		if len(assistantMsg.ToolCalls) == 0 {
			final := assistantMsg.Content
			if !a.cfg.PlanMode {
				if notes := a.applyExtractedEdits(final); notes != "" {
					final += "\n\n" + notes
				}
			}
			if choice.FinishReason == "length" {
				final += "\n\n(response truncated: max_tokens reached — say \"continue\" to keep going)"
			}
			a.persistLocked()
			return final, nil
		}

		for _, tc := range assistantMsg.ToolCalls {
			if err := ctx.Err(); err != nil {
				a.persistLocked()
				return "", err
			}
			a.history = append(a.history, a.executeTool(ctx, tc))
			a.persistLocked()
		}
		a.compactHistory()
		a.persistLocked()
	}

	return "", fmt.Errorf("max tool turns (%d) exceeded without final answer — raise /settings turns or say continue", maxTurns)
}

func (a *Agent) persistLocked() {
	if !a.cfg.AutoSave {
		return
	}
	s := NewEmptySession(a.cfg.Provider, a.cfg.Model)
	s.History = append([]Message(nil), a.history...)
	s.PlanMode = a.cfg.PlanMode
	_ = SaveSession(s)
}

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
		if err := json.Unmarshal([]byte(args), &probe); err != nil || probe == nil {
			tc.Function.Arguments = "{}"
		}
	}
}

func (a *Agent) applyExtractedEdits(text string) string {
	var b strings.Builder
	applied, failed := 0, 0

	for _, p := range ExtractPatches(text) {
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

func (a *Agent) buildMessagesLocked() []Message {
	sys := defaultSystemPrompt
	if a.cfg.PlanMode {
		sys += planModeExtra
	}
	sys += "\n\n" + TermuxEnvironment() + "\n"
	sys += a.project.PromptBlock()
	if mem := a.memory.SummaryForPrompt(1500); mem != "" {
		sys += "\n" + mem
	}
	if todos := a.todos.ReadTodos(); todos != "(no todos)" {
		sys += "\nCURRENT TODOS:\n" + todos + "\n"
	}

	out := make([]Message, 0, len(a.history)+1)
	out = append(out, Message{Role: "system", Content: sys})
	out = append(out, a.history...)
	return out
}

func (a *Agent) executeTool(ctx context.Context, tc ToolCall) Message {
	spinner := NewSpinner(fmt.Sprintf("Running %s…", tc.Function.Name))
	result, detail, ok, note := a.runTool(ctx, tc)
	spinner.Stop()
	printToolActivity(tc.Function.Name, detail, ok, note)

	if estimateTokens(result) > 800 {
		result = truncateLines(result, 40) + "\n... (output truncated by agent)"
	}

	return Message{
		Role:       "tool",
		ToolCallID: tc.ID,
		Content:    result,
	}
}

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
		if a.cfg.PlanMode {
			return "Error: execute_command blocked in plan mode", detail, false, "plan mode"
		}
		if dang, why := DangerousCommand(args.Command); dang && !a.cfg.AutoApprove {
			allow := true
			if a.approver != nil {
				// Unlock briefly so the prompt can use stdin without deadlock
				// (RunTurn holds a.mu). Approver is sync; we call it while locked
				// which is OK for CLI — stdin is the same session.
				allow = a.approver(args.Command, why)
			}
			if !allow {
				return fmt.Sprintf("Error: dangerous command denied by user (%s)", why), detail, false, "denied"
			}
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
		if a.cfg.PlanMode {
			return "Error: write_file blocked in plan mode", detail, false, "plan mode"
		}
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
			Path   string `json:"path"`
			Offset int    `json:"offset"`
			Limit  int    `json:"limit"`
		}
		if err := json.Unmarshal([]byte(tc.Function.Arguments), &args); err != nil {
			return fmt.Sprintf("Error parsing arguments: %v", err), "", false, err.Error()
		}
		detail = args.Path
		if args.Path == "" {
			return "Error: missing path", detail, false, "missing path"
		}
		content, err := ReadFileRange(args.Path, args.Offset, args.Limit)
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
		if a.cfg.PlanMode {
			return "Error: apply_search_replace blocked in plan mode", detail, false, "plan mode"
		}
		if args.Path == "" {
			return "Error: missing path", detail, false, "missing path"
		}
		if err := ApplySearchReplace(args.Path, args.Search, args.Replace); err != nil {
			return fmt.Sprintf("Error applying patch: %v", err), detail, false, err.Error()
		}
		return "Patch applied successfully.", detail, true, ""

	case "delete_file":
		var args struct {
			Path string `json:"path"`
		}
		if err := json.Unmarshal([]byte(tc.Function.Arguments), &args); err != nil {
			return fmt.Sprintf("Error parsing arguments: %v", err), "", false, err.Error()
		}
		detail = args.Path
		if a.cfg.PlanMode {
			return "Error: delete_file blocked in plan mode", detail, false, "plan mode"
		}
		msg, err := DeleteFile(args.Path)
		if err != nil {
			return fmt.Sprintf("Error: %v", err), detail, false, err.Error()
		}
		return msg, detail, true, ""

	case "move_file":
		var args struct {
			Src string `json:"src"`
			Dst string `json:"dst"`
		}
		if err := json.Unmarshal([]byte(tc.Function.Arguments), &args); err != nil {
			return fmt.Sprintf("Error parsing arguments: %v", err), "", false, err.Error()
		}
		detail = args.Src + " → " + args.Dst
		if a.cfg.PlanMode {
			return "Error: move_file blocked in plan mode", detail, false, "plan mode"
		}
		msg, err := MoveFile(args.Src, args.Dst)
		if err != nil {
			return fmt.Sprintf("Error: %v", err), detail, false, err.Error()
		}
		return msg, detail, true, ""

	case "grep_files":
		var args struct {
			Pattern string `json:"pattern"`
			Path    string `json:"path"`
			Regex   bool   `json:"regex"`
			MaxHits int    `json:"max_hits"`
		}
		if err := json.Unmarshal([]byte(tc.Function.Arguments), &args); err != nil {
			return fmt.Sprintf("Error parsing arguments: %v", err), "", false, err.Error()
		}
		detail = args.Pattern
		out, err := GrepFiles(args.Path, args.Pattern, args.Regex, args.MaxHits)
		if err != nil {
			return fmt.Sprintf("Error: %v", err), detail, false, err.Error()
		}
		return out, detail, true, ""

	case "glob_files":
		var args struct {
			Pattern string `json:"pattern"`
		}
		if err := json.Unmarshal([]byte(tc.Function.Arguments), &args); err != nil {
			return fmt.Sprintf("Error parsing arguments: %v", err), "", false, err.Error()
		}
		detail = args.Pattern
		out, err := GlobFiles(args.Pattern, 0)
		if err != nil {
			return fmt.Sprintf("Error: %v", err), detail, false, err.Error()
		}
		return out, detail, true, ""

	case "todo_write":
		var args struct {
			Merge bool   `json:"merge"`
			Todos []Todo `json:"todos"`
		}
		if err := json.Unmarshal([]byte(tc.Function.Arguments), &args); err != nil {
			return fmt.Sprintf("Error parsing arguments: %v", err), "", false, err.Error()
		}
		detail = fmt.Sprintf("%d items", len(args.Todos))
		out, err := a.todos.WriteTodos(args.Todos, args.Merge)
		if err != nil {
			return fmt.Sprintf("Error: %v", err), detail, false, err.Error()
		}
		return out, detail, true, ""

	case "todo_read":
		detail = "todos"
		return a.todos.ReadTodos(), detail, true, ""

	case "remember":
		var args struct {
			Key   string `json:"key"`
			Value string `json:"value"`
		}
		if err := json.Unmarshal([]byte(tc.Function.Arguments), &args); err != nil {
			return fmt.Sprintf("Error parsing arguments: %v", err), "", false, err.Error()
		}
		detail = args.Key
		out, err := a.memory.Remember(args.Key, args.Value)
		if err != nil {
			return fmt.Sprintf("Error: %v", err), detail, false, err.Error()
		}
		return out, detail, true, ""

	case "recall":
		var args struct {
			Key string `json:"key"`
		}
		_ = json.Unmarshal([]byte(tc.Function.Arguments), &args)
		detail = args.Key
		if detail == "" {
			detail = "(all)"
		}
		return a.memory.Recall(args.Key), detail, true, ""

	default:
		return fmt.Sprintf("Error: unknown tool %s", tc.Function.Name), tc.Function.Name, false, "unknown tool"
	}
}

func (a *Agent) compactHistory() {
	budget := a.cfg.HistoryBudget
	if budget <= 0 {
		budget = 16000
	}

	for i := 0; i < len(a.history)-1; i++ {
		a.history[i].Content = compressContent(a.history[i].Content)
	}

	for estimateMessages(a.history) > budget && len(a.history) > 4 {
		if !a.removeOldestToolPair() {
			a.history = a.history[1:]
		}
	}

	if estimateMessages(a.history) > budget {
		for i := 0; i < len(a.history)-1; i++ {
			if len(a.history[i].Content) > 200 {
				a.history[i].Content = a.history[i].Content[:200] + "...[truncated]"
			}
		}
	}
}

// CompactNow forces history compaction (slash /compact).
func (a *Agent) CompactNow() int {
	a.mu.Lock()
	defer a.mu.Unlock()
	before := estimateMessages(a.history)
	a.compactHistory()
	after := estimateMessages(a.history)
	a.persistLocked()
	return before - after
}

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
