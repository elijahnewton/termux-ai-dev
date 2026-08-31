package main

import (
    "bufio"
    "context"
    "fmt"
    "os"
    "sort"
    "strconv"
    "strings"
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
    line = strings.TrimSpace(line) // also strips \r on CRLF terminals
    if err != nil {
        return line, false
    }
    return line, true
}

// session bundles the mutable pieces the slash commands operate on.
type session struct {
    cm    *ConfigManager
    agent *Agent
}

func main() {
    cm := NewConfigManager()
    s := &session{cm: cm, agent: NewAgent(cm.ToAgentConfig())}

    fmt.Println()
    fmt.Println("  Termux Agent")
    fmt.Println("  Interactive AI assistant for Android Termux")
    fmt.Println()
    printHelp()
    fmt.Println()

    for {
        line, ok := readLine("\033[1;32m>\033[0m ")
        if !ok {
            break // EOF (Ctrl-D)
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

        cfg := s.cm.ToAgentConfig()
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

        ctx, cancel := context.WithTimeout(context.Background(), 3*time.Minute)
        resp, err := s.agent.RunTurn(ctx, line)
        cancel()
        if err != nil {
            fmt.Fprintf(os.Stderr, "\033[1;31mError:\033[0m %v\n", err)
            continue
        }
        if resp == "" {
            fmt.Fprintln(os.Stderr, "\033[1;33m(empty response from model)\033[0m")
            continue
        }
        fmt.Println(resp)
    }
}

func handleSlash(line string, s *session) bool {
    parts := strings.Fields(line)
    if len(parts) == 0 {
        return true
    }

    switch parts[0] {
    case "/exit", "/quit":
        fmt.Println("Goodbye.")
        return false

    case "/help", "/?":
        printHelp()

    case "/provider":
        interactiveProvider(s)

    case "/model":
        interactiveModel(s)

    case "/endpoint":
        interactiveEndpoint(s)

    case "/key":
        interactiveKey(s)

    case "/status":
        fmt.Println(s.cm.Status())

    case "/settings":
        interactiveSettings(s)

    case "/header":
        interactiveHeaders(s)

    case "/reset":
        ans, _ := readLine("Reset all settings to default? (y/N): ")
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
        fmt.Fprintf(os.Stderr, "Unknown command: %s. Type /help for available commands.\n", parts[0])
    }
    return true
}

func printHelp() {
    fmt.Println("Slash commands:")
    fmt.Println("  /help, /?           Show this help")
    fmt.Println("  /provider           Choose a provider (OpenAI, OpenRouter, Groq, Ollama, etc.)")
    fmt.Println("  /model              Set or change the model")
    fmt.Println("  /endpoint           Set a custom API endpoint")
    fmt.Println("  /key                Set or update the API key")
    fmt.Println("  /status             Show current configuration")
    fmt.Println("  /settings           Adjust max-tokens, history-budget, shell-timeout, max-turns")
    fmt.Println("  /header             Add or remove custom HTTP headers")
    fmt.Println("  /reset              Reset configuration to defaults")
    fmt.Println("  /exit, /quit        Leave")
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
    s.agent = NewAgent(s.cm.ToAgentConfig())
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
    // Exact-integer parse only: "7b" must not become model #7.
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
    s.agent = NewAgent(s.cm.ToAgentConfig())
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
    s.agent = NewAgent(s.cm.ToAgentConfig())
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
    s.agent = NewAgent(s.cm.ToAgentConfig())
}

func interactiveSettings(s *session) {
    cfg := s.cm.Get()
    fmt.Printf("Current settings:\n  max-tokens: %d\n  history-budget: %d\n  shell-timeout: %ds\n  max-turns: %d\n",
        cfg.MaxTokens, cfg.HistoryBudget, cfg.ShellTimeout, cfg.MaxTurns)
    choice, ok := readLine("Which setting to change? (tokens/budget/timeout/turns/cancel) ")
    if !ok {
        return
    }
    choice = strings.ToLower(choice)

    set := func(label string, cur int, fn func(int) error) {
        v, _ := readLine(fmt.Sprintf("New %s: ", label))
        n := parseIntOr(v, cur)
        if err := fn(n); err != nil {
            fmt.Fprintln(os.Stderr, "Error:", err)
            return
        }
        fmt.Printf("%s set to %d.\n", label, n)
        s.agent = NewAgent(s.cm.ToAgentConfig())
    }

    switch choice {
    case "tokens":
        set("max-tokens", cfg.MaxTokens, s.cm.SetMaxTokens)
    case "budget":
        set("history-budget", cfg.HistoryBudget, s.cm.SetHistoryBudget)
    case "timeout":
        set("shell-timeout (seconds)", cfg.ShellTimeout, s.cm.SetShellTimeout)
    case "turns":
        set("max-turns", cfg.MaxTurns, s.cm.SetMaxTurns)
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
    s.agent = NewAgent(s.cm.ToAgentConfig())
}