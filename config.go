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

var providerPresets = map[string]ProviderPreset{
	"openai": {
		Name:     "OpenAI",
		Endpoint: "https://api.openai.com/v1/chat/completions",
		Models:   []string{"gpt-4o", "gpt-4o-mini", "gpt-4-turbo", "gpt-3.5-turbo"},
		NeedsKey: true,
	},
	"openrouter": {
		Name:     "OpenRouter",
		Endpoint: "https://openrouter.ai/api/v1/chat/completions",
		Models:   []string{"anthropic/claude-3.5-sonnet", "meta-llama/llama-3.1-70b-instruct", "openai/gpt-4o-mini", "google/gemini-flash-1.5"},
		NeedsKey: true,
		Headers:  map[string]string{"HTTP-Referer": "https://localhost", "X-Title": "TermuxAgent"},
	},
	"groq": {
		Name:     "Groq",
		Endpoint: "https://api.groq.com/openai/v1/chat/completions",
		Models:   []string{"llama3-70b-8192", "llama3-8b-8192", "mixtral-8x7b-32768", "gemma2-9b-it"},
		NeedsKey: true,
	},
	"together": {
		Name:     "Together AI",
		Endpoint: "https://api.together.xyz/v1/chat/completions",
		Models:   []string{"meta-llama/Llama-3.1-70B-Instruct-Turbo", "mistralai/Mixtral-8x22B-Instruct-v0.1"},
		NeedsKey: true,
	},
	"ollama": {
		Name:     "Ollama (Local)",
		Endpoint: "http://localhost:11434/v1/chat/completions",
		Models:   []string{"llama3.1", "codellama", "mistral", "qwen2.5"},
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
}

func defaultUserConfig() *UserConfig {
	return &UserConfig{
		Provider:      "openai",
		Endpoint:      providerPresets["openai"].Endpoint,
		Model:         "gpt-4o-mini",
		MaxTokens:     1024,
		HistoryBudget: 8000,
		ShellTimeout:  15,
		MaxTurns:      10,
		ExtraHeaders:  make(map[string]string),
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
		if ep := os.Getenv("OPENAI_API_ENDPOINT"); ep != "" {
			cfg.Endpoint = ep
			cfg.Provider = "custom"
		}
		return cfg
	}
	var cfg UserConfig
	if err := json.Unmarshal(data, &cfg); err != nil {
		cfg2 := defaultUserConfig()
		if key := os.Getenv("OPENAI_API_KEY"); key != "" {
			cfg2.APIKey = key
		}
		return cfg2
	}
	if cfg.ExtraHeaders == nil {
		cfg.ExtraHeaders = make(map[string]string)
	}
	if cfg.Provider == "" {
		cfg.Provider = "openai"
	}
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
	copy := *cm.cfg
	copy.ExtraHeaders = make(map[string]string)
	for k, v := range cm.cfg.ExtraHeaders {
		copy.ExtraHeaders[k] = v
	}
	return &copy
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
	cm.cfg.ExtraHeaders = make(map[string]string)
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
	cm.mu.Lock()
	defer cm.mu.Unlock()
	cm.cfg.Model = strings.TrimSpace(model)
	return saveUserConfig(cm.cfg)
}

func (cm *ConfigManager) SetAPIKey(key string) error {
	cm.mu.Lock()
	defer cm.mu.Unlock()
	cm.cfg.APIKey = strings.TrimSpace(key)
	return saveUserConfig(cm.cfg)
}

func (cm *ConfigManager) SetMaxTokens(n int) error {
	cm.mu.Lock()
	defer cm.mu.Unlock()
	cm.cfg.MaxTokens = n
	return saveUserConfig(cm.cfg)
}

func (cm *ConfigManager) SetHistoryBudget(n int) error {
	cm.mu.Lock()
	defer cm.mu.Unlock()
	cm.cfg.HistoryBudget = n
	return saveUserConfig(cm.cfg)
}

func (cm *ConfigManager) SetShellTimeout(sec int) error {
	cm.mu.Lock()
	defer cm.mu.Unlock()
	cm.cfg.ShellTimeout = sec
	return saveUserConfig(cm.cfg)
}

func (cm *ConfigManager) SetMaxTurns(n int) error {
	cm.mu.Lock()
	defer cm.mu.Unlock()
	cm.cfg.MaxTurns = n
	return saveUserConfig(cm.cfg)
}

func (cm *ConfigManager) SetExtraHeader(key, value string) error {
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
	extra := make(map[string]string)
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
	}
}

func (cm *ConfigManager) Status() string {
	cm.mu.RLock()
	defer cm.mu.RUnlock()
	keyHint := "(not set)"
	if cm.cfg.APIKey != "" {
		keyHint = "(set)"
	}
	return fmt.Sprintf(
		"Provider: %s
Endpoint: %s
Model: %s
API Key: %s
Max Tokens: %d
History Budget: %d
Shell Timeout: %ds
Max Turns: %d",
		cm.cfg.Provider, cm.cfg.Endpoint, cm.cfg.Model, keyHint,
		cm.cfg.MaxTokens, cm.cfg.HistoryBudget, cm.cfg.ShellTimeout, cm.cfg.MaxTurns,
	)
}

func parseIntOr(s string, def int) int {
	v, err := strconv.Atoi(strings.TrimSpace(s))
	if err != nil {
		return def
	}
	return v
}
