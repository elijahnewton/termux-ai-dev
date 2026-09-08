package main

import (
	"bufio"
	"context"
	"fmt"
	"os"
	"os/signal"
	"sort"
	"strconv"
	"strings"
	"syscall"
	"time"
)

// Single shared reader for all stdin input. Mixing bufio.Scanner, ad-hoc
// bufio.Readers and fmt.Scanln on the same fd loses buffered data.
var stdin = bufio.NewReader(os.Stdin)

// readLine prints prompt to stderr and reads one trimmed line from stdin.
// ok is false on EOF/read error.
func readLine(prompt string) (string, bool) {
	if prompt != "" {
		fmt.Fprint(os.Stderr, prompt)
	}
	line, err := stdin.ReadString('\n')
	line = strings.TrimSpace(line)
	if err != nil {
		return line, false
	}
	return line, true
}

type session struct {
	cm    *ConfigManager
	agent *Agent
}

func main() {
	cm := NewConfigManager()
	s := &session{cm: cm, agent: NewAgent(cm.ToAgentConfig())}

	// Resume last session for this cwd (PPK / crash recovery).
	if saved, err := LoadSession(); err == nil && len(saved.History) > 0 {
		s.agent.SetHistory(saved.History)
		if saved.PlanMode {
			_ = cm.SetPlanMode(true)
			s.agent.SetPlanMode(true)
		}
		fmt.Fprintf(os.Stderr, "\033[2mResumed session: %d messages (use /clear to start fresh)\033[0m\n", len(saved.History))
	}

	printBanner(s)

	// Cancel in-flight turns on Ctrl+C without exiting the REPL on first press.
	sigCh := make(chan os.Signal, 1)
	signal.Notify(sigCh, os.Interrupt, syscall.SIGTERM)

	var cancelTurn context.CancelFunc

	go func() {
		for range sigCh {
			if cancelTurn != nil {
				fmt.Fprintln(os.Stderr, "\n\033[1;33mInterrupted turn.\033[0m")
				cancelTurn()
				cancelTurn = nil
			} else {
				fmt.Fprintln(os.Stderr, "\nPress Ctrl+C again or type /exit to quit.")
				// Second interrupt exits
				signal.Stop(sigCh)
				os.Exit(130)
			}
		}
	}()

	for {
		line, ok := readLine("\033[1;32m❯\033[0m ")
		if !ok {
			break
		}
		if line == "" {
			continue
		}

		if strings.HasPrefix(line, "/") {
			if !handleSlash(line, s) {
				break
			}
			continue
		}

		cfg := s.cm.Get()
		needsKey := true
		if preset, ok := providerPresets[cfg.Provider]; ok {
			needsKey = preset.NeedsKey
		}
		if cfg.APIKey == "" && needsKey {
			fmt.Fprintln(os.Stderr, "\033[1;31mNo API key configured. Use /key to set one, or /provider to switch to a local model.\033[0m")
			continue
		}
		if cfg.Endpoint == "" {
			fmt.Fprintln(os.Stderr, "\033[1;31mNo endpoint configured. Use /endpoint to set one.\033[0m")
			continue
		}

		ctx, cancel := context.WithTimeout(context.Background(), 15*time.Minute)
		cancelTurn = cancel
		resp, err := s.agent.RunTurn(ctx, line)
		cancel()
		cancelTurn = nil

		if err != nil {
			if ctx.Err() == context.Canceled {
				fmt.Fprintln(os.Stderr, "\033[1;33m(cancelled)\033[0m")
				continue
			}
			fmt.Fprintf(os.Stderr, "\033[1;31mError:\033[0m %v\n", err)
			if strings.Contains(err.Error(), "HTTP 400") {
				fmt.Fprintln(os.Stderr, "\033[1;33mHint:\033[0m check /provider, /model, /endpoint, and /key; some models reject tool-calling payloads. Try /stream off for stubborn local servers.")
			}
			continue
		}
		if resp == "" {
			fmt.Fprintln(os.Stderr, "\033[1;33m(empty response from model)\033[0m")
			continue
		}
		if !cfg.Stream {
			fmt.Println(resp)
		} else {
			if idx := strings.Index(resp, "[auto-applied"); idx >= 0 {
				fmt.Println(resp[idx:])
			}
			if strings.Contains(resp, "(response truncated") {
				fmt.Fprintln(os.Stderr, "\033[2m(response truncated: max_tokens — say \"continue\")\033[0m")
			}
		}
	}
}

