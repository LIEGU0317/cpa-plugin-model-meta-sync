package main

import (
	"os"
	"path/filepath"
	"testing"
)

func model(id, display string) upstreamModel {
	return upstreamModel{ID: id, DisplayName: display}
}

func TestLookupExactSpellings(t *testing.T) {
	index := buildUpstreamIndex([]upstreamModel{model("auto/model-a", "A")})
	for _, name := range []string{"auto/model-a", "AUTO/MODEL-A"} {
		if got, ok := index.lookup(name, ""); !ok || got.DisplayName != "A" {
			t.Fatalf("lookup(%q) = %+v, %v", name, got, ok)
		}
	}
	if got, ok := index.lookup("model-a", ""); !ok || got.DisplayName != "A" {
		// bare-name matching is allowed (single unambiguous id), so this one
		// must resolve.
		t.Fatalf("bare name should resolve inside one provider: %+v %v", got, ok)
	}
}

func TestLookupBareNameAcrossNamespaces(t *testing.T) {
	index := buildUpstreamIndex([]upstreamModel{model("vendor/deepseek-v4.1-flash", "DeepSeek")})
	got, ok := index.lookup("deepseek-v4.1-flash", "")
	if !ok || got.ID != "vendor/deepseek-v4.1-flash" {
		t.Fatalf("bare-name lookup failed: %+v %v", got, ok)
	}
}

func TestLookupFoldsSeparators(t *testing.T) {
	index := buildUpstreamIndex([]upstreamModel{model("vendor/DeepSeek-V4.1-Flash", "DeepSeek")})
	for _, name := range []string{"deepseek-v41-flash", "deepseek_v4.1_flash", "DeepSeek V41 Flash"} {
		if got, ok := index.lookup(name, ""); !ok || got.ID != "vendor/DeepSeek-V4.1-Flash" {
			t.Fatalf("folded lookup(%q) failed: %+v %v", name, got, ok)
		}
	}
}

func TestLookupCatalogSuffixDecoration(t *testing.T) {
	index := buildUpstreamIndex([]upstreamModel{model("deepseek-v4-flash-vision-exp", "Vision")})
	got, ok := index.lookup("DeepSeek-V4-Flash-Vision", "")
	if !ok || got.DisplayName != "Vision" {
		t.Fatalf("suffix decoration lookup failed: %+v %v", got, ok)
	}
	// The direction is one-way: a configured "-free" name is a different endpoint.
	if _, ok := index.lookup("deepseek-v4-flash-vision-free", ""); ok {
		t.Fatal("configured -free must not match an -exp catalog entry")
	}
}

// The catalog may decorate an id the config leaves bare, never the reverse: a
// configured "-preview" is its own endpoint and must not inherit the metadata
// of the plain upstream model.
func TestLookupSuffixOnConfiguredNameDoesNotMatchPlainUpstream(t *testing.T) {
	index := buildUpstreamIndex([]upstreamModel{model("gpt-5", "GPT-5")})
	for _, name := range []string{"gpt-5-preview", "GPT_5_Latest", "gpt-5-free"} {
		if got, ok := index.lookup(name, ""); ok {
			t.Fatalf("configured %q must not match plain upstream %q", name, got.ID)
		}
	}
	// The intended direction keeps working.
	index = buildUpstreamIndex([]upstreamModel{model("glm-4-flash-free", "Free")})
	if got, ok := index.lookup("GLM-4-Flash", ""); !ok || got.ID != "glm-4-flash-free" {
		t.Fatalf("bare configured name must match a decorated catalog id: %+v %v", got, ok)
	}
}

func TestLookupDeploymentQualifier(t *testing.T) {
	index := buildUpstreamIndex([]upstreamModel{model("deepseek-v4.1-flash", "plain")})
	got, ok := index.lookup("deepseek-v4.1-flash-self", "")
	if !ok || got.DisplayName != "plain" {
		t.Fatalf("deployment qualifier lookup failed: %+v %v", got, ok)
	}
	// A genuine entry carrying the qualifier still wins over the stripped one.
	index = buildUpstreamIndex([]upstreamModel{
		model("deepseek-v4.1-flash", "plain"),
		model("deepseek-v4.1-flash-self", "self"),
	})
	got, ok = index.lookup("deepseek-v4.1-flash-self", "")
	if !ok || got.DisplayName != "self" {
		t.Fatalf("qualified entry must win: %+v %v", got, ok)
	}
}

func TestLookupAmbiguousNameRefused(t *testing.T) {
	index := buildUpstreamIndex([]upstreamModel{
		model("a/kimi-k3", "A"),
		model("b/kimi-k3", "B"),
	})
	if got, ok := index.lookup("kimi-k3", ""); ok {
		t.Fatalf("ambiguous bare name must not resolve, got %q", got.ID)
	}
}

func TestLookupShortNameNotFolded(t *testing.T) {
	index := buildUpstreamIndex([]upstreamModel{model("ab", "AB")})
	if _, ok := index.lookup("a-b", ""); ok {
		t.Fatal("short folded keys must not be used")
	}
}

func TestParseUpstreamModelNameField(t *testing.T) {
	got := parseUpstreamModel([]byte(`{"id":"deepseek-v4.1-flash","name":"DeepSeek V4.1 Flash","context_length":256000}`))
	if got.DisplayName != "DeepSeek V4.1 Flash" {
		t.Fatalf("name field not used as display name: %+v", got)
	}
	same := parseUpstreamModel([]byte(`{"id":"auto","name":"auto"}`))
	if same.DisplayName != "" {
		t.Fatalf("name identical to id must be ignored: %+v", same)
	}
	// A label that only capitalises the id is still a label.
	label := parseUpstreamModel([]byte(`{"id":"deepseek-v4.1-flash","name":"DeepSeek-V4.1-Flash"}`))
	if label.DisplayName != "DeepSeek-V4.1-Flash" {
		t.Fatalf("capitalised label must be used: %+v", label)
	}
}

