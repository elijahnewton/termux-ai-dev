package main

import (
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"strconv"
	"strings"
	"sync"
	"time"
)

// Model IDs drift frequently; these are refreshed defaults and the user can
// always set any model manually with /model.
var providerPresets = map[string]ProviderPreset{
	"openai": {
		Name:     "OpenAI",
		Endpoint: "https://api.openai.com/v1/chat/completions",
		Models:   []string{"gpt-4o", "gpt-4o-mini", "gpt-4.1", "gpt-4.1-mini", "o4-mini"},
		NeedsKey: true,
	},
	"openrouter": {
		Name:     "OpenRouter",
		Endpoint: "https://openrouter.ai/api/v1/chat/completions",
		Models:   []string{"anthropic/claude-sonnet-4", "anthropic/claude-opus-4", "openai/gpt-4o", "google/gemini-2.5-pro", "qwen/qwen3-coder"},
		NeedsKey: true,
		Headers:  map[string]string{"HTTP-Referer": "https://github.com/user/termux-agent", "X-Title": "TermuxAgent"},
	},
	"anthropic": {
		Name:     "Anthropic (OpenAI-compat proxy)",
		Endpoint: "https://api.anthropic.com/v1/chat/completions",
		Models:   []string{"claude-sonnet-4-20250514", "claude-opus-4-20250514"},
		NeedsKey: true,
	},
	"groq": {
		Name:     "Groq",
		Endpoint: "https://api.groq.com/openai/v1/chat/completions",
		Models:   []string{"llama-3.3-70b-versatile", "llama-3.1-8b-instant", "qwen/qwen3-32b", "moonshotai/kimi-k2-instruct"},
		NeedsKey: true,
	},
	"together": {
		Name:     "Together AI",
		Endpoint: "https://api.together.xyz/v1/chat/completions",
		Models:   []string{"meta-llama/Llama-3.3-70B-Instruct-Turbo", "Qwen/Qwen2.5-Coder-32B-Instruct"},
		NeedsKey: true,
	},
	"deepseek": {
		Name:     "DeepSeek",
		Endpoint: "https://api.deepseek.com/v1/chat/completions",
		Models:   []string{"deepseek-chat", "deepseek-coder", "deepseek-reasoner"},
		NeedsKey: true,
	},
	"ollama": {
		Name:     "Ollama (Local)",
		Endpoint: "http://localhost:11434/v1/chat/completions",
		Models:   []string{"qwen2.5-coder:7b", "llama3.1", "codellama", "mistral", "deepseek-coder-v2"},
		NeedsKey: false,
	},
	"lmstudio": {
		Name:     "LM Studio (Local)",
		Endpoint: "http://localhost:1234/v1/chat/completions",
		Models:   []string{"local-model"},
		NeedsKey: false,
	},
	"vllm": {
		Name:     "vLLM (Local)",
		Endpoint: "http://localhost:8000/v1/chat/completions",
		Models:   []string{"meta-llama/Meta-Llama-3-8B-Instruct"},
		NeedsKey: false,
	},
	"custom": {
		Name:     "Custom Endpoint",
		Endpoint: "",
		Models:   []string{},
		NeedsKey: false,
	},
}

type ProviderPreset struct {
	Name     string            `json:"name"`
	Endpoint string            `json:"endpoint"`
	Models   []string          `json:"models"`
	NeedsKey bool              `json:"needs_key"`
	Headers  map[string]string `json:"headers,omitempty"`
}

type UserConfig struct {
	Provider      string            `json:"provider"`
	Endpoint      string            `json:"endpoint"`
	Model         string            `json:"model"`
	APIKey        string            `json:"api_key"`
	MaxTokens     int               `json:"max_tokens"`
	HistoryBudget int               `json:"history_budget"`
	ShellTimeout  int               `json:"shell_timeout_seconds"`
	MaxTurns      int               `json:"max_turns"`
	ExtraHeaders  map[string]string `json:"extra_headers,omitempty"`
	Stream        bool              `json:"stream"`
	AutoApprove   bool              `json:"auto_approve"`
	AutoSave      bool              `json:"auto_save"`
	PlanMode      bool              `json:"plan_mode"`
}

func defaultUserConfig() *UserConfig {
	return &UserConfig{
		Provider:      "openai",
		Endpoint:      providerPresets["openai"].Endpoint,
		Model:         "gpt-4o-mini",
		MaxTokens:     8192,
		HistoryBudget: 24000,
		ShellTimeout:  60,
		MaxTurns:      80,
		ExtraHeaders:  make(map[string]string),
		Stream:        true,
		AutoApprove:   false,
		AutoSave:      true,
		PlanMode:      false,
	}
}

func (c *UserConfig) sanitize() {
	if c.Provider == "" {
		c.Provider = "openai"
	}
	if c.Endpoint == "" {
		if p, ok := providerPresets[c.Provider]; ok && p.Endpoint != "" {
			c.Endpoint = p.Endpoint
		}
	}
	if c.MaxTokens <= 0 {
		c.MaxTokens = defaultUserConfig().MaxTokens
	}
	if c.HistoryBudget <= 0 {
		c.HistoryBudget = defaultUserConfig().HistoryBudget
	}
	if c.ShellTimeout <= 0 {
		c.ShellTimeout = defaultUserConfig().ShellTimeout
	}
	if c.MaxTurns <= 0 {
		c.MaxTurns = defaultUserConfig().MaxTurns
	}
	if c.ExtraHeaders == nil {
		c.ExtraHeaders = make(map[string]string)
	}
}