func printBanner(s *session) {
	cfg := s.cm.Get()
	fmt.Println()
	fmt.Println("  \033[1mTermux Agent\033[0m")
	fmt.Println("  Agentic coding for Android Termux — enterprise builds on a phone")
	fmt.Printf("  %s · %s", cfg.Provider, cfg.Model)
	if cfg.PlanMode {
		fmt.Print(" · \033[1;36mPLAN\033[0m")
	}
	fmt.Println()
	fmt.Println()
	printHelp()
	fmt.Println()
}

func recreateAgent(s *session, keepHistory bool) {
	hist := s.agent.History()
	plan := s.agent.PlanMode()
	s.agent = NewAgent(s.cm.ToAgentConfig())
	if keepHistory {
		s.agent.SetHistory(hist)
	}
	s.agent.SetPlanMode(plan || s.cm.Get().PlanMode)
}

func handleSlash(line string, s *session) bool {
	parts := strings.Fields(line)
	if len(parts) == 0 {
		return true
	}
	cmd := parts[0]
	args := parts[1:]
	argStr := strings.TrimSpace(strings.TrimPrefix(line, cmd))

	switch cmd {
	case "/exit", "/quit":
		fmt.Println("Goodbye.")
		return false

	case "/help", "/?":
		printHelp()

	case "/provider":
		if len(args) > 0 {
			name := strings.ToLower(args[0])
			if err := s.cm.SetProvider(name); err != nil {
				fmt.Fprintln(os.Stderr, "Error:", err)
				return true
			}
			preset := providerPresets[name]
			fmt.Printf("Provider set to %s.\nEndpoint: %s\n", preset.Name, preset.Endpoint)
			recreateAgent(s, false)
			return true
		}
		interactiveProvider(s)

	case "/model":
		if argStr != "" {
			choice := argStr
			cfg := s.cm.Get()
			if preset, ok := providerPresets[cfg.Provider]; ok {
				if idx, err := strconv.Atoi(choice); err == nil && idx > 0 && idx <= len(preset.Models) {
					choice = preset.Models[idx-1]
				}
			}
			if err := s.cm.SetModel(choice); err != nil {
				fmt.Fprintln(os.Stderr, "Error:", err)
				return true
			}
			fmt.Printf("Model set to %s.\n", choice)
			recreateAgent(s, true)
			return true
		}
		interactiveModel(s)

	case "/endpoint":
		if argStr != "" {
			if err := s.cm.SetEndpoint(argStr); err != nil {
				fmt.Fprintln(os.Stderr, "Error:", err)
				return true
			}
			fmt.Printf("Endpoint set to %s.\n", argStr)
			recreateAgent(s, true)
			return true
		}
		interactiveEndpoint(s)

	case "/key":
		if argStr != "" {
			if err := s.cm.SetAPIKey(argStr); err != nil {
				fmt.Fprintln(os.Stderr, "Error:", err)
				return true
			}
			fmt.Println("API key updated.")
			recreateAgent(s, true)
			return true
		}
		interactiveKey(s)

	case "/status":
		fmt.Println(s.cm.Status())
		fmt.Printf("Messages in session: %d\n", len(s.agent.History()))

	case "/settings":
		if len(args) >= 2 {
			if !applySettingArg(s, args[0], args[1]) {
				fmt.Println("Usage: /settings tokens|budget|timeout|turns|stream|approve|save <value>")
			}
			return true
		}
		interactiveSettings(s)

	case "/header":
		interactiveHeaders(s)

	case "/plan":
		on := true
		if len(args) > 0 {
			if v, ok := parseBoolArg(args[0]); ok {
				on = v
			} else if args[0] == "toggle" {
				on = !s.agent.PlanMode()
			}
		} else {
			on = !s.agent.PlanMode()
		}
		_ = s.cm.SetPlanMode(on)
		s.agent.SetPlanMode(on)
		if on {
			fmt.Println("Plan mode ON — read-only exploration + planning (no file/shell mutations).")
		} else {
			fmt.Println("Plan mode OFF — full agentic coding enabled.")
		}

	case "/stream":
		on := !s.cm.Get().Stream
		if len(args) > 0 {
			if v, ok := parseBoolArg(args[0]); ok {
				on = v
			}
		}
		_ = s.cm.SetStream(on)
		recreateAgent(s, true)
		fmt.Printf("Streaming %s.\n", map[bool]string{true: "on", false: "off"}[on])

	case "/approve":
		on := !s.cm.Get().AutoApprove
		if len(args) > 0 {
			if v, ok := parseBoolArg(args[0]); ok {
				on = v
			}
		}
		_ = s.cm.SetAutoApprove(on)
		s.agent.SetAutoApprove(on)
		if on {
			fmt.Println("Auto-approve ON — dangerous commands run without prompting.")
		} else {
			fmt.Println("Auto-approve OFF — dangerous commands require confirmation.")
		}

	case "/clear":
		s.agent.ClearHistory()
		_ = ClearSession()
		fmt.Println("Conversation cleared.")

	case "/compact":
		saved := s.agent.CompactNow()
		fmt.Printf("Compacted ~%d tokens from history.\n", saved)

	case "/save":
		name := "default"
		if len(args) > 0 {
			name = args[0]
		}
		snap := s.agent.SnapshotSession(s.cm.Get().Provider, s.cm.Get().Model)
		if err := SaveSessionAs(snap, name); err != nil {
			fmt.Fprintln(os.Stderr, "Error:", err)
			return true
		}
		_ = SaveSession(snap)
		fmt.Printf("Session saved as %q.\n", name)

	case "/load":
		var (
			snap *Session
			err  error
		)
		if len(args) > 0 {
			snap, err = LoadSessionNamed(args[0])
		} else {
			snap, err = LoadSession()
		}
		if err != nil {
			fmt.Fprintln(os.Stderr, "Error:", err)
			return true
		}
		s.agent.SetHistory(snap.History)
		s.agent.SetPlanMode(snap.PlanMode)
		fmt.Printf("Loaded session (%d messages).\n", len(snap.History))

	case "/sessions":
		fmt.Println(ListSessions())

	case "/todos":
		fmt.Println(NewTodoStore().ReadTodos())

	case "/memory":
		fmt.Println(NewMemoryStore().Recall(""))

	case "/project":
		fmt.Println(DetectProject().PromptBlock())

	case "/reset":
		ans := "n"
		if len(args) > 0 && (args[0] == "-y" || args[0] == "--yes") {
			ans = "y"
		} else {
			ans, _ = readLine("Reset all settings to default? (y/N): ")
		}
		if strings.EqualFold(ans, "y") {
			if err := s.cm.Reset(); err != nil {
				fmt.Fprintln(os.Stderr, "Error:", err)
				return true
			}
			s.agent = NewAgent(s.cm.ToAgentConfig())
			fmt.Println("Settings reset (API key cleared — use /key to set it again).")
		} else {
			fmt.Println("Cancelled.")
		}

	default:
		fmt.Fprintf(os.Stderr, "Unknown command: %s. Type /help for available commands.\n", cmd)
	}
	return true
}

