package main

import (
	"fmt"
	"strings"
	"time"

	"gopkg.in/yaml.v3"
)

const (
	defaultConfigPath  = "/srv/cliproxyapi/config.yaml"
	defaultTimeoutSec  = 20
	defaultBackupKeep  = 10
	defaultUpstreamDir = "/v1/models"
)

var allFields = []string{"display-name", "max-context-length", "input-modalities", "output-modalities", "thinking"}

// Config is the plugin-owned configuration subtree (plugins.configs.<id>).
type Config struct {
	// ConfigPath is the absolute path of the CPA config.yaml that owns the provider entries.
	ConfigPath string `yaml:"config_path"`
	// Providers is an optional name filter. Empty means every enabled
	// openai-compatibility provider in the config is synced; no vendor is
	// hard-coded. Matching is by provider name only.
	Providers []string `yaml:"providers"`
	// IncludeDisabled also syncs providers marked disabled in the config.
	IncludeDisabled bool `yaml:"include_disabled"`
	// Mode is "missing" (fill empty fields only) or "always" (recompute every listed field).
	Mode string `yaml:"mode"`
	// Fields restricts which model attributes are synced. Empty means all.
	Fields []string `yaml:"fields"`
	// OverwriteFields lists fields that are authoritative upstream even in
	// "missing" mode. Use it for display names and limits while leaving
	// hand-tuned reasoning levels alone.
	OverwriteFields []string `yaml:"overwrite_fields"`
	// TimeoutSeconds bounds one upstream /v1/models request.
	TimeoutSeconds int `yaml:"timeout_seconds"`
	// BackupKeep is the number of timestamped backups retained next to config.yaml.
	BackupKeep int `yaml:"backup_keep"`
	// SyncOnStart runs one sync shortly after the host loads the plugin.
	SyncOnStart bool `yaml:"sync_on_start"`
	// DryRunOnStart turns the startup sync into a preview: it fetches and
	// reports, but writes nothing. Useful before rolling out to every provider.
	DryRunOnStart bool `yaml:"dry_run_on_start"`
	// IntervalMinutes, when positive, repeats the sync on a timer.
	IntervalMinutes int `yaml:"interval_minutes"`
	// APIKey optionally overrides the provider key used to read the upstream model list.
	APIKey string `yaml:"api_key"`
	// UpstreamModelsPath overrides the model-list path appended to the provider base URL.
	UpstreamModelsPath string `yaml:"upstream_models_path"`
}

func parseConfig(raw []byte) (Config, error) {
	cfg := Config{}
	if len(strings.TrimSpace(string(raw))) > 0 {
		if err := yaml.Unmarshal(raw, &cfg); err != nil {
			return Config{}, fmt.Errorf("parse plugin config: %w", err)
		}
	}
	if strings.TrimSpace(cfg.ConfigPath) == "" {
		cfg.ConfigPath = defaultConfigPath
	}
	cfg.Mode = strings.ToLower(strings.TrimSpace(cfg.Mode))
	switch cfg.Mode {
	case "", "missing":
		cfg.Mode = "missing"
	case "always", "overwrite", "refresh":
		cfg.Mode = "always"
	default:
		return Config{}, fmt.Errorf("invalid mode %q: want missing or always", cfg.Mode)
	}
	cfg.Fields = normalizeFields(cfg.Fields)
	// An absent overwrite list means "never overwrite"; it must not inherit the
	// empty-list-means-all-fields rule that normalizeFields applies to fields.
	if len(cfg.OverwriteFields) > 0 {
		normalized := normalizeFields(cfg.OverwriteFields)
		filtered := make([]string, 0, len(normalized))
		for _, field := range normalized {
			if cfg.hasField(field) {
				filtered = append(filtered, field)
			}
		}
		cfg.OverwriteFields = filtered
	} else {
		cfg.OverwriteFields = nil
	}
	if cfg.TimeoutSeconds <= 0 {
		cfg.TimeoutSeconds = defaultTimeoutSec
	}
	if cfg.BackupKeep <= 0 {
		cfg.BackupKeep = defaultBackupKeep
	}
	if cfg.IntervalMinutes < 0 {
		cfg.IntervalMinutes = 0
	}
	if strings.TrimSpace(cfg.UpstreamModelsPath) == "" {
		cfg.UpstreamModelsPath = defaultUpstreamDir
	}
	cfg.APIKey = strings.TrimSpace(cfg.APIKey)
	for i := range cfg.Providers {
		cfg.Providers[i] = strings.TrimSpace(cfg.Providers[i])
	}
	return cfg, nil
}

func normalizeFields(fields []string) []string {
	if len(fields) == 0 {
		out := make([]string, len(allFields))
		copy(out, allFields)
		return out
	}
	seen := map[string]bool{}
	out := make([]string, 0, len(fields))
	for _, item := range fields {
		name := strings.ToLower(strings.TrimSpace(item))
		for _, allowed := range allFields {
			if name == allowed && !seen[allowed] {
				seen[allowed] = true
				out = append(out, allowed)
			}
		}
	}
	if len(out) == 0 {
		out = append(out, allFields...)
	}
	return out
}

// withDefaults fills in values that parseConfig would have supplied so a
// programmatically constructed Config (tests, future callers) stays valid.
func (c Config) withDefaults() Config {
	if strings.TrimSpace(c.ConfigPath) == "" {
		c.ConfigPath = defaultConfigPath
	}
	if strings.TrimSpace(c.Mode) == "" {
		c.Mode = "missing"
	}
	if len(c.Fields) == 0 {
		c.Fields = normalizeFields(nil)
	}
	if c.TimeoutSeconds <= 0 {
		c.TimeoutSeconds = defaultTimeoutSec
	}
	if c.BackupKeep <= 0 {
		c.BackupKeep = defaultBackupKeep
	}
	if strings.TrimSpace(c.UpstreamModelsPath) == "" {
		c.UpstreamModelsPath = defaultUpstreamDir
	}
	return c
}

// overwrites reports whether field replaces an existing value in "missing" mode.
func (c Config) overwrites(field string) bool {
	if c.Mode == "always" {
		return true
	}
	for _, item := range c.OverwriteFields {
		if item == field {
			return true
		}
	}
	return false
}

func (c Config) hasField(name string) bool {
	for _, item := range c.Fields {
		if item == name {
			return true
		}
	}
	return false
}

// wantsProvider matches by provider name only. An empty filter means every
// provider is eligible; no address, host or IP participates in the decision.
func (c Config) wantsProvider(name string) bool {
	if len(c.Providers) == 0 {
		return true
	}
	for _, item := range c.Providers {
		if strings.EqualFold(strings.TrimSpace(item), strings.TrimSpace(name)) {
			return true
		}
	}
	return false
}

func (c Config) timeout() time.Duration {
	return time.Duration(c.TimeoutSeconds) * time.Second
}