func configPath() string {
	home, err := os.UserHomeDir()
	if err != nil {
		home = os.Getenv("HOME")
	}
	if home == "" {
		home = "."
	}
	dir := filepath.Join(home, ".config", "termux-agent")
	_ = os.MkdirAll(dir, 0700)
	return filepath.Join(dir, "config.json")
}

func loadUserConfig() *UserConfig {
	path := configPath()
	data, err := os.ReadFile(path)
	if err != nil {
		cfg := defaultUserConfig()
		if key := os.Getenv("OPENAI_API_KEY"); key != "" {
			cfg.APIKey = key
		}
		if key := os.Getenv("TERMUX_AGENT_API_KEY"); key != "" {
			cfg.APIKey = key
		}
		if ep := os.Getenv("OPENAI_API_ENDPOINT"); ep != "" {
			cfg.Endpoint = ep
			cfg.Provider = "custom"
		}
		if ep := os.Getenv("TERMUX_AGENT_ENDPOINT"); ep != "" {
			cfg.Endpoint = ep
			cfg.Provider = "custom"
		}
		if m := os.Getenv("TERMUX_AGENT_MODEL"); m != "" {
			cfg.Model = m
		}
		return cfg
	}
	var cfg UserConfig
	if err := json.Unmarshal(data, &cfg); err != nil {
		fmt.Fprintf(os.Stderr, "warning: %s is invalid (%v); using defaults\n", path, err)
		cfg2 := defaultUserConfig()
		if key := os.Getenv("OPENAI_API_KEY"); key != "" {
			cfg2.APIKey = key
		}
		return cfg2
	}
	cfg.sanitize()
	return &cfg
}

func saveUserConfig(cfg *UserConfig) error {
	path := configPath()
	data, err := json.MarshalIndent(cfg, "", "  ")
	if err != nil {
		return err
	}
	return os.WriteFile(path, data, 0600)
}

type ConfigManager struct {
	mu  sync.RWMutex
	cfg *UserConfig
}

func NewConfigManager() *ConfigManager {
	return &ConfigManager{
		cfg: loadUserConfig(),
	}
}

func (cm *ConfigManager) Get() *UserConfig {
	cm.mu.RLock()
	defer cm.mu.RUnlock()
	c := *cm.cfg
	c.ExtraHeaders = make(map[string]string, len(cm.cfg.ExtraHeaders))
	for k, v := range cm.cfg.ExtraHeaders {
		c.ExtraHeaders[k] = v
	}
	return &c
}

func (cm *ConfigManager) Reset() error {
	cm.mu.Lock()
	defer cm.mu.Unlock()
	cm.cfg = defaultUserConfig()
	return saveUserConfig(cm.cfg)
}

func (cm *ConfigManager) SetProvider(name string) error {
	preset, ok := providerPresets[name]
	if !ok {
		return fmt.Errorf("unknown provider: %s", name)
	}
	cm.mu.Lock()
	defer cm.mu.Unlock()
	cm.cfg.Provider = name
	if preset.Endpoint != "" {
		cm.cfg.Endpoint = preset.Endpoint
	}
	if len(preset.Models) > 0 {
		cm.cfg.Model = preset.Models[0]
	}
	cm.cfg.ExtraHeaders = make(map[string]string, len(preset.Headers))
	for k, v := range preset.Headers {
		cm.cfg.ExtraHeaders[k] = v
	}
	return saveUserConfig(cm.cfg)
}

func (cm *ConfigManager) SetEndpoint(endpoint string) error {
	cm.mu.Lock()
	defer cm.mu.Unlock()
	cm.cfg.Endpoint = strings.TrimSpace(endpoint)
	cm.cfg.Provider = "custom"
	return saveUserConfig(cm.cfg)
}

func (cm *ConfigManager) SetModel(model string) error {
	model = strings.TrimSpace(model)
	if model == "" {
		return fmt.Errorf("model name cannot be empty")
	}
	cm.mu.Lock()
	defer cm.mu.Unlock()
	cm.cfg.Model = model
	return saveUserConfig(cm.cfg)
}

func (cm *ConfigManager) SetAPIKey(key string) error {
	cm.mu.Lock()
	defer cm.mu.Unlock()
	cm.cfg.APIKey = strings.TrimSpace(key)
	return saveUserConfig(cm.cfg)
}

func (cm *ConfigManager) SetMaxTokens(n int) error {
	if n <= 0 {
		return fmt.Errorf("max-tokens must be positive (got %d)", n)
	}
	cm.mu.Lock()
	defer cm.mu.Unlock()
	cm.cfg.MaxTokens = n
	return saveUserConfig(cm.cfg)
}

