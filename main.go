package main

import (
	"bufio"
	"context"
	"fmt"
	"os"
	"sort"
	"strings"
	"time"
)

func main() {
	cm := NewConfigManager()

	fmt.Println()
	fmt.Println("  Termux Agent")
	fmt.Println("  Interactive AI assistant for Android Termux")
	fmt.Println()
	fmt.Println("  Type a message to chat, or use slash commands:")
	fmt.Println("    /help, /?           Show this help")
	fmt.Println("    /provider           Choose a provider (OpenAI, OpenRouter, Groq, Ollama, etc.)")
	fmt.Println("    /model              Set or change the model")
	fmt.Println("    /endpoint           Set a custom API endpoint")
	fmt.Println("    /key                Set or update the API key")
	fmt.Println("    /status             Show current configuration")
	fmt.Println("    /settings           Adjust max-tokens, history-budget, shell-timeout, max-turns")
	fmt.Println("    /header             Add or remove custom HTTP headers")
	fmt.Println("    /reset              Reset configuration to defaults")
	fmt.Println("    /exit, /quit        Leave")
	fmt.Println()

	agent := NewAgent(cm.ToAgentConfig())
	scanner := bufio.NewScanner(os.Stdin)

	for {
		fmt.Fprint(os.Stderr, "[1;32m>[0m ")
		if !scanner.Scan() {
			break
		}
		line := strings.TrimSpace(scanner.Text())
		if line == "" {
			continue
		}

		if strings.HasPrefix(line, "/") {
			handled := handleSlash(line, cm, &agent)
			if !handled {
				break
			}
			continue
		}

		cfg := cm.ToAgentConfig()
		if cfg.APIKey == "" && cfg.Provider != "ollama" && cfg.Provider != "lmstudio" && cfg.Provider != "vllm" {
			fmt.Fprintln(os.Stderr, "[1;31mNo API key configured. Use /key to set one, or /provider to switch to a local model.[0m")
			continue
		}
		if cfg.Endpoint == "" {
			fmt.Fprintln(os.Stderr, "[1;31mNo endpoint configured. Use /endpoint to set one.[0m")
			continue
		}

		ctx, cancel := context.WithTimeout(context.Background(), 3*time.Minute)
		resp, err := agent.RunTurn(ctx, line)
		cancel()
		if err != nil {
			fmt.Fprintf(os.Stderr, "[1;31mError:[0m %v
", err)
			continue
		}
		if resp != "" {
			fmt.Println(resp)
		}
	}
}

func handleSlash(line string, cm *ConfigManager, agentPtr **Agent) bool {
	parts := strings.Fields(line)
	if len(parts) == 0 {
		return true
	}
	cmd := parts[0]

	switch cmd {
	case "/exit", "/quit":
		fmt.Println("Goodbye.")
		return false

	case "/help", "/?":
		printHelp()

	case "/provider":
		interactiveProvider(cm, agentPtr)

	case "/model":
		interactiveModel(cm, agentPtr)

	case "/endpoint":
		interactiveEndpoint(cm, agentPtr)

	case "/key":
		interactiveKey(cm, agentPtr)

	case "/status":
		fmt.Println(cm.Status())

	case "/settings":
		interactiveSettings(cm, agentPtr)

	case "/header":
		interactiveHeaders(cm, agentPtr)

	case "/reset":
		fmt.Print("Reset all settings to default? (y/N): ")
		var confirm string
		fmt.Scanln(&confirm)
		if strings.ToLower(confirm) == "y" {
			cfg := defaultUserConfig()
			_ = saveUserConfig(cfg)
			*cm = *NewConfigManager()
			*agentPtr = NewAgent(cm.ToAgentConfig())
			fmt.Println("Settings reset.")
		} else {
			fmt.Println("Cancelled.")
		}

	default:
		fmt.Fprintf(os.Stderr, "Unknown command: %s. Type /help for available commands.
", cmd)
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

func interactiveProvider(cm *ConfigManager, agentPtr **Agent) {
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
		fmt.Printf("  %d. %-12s %s%s
", i+1, k, preset.Name, keyHint)
	}
	fmt.Print("Choose provider (name or number): ")
	var choice string
	fmt.Scanln(&choice)
	choice = strings.TrimSpace(strings.ToLower(choice))

	for i, k := range keys {
		if choice == fmt.Sprintf("%d", i+1) {
			choice = k
			break
		}
	}

	if _, ok := providerPresets[choice]; !ok {
		fmt.Println("Invalid provider.")
		return
	}

	if err := cm.SetProvider(choice); err != nil {
		fmt.Println("Error:", err)
		return
	}

	preset := providerPresets[choice]
	fmt.Printf("Provider set to %s.
", preset.Name)
	fmt.Printf("Endpoint: %s
", preset.Endpoint)
	if len(preset.Models) > 0 {
		fmt.Printf("Default model: %s
", preset.Models[0])
	}
	if preset.NeedsKey {
		cfg := cm.Get()
		if cfg.APIKey == "" {
			fmt.Println("This provider requires an API key. Use /key to set one.")
		}
	}

	*agentPtr = NewAgent(cm.ToAgentConfig())
}

func interactiveModel(cm *ConfigManager, agentPtr **Agent) {
	cfg := cm.Get()
	preset, hasPreset := providerPresets[cfg.Provider]
	if hasPreset && len(preset.Models) > 0 {
		fmt.Println("Suggested models for this provider:")
		for i, m := range preset.Models {
			marker := " "
			if m == cfg.Model {
				marker = "*"
			}
			fmt.Printf("  %s %d. %s
", marker, i+1, m)
		}
		fmt.Println("Or type any model name manually.")
	}
	fmt.Printf("Current model: %s
New model: ", cfg.Model)
	var choice string
	fmt.Scanln(&choice)
	choice = strings.TrimSpace(choice)
	if choice == "" {
		fmt.Println("No change.")
		return
	}

	if hasPreset {
		var idx int
		if _, err := fmt.Sscanf(choice, "%d", &idx); err == nil && idx > 0 && idx <= len(preset.Models) {
			choice = preset.Models[idx-1]
		}
	}

	if err := cm.SetModel(choice); err != nil {
		fmt.Println("Error:", err)
		return
	}
	fmt.Printf("Model set to %s.
", choice)
	*agentPtr = NewAgent(cm.ToAgentConfig())
}

func interactiveEndpoint(cm *ConfigManager, agentPtr **Agent) {
	cfg := cm.Get()
	fmt.Printf("Current endpoint: %s
New endpoint (or keep empty to keep current): ", cfg.Endpoint)
	reader := bufio.NewReader(os.Stdin)
	choice, _ := reader.ReadString('
')
	choice = strings.TrimSpace(choice)
	if choice == "" {
		fmt.Println("No change.")
		return
	}
	if err := cm.SetEndpoint(choice); err != nil {
		fmt.Println("Error:", err)
		return
	}
	fmt.Printf("Endpoint set to %s.
", choice)
	*agentPtr = NewAgent(cm.ToAgentConfig())
}

func interactiveKey(cm *ConfigManager, agentPtr **Agent) {
	cfg := cm.Get()
	keyHint := "(not set)"
	if cfg.APIKey != "" {
		keyHint = "(set)"
	}
	fmt.Printf("Current API key: %s
New API key (or keep empty to keep current): ", keyHint)
	reader := bufio.NewReader(os.Stdin)
	choice, _ := reader.ReadString('
')
	choice = strings.TrimSpace(choice)
	if choice == "" {
		fmt.Println("No change.")
		return
	}
	if err := cm.SetAPIKey(choice); err != nil {
		fmt.Println("Error:", err)
		return
	}
	fmt.Println("API key updated.")
	*agentPtr = NewAgent(cm.ToAgentConfig())
}

func interactiveSettings(cm *ConfigManager, agentPtr **Agent) {
	cfg := cm.Get()
	fmt.Printf("Current settings:
  max-tokens: %d
  history-budget: %d
  shell-timeout: %ds
  max-turns: %d
",
		cfg.MaxTokens, cfg.HistoryBudget, cfg.ShellTimeout, cfg.MaxTurns)
	fmt.Println("Which setting to change? (tokens/budget/timeout/turns/cancel)")
	var choice string
	fmt.Scanln(&choice)
	choice = strings.ToLower(strings.TrimSpace(choice))

	switch choice {
	case "tokens":
		fmt.Print("New max-tokens: ")
		var v string
		fmt.Scanln(&v)
		n := parseIntOr(v, cfg.MaxTokens)
		_ = cm.SetMaxTokens(n)
		fmt.Printf("max-tokens set to %d.
", n)
	case "budget":
		fmt.Print("New history-budget: ")
		var v string
		fmt.Scanln(&v)
		n := parseIntOr(v, cfg.HistoryBudget)
		_ = cm.SetHistoryBudget(n)
		fmt.Printf("history-budget set to %d.
", n)
	case "timeout":
		fmt.Print("New shell-timeout (seconds): ")
		var v string
		fmt.Scanln(&v)
		n := parseIntOr(v, cfg.ShellTimeout)
		_ = cm.SetShellTimeout(n)
		fmt.Printf("shell-timeout set to %ds.
", n)
	case "turns":
		fmt.Print("New max-turns: ")
		var v string
		fmt.Scanln(&v)
		n := parseIntOr(v, cfg.MaxTurns)
		_ = cm.SetMaxTurns(n)
		fmt.Printf("max-turns set to %d.
", n)
	case "cancel", "":
		fmt.Println("No changes.")
		return
	default:
		fmt.Println("Unknown setting.")
		return
	}
	*agentPtr = NewAgent(cm.ToAgentConfig())
}

func interactiveHeaders(cm *ConfigManager, agentPtr **Agent) {
	cfg := cm.Get()
	fmt.Println("Current custom headers:")
	if len(cfg.ExtraHeaders) == 0 {
		fmt.Println("  (none)")
	} else {
		for k, v := range cfg.ExtraHeaders {
			fmt.Printf("  %s: %s
", k, v)
		}
	}
	fmt.Println("Actions: add / remove / back")
	var choice string
	fmt.Scanln(&choice)
	choice = strings.ToLower(strings.TrimSpace(choice))

	switch choice {
	case "add":
		fmt.Print("Header key: ")
		var key string
		fmt.Scanln(&key)
		key = strings.TrimSpace(key)
		if key == "" {
			fmt.Println("Cancelled.")
			return
		}
		fmt.Print("Header value: ")
		reader := bufio.NewReader(os.Stdin)
		val, _ := reader.ReadString('
')
		val = strings.TrimSpace(val)
		_ = cm.SetExtraHeader(key, val)
		fmt.Printf("Header %s added.
", key)
	case "remove":
		fmt.Print("Header key to remove: ")
		var key string
		fmt.Scanln(&key)
		key = strings.TrimSpace(key)
		_ = cm.DeleteExtraHeader(key)
		fmt.Printf("Header %s removed if it existed.
", key)
	case "back", "":
		return
	default:
		fmt.Println("Unknown action.")
		return
	}
	*agentPtr = NewAgent(cm.ToAgentConfig())
}