func applySettingArg(s *session, key, val string) bool {
	switch strings.ToLower(key) {
	case "tokens", "max-tokens", "max_tokens":
		n := parseIntOr(val, 0)
		if err := s.cm.SetMaxTokens(n); err != nil {
			fmt.Fprintln(os.Stderr, "Error:", err)
			return true
		}
	case "budget", "history", "history-budget":
		n := parseIntOr(val, 0)
		if err := s.cm.SetHistoryBudget(n); err != nil {
			fmt.Fprintln(os.Stderr, "Error:", err)
			return true
		}
	case "timeout", "shell-timeout":
		n := parseIntOr(val, 0)
		if err := s.cm.SetShellTimeout(n); err != nil {
			fmt.Fprintln(os.Stderr, "Error:", err)
			return true
		}
	case "turns", "max-turns":
		n := parseIntOr(val, 0)
		if err := s.cm.SetMaxTurns(n); err != nil {
			fmt.Fprintln(os.Stderr, "Error:", err)
			return true
		}
	case "stream":
		v, ok := parseBoolArg(val)
		if !ok {
			return false
		}
		_ = s.cm.SetStream(v)
	case "approve", "auto-approve":
		v, ok := parseBoolArg(val)
		if !ok {
			return false
		}
		_ = s.cm.SetAutoApprove(v)
		s.agent.SetAutoApprove(v)
	case "save", "auto-save":
		v, ok := parseBoolArg(val)
		if !ok {
			return false
		}
		_ = s.cm.SetAutoSave(v)
	default:
		return false
	}
	recreateAgent(s, true)
	fmt.Printf("Updated %s.\n", key)
	return true
}

