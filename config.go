// Package sikasa: config.go
// Purpose: Defines the centralized configuration system for the sikasa bot library,
// allowing settings to be loaded from YAML files instead of relying on hardcoded defaults.
//
// Key Components:
//   - Config: Main configuration struct mapping to YAML layout.
//   - DefaultConfig(): Returns a Config instance with sensible defaults.
//   - LoadConfig(): Parses a YAML file into a Config struct.

package sikasa

import (
	"fmt"
	"os"
	"time"

	"gopkg.in/yaml.v3"
)

// VoiceConfig contains settings for voice connection and recovery.
type VoiceConfig struct {
	ReconnectDebounce time.Duration `yaml:"reconnect_debounce"`
	MaxReconnects     int           `yaml:"max_reconnects"`
}

// CacheConfig contains settings for the audio prefetching cache.
type CacheConfig struct {
	Dir      string `yaml:"dir"`
	MaxAhead int    `yaml:"max_ahead"`
	Enabled  bool   `yaml:"enabled"`
}

// SystemConfig contains low-level and diagnostic settings.
type SystemConfig struct {
	MusicLogInterval time.Duration `yaml:"music_log_interval"`
	DevUserIDs       []string      `yaml:"dev_user_ids"`
}

// AIProviderConfig contains settings for an individual AI provider.
type AIProviderConfig struct {
	Name     string `yaml:"name"`
	APIKey   string `yaml:"api_key"`
	Endpoint string `yaml:"endpoint"`
	Model    string `yaml:"model"`
	TokenCap int    `yaml:"token_cap"`
}

// AIConfig contains settings for OpenAI-compatible completion.
type AIConfig struct {
	Enabled         bool               `yaml:"enabled"`
	DefaultProvider string             `yaml:"default_provider"`
	SystemPrompt    string             `yaml:"system_prompt"`
	TokenCap        int                `yaml:"token_cap"`
	Providers       []AIProviderConfig `yaml:"providers"`
}

// Config represents the top-level configuration for the bot.
type Config struct {
	Voice  VoiceConfig  `yaml:"voice"`
	Cache  CacheConfig  `yaml:"cache"`
	System SystemConfig `yaml:"system"`
	AI     AIConfig     `yaml:"ai"`
}

// DefaultConfig returns a configuration struct with standard defaults.
func DefaultConfig() *Config {
	return &Config{
		Voice: VoiceConfig{
			ReconnectDebounce: 30 * time.Second,
			MaxReconnects:     3,
		},
		Cache: CacheConfig{
			Dir:      "sikasa-data/audiocache",
			MaxAhead: 3,
			Enabled:  true,
		},
		System: SystemConfig{
			MusicLogInterval: 5 * time.Second,
			DevUserIDs:       []string{},
		},
		AI: AIConfig{
			Enabled:         true,
			DefaultProvider: "openai",
			SystemPrompt:    "You are a helpful assistant.",
			TokenCap:        2048,
			Providers: []AIProviderConfig{
				{
					Name:     "openai",
					APIKey:   "",
					Endpoint: "https://api.openai.com/v1",
					Model:    "gpt-4o-mini",
					TokenCap: 2048,
				},
			},
		},
	}
}

// LoadConfig parses a YAML configuration file from the given path.
func LoadConfig(path string) (*Config, error) {
	b, err := os.ReadFile(path)
	if err != nil {
		return nil, fmt.Errorf("read config file: %w", err)
	}

	cfg := DefaultConfig()
	if err := yaml.Unmarshal(b, cfg); err != nil {
		return nil, fmt.Errorf("parse config file: %w", err)
	}

	return cfg, nil
}
