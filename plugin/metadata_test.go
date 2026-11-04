package main

import (
	"net/url"
	"reflect"
	"regexp"
	"strings"
	"testing"

	"github.com/router-for-me/CLIProxyAPI/v8/sdk/pluginapi"
)

// TestMetadataIdentityFields covers the mandatory registration fields: CPA
// drops a plugin whose Metadata is missing any of them (pluginhost.validPlugin).
func TestMetadataIdentityFields(t *testing.T) {
	meta := (&pluginState{}).metadata()
	for name, value := range map[string]string{
		"Name":             meta.Name,
		"Version":          meta.Version,
		"Author":           meta.Author,
		"GitHubRepository": meta.GitHubRepository,
		"Logo":             meta.Logo,
	} {
		if strings.TrimSpace(value) == "" {
			t.Fatalf("metadata %s is empty; the host rejects plugins without it", name)
		}
	}
	if meta.Name != pluginName {
		t.Fatalf("metadata Name = %q, want the plugin id %q", meta.Name, pluginName)
	}
	if !regexp.MustCompile(`^[0-9][0-9A-Za-z.+-]*$`).MatchString(meta.Version) {
		t.Fatalf("metadata Version %q is not a valid plugin version", meta.Version)
	}
	if !regexp.MustCompile(`^[0-9]+\.[0-9]+\.[0-9]+$`).MatchString(meta.Version) {
		t.Fatalf("metadata Version %q must be dotted numeric: the store derives it from a v<version> tag", meta.Version)
	}
	if meta.GitHubRepository != pluginRepo {
		t.Fatalf("metadata GitHubRepository = %q, want %q", meta.GitHubRepository, pluginRepo)
	}
}

// TestMetadataLogoIsServableURL keeps the display asset resolvable by the
// management UI and the plugin store: an absolute https URL, not a local path.
func TestMetadataLogoIsServableURL(t *testing.T) {
	logo := (&pluginState{}).metadata().Logo
	parsed, err := url.Parse(logo)
	if err != nil {
		t.Fatalf("logo %q does not parse: %v", logo, err)
	}
	if parsed.Scheme != "https" || parsed.Host == "" {
		t.Fatalf("logo %q must be an absolute https URL", logo)
	}
	if !strings.HasPrefix(parsed.Path, "/LIEGU0317/cpa-plugin-model-meta-sync/") {
		t.Fatalf("logo %q must be served from this repository", logo)
	}
	if !strings.HasSuffix(parsed.Path, ".png") && !strings.HasSuffix(parsed.Path, ".svg") {
		t.Fatalf("logo %q must point at an image asset", logo)
	}
}

// TestConfigFieldsCoverConfigKeys keeps the advertised configuration surface in
// sync with what the plugin actually parses: a stale or missing ConfigField
// means the management UI renders a form that does not match config.yaml.
func TestConfigFieldsCoverConfigKeys(t *testing.T) {
	meta := (&pluginState{}).metadata()
	advertised := make(map[string]pluginapi.ConfigField, len(meta.ConfigFields))
	for _, field := range meta.ConfigFields {
		if strings.TrimSpace(field.Name) == "" {
			t.Fatal("ConfigFields contains an empty Name")
		}
		if strings.TrimSpace(field.Description) == "" {
			t.Fatalf("ConfigField %q has no Description", field.Name)
		}
		if _, exists := advertised[field.Name]; exists {
			t.Fatalf("ConfigField %q is declared twice", field.Name)
		}
		advertised[field.Name] = field
	}

	declared := make(map[string]bool)
	configType := reflect.TypeOf(Config{})
	for i := 0; i < configType.NumField(); i++ {
		tag := configType.Field(i).Tag.Get("yaml")
		key := strings.Split(tag, ",")[0]
		if key == "" || key == "-" {
			continue
		}
		declared[key] = true
		if _, ok := advertised[key]; !ok {
			t.Errorf("Config key %q is parsed but missing from ConfigFields", key)
		}
	}
	for name := range advertised {
		if !declared[name] {
			t.Errorf("ConfigField %q is advertised but no Config field parses it", name)
		}
	}
}

// TestConfigFieldDefaultsMatchParser keeps the defaults the management UI shows
// identical to the values parseConfig applies to an empty configuration.
func TestConfigFieldDefaultsMatchParser(t *testing.T) {
	meta := (&pluginState{}).metadata()
	cfg, err := parseConfig(nil)
	if err != nil {
		t.Fatalf("parseConfig(nil): %v", err)
	}
	want := map[string]any{
		"mode":                 cfg.Mode,
		"timeout_seconds":      cfg.TimeoutSeconds,
		"backup_keep":          cfg.BackupKeep,
		"sync_on_start":        cfg.SyncOnStart,
		"dry_run_on_start":     cfg.DryRunOnStart,
		"interval_minutes":     cfg.IntervalMinutes,
		"fields":               cfg.Fields,
		"overwrite_fields":     []string{},
		"models_paths":         map[string]string{},
		"upstream_models_path": cfg.UpstreamModelsPath,
	}
	for _, field := range meta.ConfigFields {
		expected, checked := want[field.Name]
		if !checked {
			continue
		}
		if !reflect.DeepEqual(field.Default, expected) {
			t.Errorf("ConfigField %q default = %#v, want %#v", field.Name, field.Default, expected)
		}
	}

	for _, field := range meta.ConfigFields {
		if field.Type != pluginapi.ConfigFieldTypeEnum {
			continue
		}
		if len(field.EnumValues) == 0 {
			t.Fatalf("enum ConfigField %q has no EnumValues", field.Name)
		}
		defaultValue, _ := field.Default.(string)
		found := false
		for _, candidate := range field.EnumValues {
			if candidate == defaultValue {
				found = true
			}
		}
		if !found {
			t.Fatalf("enum ConfigField %q default %q is not one of %v", field.Name, defaultValue, field.EnumValues)
		}
	}
}