func printHelp() {
	fmt.Println("Slash commands:")
	fmt.Println("  /help, /?              Show this help")
	fmt.Println("  /provider [name]       Choose provider (openai, openrouter, groq, ollama, …)")
	fmt.Println("  /model [name|n]        Set model (presets are suggestions; any id works)")
	fmt.Println("  /endpoint [url]        Set custom API endpoint")
	fmt.Println("  /key [secret]          Set API key")
	fmt.Println("  /status                Show configuration + session size")
	fmt.Println("  /settings […]          Adjust tokens/budget/timeout/turns/stream/approve/save")
	fmt.Println("  /header                Add or remove custom HTTP headers")
	fmt.Println("  /plan [on|off]         Toggle plan mode (read-only design)")
	fmt.Println("  /stream [on|off]       Toggle token streaming")
	fmt.Println("  /approve [on|off]      Toggle dangerous-command auto-approve")
	fmt.Println("  /clear                 Clear conversation (keeps config)")
	fmt.Println("  /compact               Force history compaction")
	fmt.Println("  /save [name]           Save session (PPK-safe)")
	fmt.Println("  /load [name]           Load session")
	fmt.Println("  /sessions              List saved sessions")
	fmt.Println("  /todos                 Show project todo list")
	fmt.Println("  /memory                Show project memory notes")
	fmt.Println("  /project               Show detected project context")
	fmt.Println("  /reset                 Reset configuration to defaults")
	fmt.Println("  /exit, /quit           Leave")
}

func interactiveProvider(s *session) {
	fmt.Println("Available providers:")
	keys := make([]string, 0, len(providerPresets))
	for k := range providerPresets {
		keys = append(keys, k)
	}
	sort.Strings(keys)
	for i, k := range keys {
		preset := providerPresets[k]
		keyHint := ""
		if preset.NeedsKey {
			keyHint = " [requires API key]"
		}
		fmt.Printf("  %d. %-12s %s%s\n", i+1, k, preset.Name, keyHint)
	}
	choice, ok := readLine("Choose provider (name or number): ")
	if !ok {
		return
	}
	choice = strings.ToLower(choice)

	for i, k := range keys {
		if choice == strconv.Itoa(i+1) {
			choice = k
			break
		}
	}
	if _, ok := providerPresets[choice]; !ok {
		fmt.Println("Invalid provider.")
		return
	}
	if err := s.cm.SetProvider(choice); err != nil {
		fmt.Fprintln(os.Stderr, "Error:", err)
		return
	}
	preset := providerPresets[choice]
	fmt.Printf("Provider set to %s.\nEndpoint: %s\n", preset.Name, preset.Endpoint)
	if len(preset.Models) > 0 {
		fmt.Printf("Default model: %s\n", preset.Models[0])
	}
	if preset.NeedsKey && s.cm.Get().APIKey == "" {
		fmt.Println("This provider requires an API key. Use /key to set one.")
	}
	recreateAgent(s, false)
}

func interactiveModel(s *session) {
	cfg := s.cm.Get()
	preset, hasPreset := providerPresets[cfg.Provider]
	if hasPreset && len(preset.Models) > 0 {
		fmt.Println("Suggested models for this provider:")
		for i, m := range preset.Models {
			marker := " "
			if m == cfg.Model {
				marker = "*"
			}
			fmt.Printf("  %s %d. %s\n", marker, i+1, m)
		}
		fmt.Println("Or type any model name manually.")
	}
	choice, ok := readLine(fmt.Sprintf("Current model: %s\nNew model: ", cfg.Model))
	if !ok || choice == "" {
		fmt.Println("No change.")
		return
	}
	if hasPreset {
		if idx, err := strconv.Atoi(choice); err == nil && idx > 0 && idx <= len(preset.Models) {
			choice = preset.Models[idx-1]
		}
	}
	if err := s.cm.SetModel(choice); err != nil {
		fmt.Fprintln(os.Stderr, "Error:", err)
		return
	}
	fmt.Printf("Model set to %s.\n", choice)
	recreateAgent(s, true)
}

func interactiveEndpoint(s *session) {
	cfg := s.cm.Get()
	choice, ok := readLine(fmt.Sprintf("Current endpoint: %s\nNew endpoint (empty to keep current): ", cfg.Endpoint))
	if !ok || choice == "" {
		fmt.Println("No change.")
		return
	}
	if err := s.cm.SetEndpoint(choice); err != nil {
		fmt.Fprintln(os.Stderr, "Error:", err)
		return
	}
	fmt.Printf("Endpoint set to %s.\n", choice)
	recreateAgent(s, true)
}