func (cm *ConfigManager) SetHistoryBudget(n int) error {
	if n <= 0 {
		return fmt.Errorf("history-budget must be positive (got %d)", n)
	}
	cm.mu.Lock()
	defer cm.mu.Unlock()
	cm.cfg.HistoryBudget = n
	return saveUserConfig(cm.cfg)
}

func (cm *ConfigManager) SetShellTimeout(sec int) error {
	if sec <= 0 {
		return fmt.Errorf("shell-timeout must be at least 1 second (got %d)", sec)
	}
	cm.mu.Lock()
	defer cm.mu.Unlock()
	cm.cfg.ShellTimeout = sec
	return saveUserConfig(cm.cfg)
}

func (cm *ConfigManager) SetMaxTurns(n int) error {
	if n <= 0 {
		return fmt.Errorf("max-turns must be positive (got %d)", n)
	}
	cm.mu.Lock()
	defer cm.mu.Unlock()
	cm.cfg.MaxTurns = n
	return saveUserConfig(cm.cfg)
}

func (cm *ConfigManager) SetStream(on bool) error {
	cm.mu.Lock()
	defer cm.mu.Unlock()
	cm.cfg.Stream = on
	return saveUserConfig(cm.cfg)
}

func (cm *ConfigManager) SetAutoApprove(on bool) error {
	cm.mu.Lock()
	defer cm.mu.Unlock()
	cm.cfg.AutoApprove = on
	return saveUserConfig(cm.cfg)
}

func (cm *ConfigManager) SetAutoSave(on bool) error {
	cm.mu.Lock()
	defer cm.mu.Unlock()
	cm.cfg.AutoSave = on
	return saveUserConfig(cm.cfg)
}

func (cm *ConfigManager) SetPlanMode(on bool) error {
	cm.mu.Lock()
	defer cm.mu.Unlock()
	cm.cfg.PlanMode = on
	return saveUserConfig(cm.cfg)
}

func (cm *ConfigManager) SetExtraHeader(key, value string) error {
	key = strings.TrimSpace(key)
	if key == "" {
		return fmt.Errorf("header key cannot be empty")
	}
	cm.mu.Lock()
	defer cm.mu.Unlock()
	cm.cfg.ExtraHeaders[key] = value
	return saveUserConfig(cm.cfg)
}

func (cm *ConfigManager) DeleteExtraHeader(key string) error {
	cm.mu.Lock()
	defer cm.mu.Unlock()
	delete(cm.cfg.ExtraHeaders, key)
	return saveUserConfig(cm.cfg)
}

func (cm *ConfigManager) ToAgentConfig() AgentConfig {
	cm.mu.RLock()
	defer cm.mu.RUnlock()
	extra := make(map[string]string, len(cm.cfg.ExtraHeaders))
	for k, v := range cm.cfg.ExtraHeaders {
		extra[k] = v
	}
	return AgentConfig{
		APIKey:        cm.cfg.APIKey,
		Endpoint:      cm.cfg.Endpoint,
		Model:         cm.cfg.Model,
		MaxTokens:     cm.cfg.MaxTokens,
		HistoryBudget: cm.cfg.HistoryBudget,
		ShellTimeout:  time.Duration(cm.cfg.ShellTimeout) * time.Second,
		MaxTurns:      cm.cfg.MaxTurns,
		ExtraHeaders:  extra,
		Stream:        cm.cfg.Stream,
		PlanMode:      cm.cfg.PlanMode,
		AutoApprove:   cm.cfg.AutoApprove,
		AutoSave:      cm.cfg.AutoSave,
		Provider:      cm.cfg.Provider,
	}
}

func (cm *ConfigManager) Status() string {
	cm.mu.RLock()
	defer cm.mu.RUnlock()
	keyHint := "(not set)"
	if cm.cfg.APIKey != "" {
		keyHint = "(set)"
	}
	onOff := func(b bool) string {
		if b {
			return "on"
		}
		return "off"
	}
	return fmt.Sprintf(
		"Provider: %s\nEndpoint: %s\nModel: %s\nAPI Key: %s\nMax Tokens: %d\nHistory Budget: %d\nShell Timeout: %ds\nMax Turns: %d\nStream: %s\nAuto-approve: %s\nAuto-save: %s\nPlan mode: %s",
		cm.cfg.Provider, cm.cfg.Endpoint, cm.cfg.Model, keyHint,
		cm.cfg.MaxTokens, cm.cfg.HistoryBudget, cm.cfg.ShellTimeout, cm.cfg.MaxTurns,
		onOff(cm.cfg.Stream), onOff(cm.cfg.AutoApprove), onOff(cm.cfg.AutoSave), onOff(cm.cfg.PlanMode),
	)
}

func parseIntOr(s string, def int) int {
	v, err := strconv.Atoi(strings.TrimSpace(s))
	if err != nil {
		return def
	}
	return v
}

func parseBoolArg(s string) (bool, bool) {
	switch strings.ToLower(strings.TrimSpace(s)) {
	case "1", "true", "on", "yes", "y":
		return true, true
	case "0", "false", "off", "no", "n":
		return false, true
	default:
		return false, false
	}
}