// A config without overwrite_fields must never rewrite values that already
// exist: "missing" is the default and the production rollout mode.
func TestParseConfigMissingModeNeverOverwrites(t *testing.T) {
	cfg, err := parseConfig([]byte("mode: missing\n"))
	if err != nil {
		t.Fatal(err)
	}
	if len(cfg.OverwriteFields) != 0 {
		t.Fatalf("empty overwrite list must stay empty, got %v", cfg.OverwriteFields)
	}
	for _, field := range allFields {
		if cfg.overwrites(field) {
			t.Fatalf("missing mode must not overwrite %q", field)
		}
	}
}

func TestParseConfigExplicitOverwriteFields(t *testing.T) {
	cfg, err := parseConfig([]byte("mode: missing\noverwrite_fields: [display-name]\n"))
	if err != nil {
		t.Fatal(err)
	}
	if !cfg.overwrites("display-name") || cfg.overwrites("thinking") {
		t.Fatalf("unexpected overwrite set: %v", cfg.OverwriteFields)
	}
}

// sync_on_start is documented as on by default (applying models in the CPAMC
// editor enriches them in the same reload), so an omitted key must not disable
// it while an explicit false still must.
func TestParseConfigSyncOnStartDefault(t *testing.T) {
	cases := []struct {
		name string
		raw  string
		want bool
	}{
		{name: "absent", raw: "mode: missing\n", want: true},
		{name: "empty config", raw: "", want: true},
		{name: "explicit true", raw: "sync_on_start: true\n", want: true},
		{name: "explicit false", raw: "sync_on_start: false\n", want: false},
	}
	for _, tc := range cases {
		cfg, err := parseConfig([]byte(tc.raw))
		if err != nil {
			t.Fatalf("%s: %v", tc.name, err)
		}
		if cfg.SyncOnStart != tc.want {
			t.Fatalf("%s: SyncOnStart = %v, want %v", tc.name, cfg.SyncOnStart, tc.want)
		}
	}
}

func TestConfigPathFromArgs(t *testing.T) {
	cases := []struct {
		args []string
		want string
	}{
		{[]string{"-config", "/etc/cpa/config.yaml"}, "/etc/cpa/config.yaml"},
		{[]string{"-config=/etc/cpa/config.yaml"}, "/etc/cpa/config.yaml"},
		{[]string{"--config", "cpa.yaml"}, "cpa.yaml"},
		{[]string{"--config=cpa.yaml"}, "cpa.yaml"},
		{[]string{"-port", "8317"}, ""},
		{[]string{"-config"}, ""},
	}
	for _, tc := range cases {
		if got := configPathFromArgs(tc.args); got != tc.want {
			t.Errorf("configPathFromArgs(%v)=%q want %q", tc.args, got, tc.want)
		}
	}
}

func TestResolveConfigPath(t *testing.T) {
	if got, err := resolveConfigPath(" /explicit/config.yaml "); err != nil || got != "/explicit/config.yaml" {
		t.Fatalf("explicit path: %q %v", got, err)
	}
	dir := t.TempDir()
	t.Chdir(dir)
	if _, err := resolveConfigPath(""); err == nil {
		t.Fatal("missing config.yaml must be an error, not a silent guess")
	}
	if err := os.WriteFile(filepath.Join(dir, "config.yaml"), []byte("config-version: 8\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	got, err := resolveConfigPath("")
	if err != nil || got != filepath.Join(dir, "config.yaml") {
		t.Fatalf("cwd fallback: %q %v", got, err)
	}
}

func TestParseReasoningFlagsAndLadders(t *testing.T) {
	cases := []struct {
		name      string
		payload   string
		reasoning bool
		levels    []string
	}{
		{
			name:      "workbuddy supported_reasoning_levels",
			payload:   `{"id":"glm","supports_reasoning":true,"supported_reasoning_levels":[{"effort":"low"},{"effort":"high"},{"effort":"max"}]}`,
			reasoning: true,
			levels:    []string{"low", "high", "max"},
		},
		{
			name:      "reasoning_supported_efforts",
			payload:   `{"id":"kimi","only_reasoning":true,"reasoning_supported_efforts":["low","medium","high","xhigh","max"]}`,
			reasoning: true,
			levels:    []string{"low", "medium", "high", "xhigh", "max"},
		},
		{
			name:      "single effort on a reasoning-only model",
			payload:   `{"id":"auto","only_reasoning":true,"reasoning_effort":"high"}`,
			reasoning: true,
			levels:    []string{"high"},
		},
		{
			name:      "support without a ladder stays empty",
			payload:   `{"id":"mimo","supports_reasoning":true}`,
			reasoning: true,
			levels:    nil,
		},
		{
			name:      "unknown levels are dropped and case folded",
			payload:   `{"id":"x","support_reasoning":true,"reasoning_levels":["HIGH","turbo","low"]}`,
			reasoning: false,
			levels:    []string{"high", "low"},
		},
	}
	for _, tc := range cases {
		got := parseUpstreamModel([]byte(tc.payload))
		if got.Reasoning != tc.reasoning {
			t.Errorf("%s: reasoning=%v want %v", tc.name, got.Reasoning, tc.reasoning)
		}
		if len(got.Levels) != len(tc.levels) {
			t.Errorf("%s: levels=%v want %v", tc.name, got.Levels, tc.levels)
			continue
		}
		for i := range tc.levels {
			if got.Levels[i] != tc.levels[i] {
				t.Errorf("%s: levels=%v want %v", tc.name, got.Levels, tc.levels)
				break
			}
		}
	}
}