func interactiveKey(s *session) {
	cfg := s.cm.Get()
	keyHint := "(not set)"
	if cfg.APIKey != "" {
		keyHint = "(set)"
	}
	choice, ok := readLine(fmt.Sprintf("Current API key: %s\nNew API key (empty to keep current): ", keyHint))
	if !ok || choice == "" {
		fmt.Println("No change.")
		return
	}
	if err := s.cm.SetAPIKey(choice); err != nil {
		fmt.Fprintln(os.Stderr, "Error:", err)
		return
	}
	fmt.Println("API key updated.")
	recreateAgent(s, true)
}

func interactiveSettings(s *session) {
	cfg := s.cm.Get()
	fmt.Printf("Current settings:\n  max-tokens: %d\n  history-budget: %d\n  shell-timeout: %ds\n  max-turns: %d\n  stream: %v\n  auto-approve: %v\n  auto-save: %v\n",
		cfg.MaxTokens, cfg.HistoryBudget, cfg.ShellTimeout, cfg.MaxTurns, cfg.Stream, cfg.AutoApprove, cfg.AutoSave)
	choice, ok := readLine("Which setting? (tokens/budget/timeout/turns/stream/approve/save/cancel) ")
	if !ok {
		return
	}
	choice = strings.ToLower(choice)

	setInt := func(label string, cur int, fn func(int) error) {
		v, _ := readLine(fmt.Sprintf("New %s: ", label))
		n := parseIntOr(v, cur)
		if err := fn(n); err != nil {
			fmt.Fprintln(os.Stderr, "Error:", err)
			return
		}
		fmt.Printf("%s set to %d.\n", label, n)
		recreateAgent(s, true)
	}
	setBool := func(label string, cur bool, fn func(bool) error) {
		v, _ := readLine(fmt.Sprintf("New %s (on/off) [current %v]: ", label, cur))
		if b, ok := parseBoolArg(v); ok {
			if err := fn(b); err != nil {
				fmt.Fprintln(os.Stderr, "Error:", err)
				return
			}
			fmt.Printf("%s set to %v.\n", label, b)
			recreateAgent(s, true)
		} else {
			fmt.Println("No change.")
		}
	}

	switch choice {
	case "tokens":
		setInt("max-tokens", cfg.MaxTokens, s.cm.SetMaxTokens)
	case "budget":
		setInt("history-budget", cfg.HistoryBudget, s.cm.SetHistoryBudget)
	case "timeout":
		setInt("shell-timeout (seconds)", cfg.ShellTimeout, s.cm.SetShellTimeout)
	case "turns":
		setInt("max-turns", cfg.MaxTurns, s.cm.SetMaxTurns)
	case "stream":
		setBool("stream", cfg.Stream, s.cm.SetStream)
	case "approve":
		setBool("auto-approve", cfg.AutoApprove, func(b bool) error {
			if err := s.cm.SetAutoApprove(b); err != nil {
				return err
			}
			s.agent.SetAutoApprove(b)
			return nil
		})
	case "save":
		setBool("auto-save", cfg.AutoSave, s.cm.SetAutoSave)
	case "cancel", "":
		fmt.Println("No changes.")
	default:
		fmt.Println("Unknown setting.")
	}
}

func interactiveHeaders(s *session) {
	cfg := s.cm.Get()
	fmt.Println("Current custom headers:")
	if len(cfg.ExtraHeaders) == 0 {
		fmt.Println("  (none)")
	} else {
		for k, v := range cfg.ExtraHeaders {
			fmt.Printf("  %s: %s\n", k, v)
		}
	}
	choice, ok := readLine("Actions: add / remove / back ")
	if !ok {
		return
	}
	switch strings.ToLower(choice) {
	case "add":
		key, ok := readLine("Header key: ")
		if !ok || key == "" {
			fmt.Println("Cancelled.")
			return
		}
		val, _ := readLine("Header value: ")
		if err := s.cm.SetExtraHeader(key, val); err != nil {
			fmt.Fprintln(os.Stderr, "Error:", err)
			return
		}
		fmt.Printf("Header %s added.\n", key)
	case "remove":
		key, ok := readLine("Header key to remove: ")
		if !ok || key == "" {
			fmt.Println("Cancelled.")
			return
		}
		if err := s.cm.DeleteExtraHeader(key); err != nil {
			fmt.Fprintln(os.Stderr, "Error:", err)
			return
		}
		fmt.Printf("Header %s removed if it existed.\n", key)
	case "back", "":
		return
	default:
		fmt.Println("Unknown action.")
		return
	}
	recreateAgent(s, true)
}
