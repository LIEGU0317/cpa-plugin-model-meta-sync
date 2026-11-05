package main

import (
	"fmt"
	"os"
	"path/filepath"
	"strconv"
	"strings"
	"time"

	"gopkg.in/yaml.v3"
)

const (
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
	// APIKey is the deprecated single key override. It is only accepted when
	// Providers names exactly one provider and is folded into APIKeys for it:
	// a key sent to every provider leaks to third-party gateways.
	APIKey string `yaml:"api_key"`
	// APIKeys maps a provider name to the key used to read its model list,
	// overriding the provider's first configured key.
	APIKeys map[string]string `yaml:"api_keys"`
	// UpstreamModelsPath overrides the model-list path appended to the provider base URL.
	UpstreamModelsPath string `yaml:"upstream_models_path"`
	// ModelsPaths maps a provider name to its model-list path. Non-standard
	// gateways (for example one rooted at /v2 that only serves /models) need it;
	// everything else uses the automatic /v1/models -> /models fallback.
	ModelsPaths map[string]string `yaml:"models_paths"`
}

func parseConfig(raw []byte) (Config, error) {
	// sync_on_start defaults to true: the documented contract is that applying
	// models in the CPAMC editor enriches them in the same config reload. A
	// struct decoded over this zero value keeps true when the key is absent and
	// takes the explicit value when it is present.
	cfg := Config{SyncOnStart: true}
	if len(strings.TrimSpace(string(raw))) > 0 {
		if err := yaml.Unmarshal(raw, &cfg); err != nil {
			return Config{}, fmt.Errorf("parse plugin config: %w", err)
		}
	}
	cfg.ConfigPath = strings.TrimSpace(cfg.ConfigPath)
	cfg.Mode = strings.ToLower(strings.TrimSpace(cfg.Mode))
	switch cfg.Mode {
	case "", "missing":
		cfg.Mode = "missing"
	case "always", "overwrite", "refresh":
		cfg.Mode = "always"
	default:
		return Config{}, fmt.Errorf("invalid mode %q: want missing or always", cfg.Mode)
	}
	fields, errFields := normalizeFields(cfg.Fields)
	if errFields != nil {
		return Config{}, fmt.Errorf("fields: %w", errFields)
	}
	cfg.Fields = fields
	// An absent overwrite list means "never overwrite"; it must not inherit the
	// empty-list-means-all-fields rule that normalizeFields applies to fields.
	if len(cfg.OverwriteFields) > 0 {
		normalized, errOverwrite := normalizeFields(cfg.OverwriteFields)
		if errOverwrite != nil {
			return Config{}, fmt.Errorf("overwrite_fields: %w", errOverwrite)
		}
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
	keys := make(map[string]string, len(cfg.APIKeys)+1)
	for name, key := range cfg.APIKeys {
		name, key = strings.TrimSpace(name), strings.TrimSpace(key)
		if name == "" || key == "" {
			return Config{}, fmt.Errorf("api_keys: entries need a provider name and a key")
		}
		keys[name] = key
	}
	if cfg.APIKey != "" {
		if len(cfg.Providers) != 1 || cfg.Providers[0] == "" {
			return Config{}, fmt.Errorf("api_key must be scoped to one provider (providers: [<name>]); use api_keys: {<provider>: <key>} instead")
		}
		if _, exists := keys[cfg.Providers[0]]; !exists {
			keys[cfg.Providers[0]] = cfg.APIKey
		}
	}
	cfg.APIKeys = keys
	return cfg, nil
}

// keyOverride returns the configured key override for a provider, matched by
// name the same way as the provider filter.
func (c Config) keyOverride(provider string) string {
	for name, key := range c.APIKeys {
		if strings.EqualFold(strings.TrimSpace(name), strings.TrimSpace(provider)) {
			return key
		}
	}
	return ""
}

// normalizeFields canonicalises a field list. An empty list means every field;
// an unknown name is an error rather than a silent widening, because the same
// helper parses overwrite_fields, where widening means overwriting everything.
func normalizeFields(fields []string) ([]string, error) {
	if len(fields) == 0 {
		return append([]string(nil), allFields...), nil
	}
	seen := map[string]bool{}
	out := make([]string, 0, len(fields))
	unknown := make([]string, 0)
	for _, item := range fields {
		name := strings.ToLower(strings.TrimSpace(item))
		if !containsString(allFields, name) {
			unknown = append(unknown, strconv.Quote(item))
			continue
		}
		if !seen[name] {
			seen[name] = true
			out = append(out, name)
		}
	}
	if len(unknown) > 0 {
		return nil, fmt.Errorf("unknown field name(s) %s; valid: %s", strings.Join(unknown, ", "), strings.Join(allFields, ", "))
	}
	return out, nil
}

// withDefaults fills in values that parseConfig would have supplied so a
// programmatically constructed Config (tests, future callers) stays valid.
func (c Config) withDefaults() Config {
	c.ConfigPath = strings.TrimSpace(c.ConfigPath)
	if strings.TrimSpace(c.Mode) == "" {
		c.Mode = "missing"
	}
	if len(c.Fields) == 0 {
		c.Fields = append([]string(nil), allFields...)
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

// modelsPathCandidates returns the model-list paths to try, in order. The
// second entry mirrors CPA's own "fetch from endpoint" discovery, which appends
// /models to the base URL: gateways such as 讯飞星火 serve /v2/models and 404 on
// /v2/v1/models.
func (c Config) modelsPathCandidates(provider string) []string {
	for name, path := range c.ModelsPaths {
		if strings.EqualFold(strings.TrimSpace(name), strings.TrimSpace(provider)) {
			if trimmed := strings.TrimSpace(path); trimmed != "" {
				return []string{trimmed}
			}
		}
	}
	seen := map[string]bool{}
	out := make([]string, 0, 3)
	for _, candidate := range []string{c.UpstreamModelsPath, defaultUpstreamDir, "/models"} {
		trimmed := strings.TrimSpace(candidate)
		if trimmed == "" || seen[trimmed] {
			continue
		}
		seen[trimmed] = true
		out = append(out, trimmed)
	}
	return out
}

func (c Config) timeout() time.Duration {
	return time.Duration(c.TimeoutSeconds) * time.Second
}

// resolveConfigPath finds the CPA config that owns the provider entries.
//
// config_path is optional. When it is absent the path is taken from the host
// process itself: the -config flag it was started with, otherwise config.yaml
// in its working directory - exactly the fallback CPA uses. The host ABI does
// not expose config read/write, which is why the plugin has to locate the file
// rather than ask for it.
func resolveConfigPath(configured string) (string, error) {
	configured = strings.TrimSpace(configured)
	if configured != "" {
		return configured, nil
	}
	wd, errGetwd := os.Getwd()
	if errGetwd != nil {
		return "", fmt.Errorf("cannot resolve config path: %w", errGetwd)
	}
	if fromFlag := configPathFromArgs(os.Args[1:]); fromFlag != "" {
		if !filepath.IsAbs(fromFlag) {
			fromFlag = filepath.Join(wd, fromFlag)
		}
		return fromFlag, nil
	}
	candidate := filepath.Join(wd, "config.yaml")
	if _, errStat := os.Stat(candidate); errStat == nil {
		return candidate, nil
	}
	return "", fmt.Errorf("cannot locate CPA config.yaml in %s: set config_path", wd)
}

// configPathFromArgs extracts -config/--config from the host command line.
func configPathFromArgs(args []string) string {
	for i := 0; i < len(args); i++ {
		arg := args[i]
		name, value, hasValue := strings.Cut(arg, "=")
		if name != "-config" && name != "--config" {
			continue
		}
		if hasValue {
			return strings.TrimSpace(value)
		}
		if i+1 < len(args) {
			return strings.TrimSpace(args[i+1])
		}
	}
	return ""
}
