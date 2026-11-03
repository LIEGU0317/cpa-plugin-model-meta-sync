package main

import (
	"context"
	"encoding/json"
	"fmt"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"sync/atomic"
	"testing"

	"gopkg.in/yaml.v3"
)

const testUpstreamBody = `{"object":"list","has_more":false,"data":[
 {"id":"auto/model-a","display_name":"Model A","context_length":1048576,"modalities":{"input":["text","image"],"output":["text"]},"reasoning":true,"reasoning_levels":["low","high","max"]},
 {"id":"auto/model-b","display_name":"Model B","context_length":1000000,"reasoning":true,"supported_reasoning_levels":[{"effort":"low"},{"effort":"high"}]},
 {"id":"auto/model-c","display_name":"Model C","context_length":200000,"reasoning":false}
]}`

func testConfigYAML(mode string) string {
	return fmt.Sprintf(`config-version: 8
server:
    host: 127.0.0.1
    port: 18317
api-keys:
    openai-compatibility:
        - name: Magpie
          base-url: http://127.0.0.1:19001
          keys:
            - api-key: sk-test
          models:
            - name: auto/model-a
              alias: model-a
            - name: auto/model-b
              alias: model-b
              display-name: keep-me
              max-context-length: 123
              thinking:
                levels:
                    - low
            - name: auto/model-c
              alias: model-c
              thinking:
                levels:
                    - low
                    - medium
`)
}

func newTestPlugin(t *testing.T, mode string, upstreamURL string) (*pluginState, string) {
	t.Helper()
	dir := t.TempDir()
	path := filepath.Join(dir, "config.yaml")
	if err := os.WriteFile(path, []byte(testConfigYAML(mode)), 0o644); err != nil {
		t.Fatalf("write fixture: %v", err)
	}
	cfg := Config{ConfigPath: path, Providers: []string{"Magpie"}, Mode: mode, TimeoutSeconds: 5, BackupKeep: 3}
	cfg.Fields = normalizeFields(nil)
	p := &pluginState{cfg: cfg, stop: make(chan struct{})}
	_ = upstreamURL
	return p, path
}

func TestParseUpstreamModel(t *testing.T) {
	raw := json.RawMessage(`{"id":"auto/x","display_name":"X","context_length":100,"modalities":{"input":["text","image"],"output":["text"]},"reasoning":true,"supported_reasoning_levels":[{"effort":"minimal"},{"effort":"high"}]}`)
	model := parseUpstreamModel(raw)
	if model.ID != "auto/x" || model.DisplayName != "X" || model.ContextLength != 100 {
		t.Fatalf("unexpected model: %+v", model)
	}
	if !model.Reasoning || len(model.Levels) != 2 || model.Levels[1] != "high" {
		t.Fatalf("levels: %+v", model.Levels)
	}
	if len(model.Input) != 2 || model.Output[0] != "text" {
		t.Fatalf("modalities: %+v %+v", model.Input, model.Output)
	}
}

func TestApplyMissingKeepsExisting(t *testing.T) {
	var model yaml.Node
	if err := yaml.Unmarshal([]byte("name: auto/model-b\nalias: model-b\ndisplay-name: keep-me\nmax-context-length: 123\nthinking:\n  levels:\n    - low\n"), &model); err != nil {
		t.Fatal(err)
	}
	up := upstreamModel{ID: "auto/model-b", DisplayName: "new-name", ContextLength: 999, Reasoning: true, Levels: []string{"low", "high"}}
	changes := applyUpstreamFields(documentRoot(&model), up, Config{Mode: "missing", Fields: allFields})
	if len(changes) != 0 {
		t.Fatalf("missing mode must not overwrite: %+v", changes)
	}
}

func TestApplyAlwaysOverwrites(t *testing.T) {
	var model yaml.Node
	if err := yaml.Unmarshal([]byte("name: auto/model-b\nalias: model-b\ndisplay-name: keep-me\nmax-context-length: 123\nthinking:\n  levels:\n    - low\n"), &model); err != nil {
		t.Fatal(err)
	}
	up := upstreamModel{ID: "auto/model-b", DisplayName: "new-name", ContextLength: 999, Reasoning: true, Levels: []string{"low", "high"}}
	changes := applyUpstreamFields(documentRoot(&model), up, Config{Mode: "always", Fields: allFields})
	if len(changes) != 3 {
		t.Fatalf("want 3 changes, got %d: %+v", len(changes), changes)
	}
	var out map[string]any
	encoded, err := yaml.Marshal(documentRoot(&model))
	if err != nil {
		t.Fatal(err)
	}
	if err := yaml.Unmarshal(encoded, &out); err != nil {
		t.Fatal(err)
	}
	if out["display-name"] != "new-name" || out["max-context-length"] != 999 {
		t.Fatalf("unexpected model after apply: %v", out)
	}
}

func TestValidateRejectsUnexpectedChanges(t *testing.T) {
	oldRaw := []byte(testConfigYAML("missing"))
	newRaw := []byte(strings.Replace(testConfigYAML("missing"), "http://127.0.0.1:19001", "http://evil.example", 1))
	if err := validateSemanticDiff(oldRaw, newRaw, Config{Fields: allFields}, map[int]bool{0: true}); err == nil {
		t.Fatal("expected validation error for base-url change")
	}
}

func TestPathAllowedThinkingLevels(t *testing.T) {
	allowed := map[string]bool{"thinking": true, "display-name": true}
	providers := map[int]bool{0: true}
	cases := map[string]bool{
		"api-keys.openai-compatibility.0.models.3.thinking":          true,
		"api-keys.openai-compatibility.0.models.3.thinking.levels.1": true,
		"api-keys.openai-compatibility.0.models.3.display-name":      true,
		"api-keys.openai-compatibility.0.models.3.name":              false,
		"api-keys.openai-compatibility.0.base-url":                   false,
		"api-keys.openai-compatibility.1.models.0.thinking":          false,
		"openai-compatibility.0.models.2.thinking.levels":            true,
	}
	for path, want := range cases {
		if got := pathAllowed(path, allowed, providers); got != want {
			t.Errorf("pathAllowed(%q)=%v want %v", path, got, want)
		}
	}
}

func TestRunSyncFillsAndIsIdempotent(t *testing.T) {
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path != "/v1/models" {
			http.NotFound(w, r)
			return
		}
		if r.Header.Get("Authorization") != "Bearer sk-test" {
			w.WriteHeader(http.StatusUnauthorized)
			return
		}
		_, _ = w.Write([]byte(testUpstreamBody))
	}))
	defer server.Close()

	dir := t.TempDir()
	path := filepath.Join(dir, "config.yaml")
	fixture := strings.Replace(testConfigYAML("missing"), "http://127.0.0.1:19001", server.URL, 1)
	if err := os.WriteFile(path, []byte(fixture), 0o644); err != nil {
		t.Fatal(err)
	}
	p := &pluginState{cfg: Config{ConfigPath: path, Providers: []string{"Magpie"}, Mode: "missing", Fields: allFields, TimeoutSeconds: 5, BackupKeep: 3}, stop: make(chan struct{})}

	report, err := p.runSync(context.Background(), syncOptions{})
	if err != nil {
		t.Fatalf("sync: %v (%s)", err, report.Error)
	}
	if !report.Written || report.ChangedFields == 0 {
		t.Fatalf("expected write, got %+v", report)
	}
	data, err := os.ReadFile(path)
	if err != nil {
		t.Fatal(err)
	}
	var cfg map[string]any
	if err := yaml.Unmarshal(data, &cfg); err != nil {
		t.Fatalf("written yaml invalid: %v", err)
	}
	models := cfg["api-keys"].(map[string]any)["openai-compatibility"].([]any)[0].(map[string]any)["models"].([]any)
	first := models[0].(map[string]any)
	if first["display-name"] != "Model A" || first["max-context-length"] != 1048576 {
		t.Fatalf("model-a not enriched: %v", first)
	}
	second := models[1].(map[string]any)
	if second["display-name"] != "keep-me" || second["max-context-length"] != 123 {
		t.Fatalf("model-b overwritten in missing mode: %v", second)
	}
	levels := second["thinking"].(map[string]any)["levels"].([]any)
	if len(levels) != 1 || levels[0] != "low" {
		t.Fatalf("model-b thinking changed in missing mode: %v", levels)
	}
	if out, ok := first["output-modalities"].([]any); !ok || len(out) != 1 || out[0] != "text" {
		t.Fatalf("model-a output modalities not copied: %v", first["output-modalities"])
	}

	report2, err := p.runSync(context.Background(), syncOptions{})
	if err != nil {
		t.Fatalf("second sync: %v", err)
	}
	if report2.Written || report2.ChangedFields != 0 {
		t.Fatalf("expected idempotent second run, got %+v", report2)
	}
}

func TestRunSyncUpstreamFailureLeavesFile(t *testing.T) {
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(http.StatusServiceUnavailable)
	}))
	defer server.Close()

	dir := t.TempDir()
	path := filepath.Join(dir, "config.yaml")
	fixture := strings.Replace(testConfigYAML("missing"), "http://127.0.0.1:19001", server.URL, 1)
	if err := os.WriteFile(path, []byte(fixture), 0o644); err != nil {
		t.Fatal(err)
	}
	p := &pluginState{cfg: Config{ConfigPath: path, Providers: []string{"Magpie"}, Mode: "missing", Fields: allFields, TimeoutSeconds: 5, BackupKeep: 3}, stop: make(chan struct{})}
	before, _ := os.ReadFile(path)
	report, err := p.runSync(context.Background(), syncOptions{})
	if err == nil || report == nil || report.OK {
		t.Fatalf("expected upstream failure, got %+v err=%v", report, err)
	}
	after, _ := os.ReadFile(path)
	if string(before) != string(after) {
		t.Fatal("config must not change when upstream fetch fails")
	}
}

func TestFetchPagination(t *testing.T) {
	var calls int32
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		n := atomic.AddInt32(&calls, 1)
		if n == 1 {
			if r.URL.Query().Get("after") != "" {
				t.Errorf("first call must not carry after, got %q", r.URL.RawQuery)
			}
			_, _ = w.Write([]byte(`{"object":"list","has_more":true,"last_id":"auto/a","data":[{"id":"auto/a","display_name":"A"}]}`))
			return
		}
		if r.URL.Query().Get("after") != "auto/a" {
			t.Errorf("second call after=%q", r.URL.Query().Get("after"))
		}
		_, _ = w.Write([]byte(`{"object":"list","has_more":false,"data":[{"id":"auto/b","display_name":"B"}]}`))
	}))
	defer server.Close()
	models, err := fetchUpstream(context.Background(), Config{TimeoutSeconds: 5, UpstreamModelsPath: "/v1/models"}, server.URL, "sk-test", "/v1/models")
	if err != nil {
		t.Fatal(err)
	}
	if len(models) != 2 || models[1].ID != "auto/b" {
		t.Fatalf("unexpected models: %+v", models)
	}
}

func TestWriteConflictRejectsStaleDocument(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, "config.yaml")
	if err := os.WriteFile(path, []byte(testConfigYAML("missing")), 0o644); err != nil {
		t.Fatal(err)
	}
	doc, err := loadConfigDocument(path)
	if err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(path, []byte(testConfigYAML("missing")+"# concurrent\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	if _, err := writeConfigAtomic(docConfig(doc), doc, []byte("attacker")); err == nil {
		t.Fatal("expected conflict error")
	}
}

func docConfig(doc *configDocument) Config {
	return Config{ConfigPath: doc.path, BackupKeep: 3}
}

func TestModelsURLJoinsBase(t *testing.T) {
	cases := map[[2]string]string{
		{"http://127.0.0.1:3425", "/v1/models"}: "http://127.0.0.1:3425/v1/models",
		{"http://host/v1", "/v1/models"}:        "http://host/v1/models",
		{"http://host/", "/v1/models"}:          "http://host/v1/models",
		{"http://host/base/", "/v1/models"}:     "http://host/base/v1/models",
	}
	for in, want := range cases {
		got, err := modelsURL(in[0], in[1])
		if err != nil {
			t.Fatalf("%v: %v", in, err)
		}
		if got != want {
			t.Errorf("modelsURL(%q)=%q want %q", in[0], got, want)
		}
	}
}

func TestProviderKeyReadsEntries(t *testing.T) {
	var node yaml.Node
	if err := yaml.Unmarshal([]byte("name: p\nkeys:\n  - api-key: sk-one\n  - api-key: sk-two\n"), &node); err != nil {
		t.Fatal(err)
	}
	if got := providerKey(documentRoot(&node)); got != "sk-one" {
		t.Fatalf("providerKey=%q", got)
	}
}

func TestOverwriteFieldsOnly(t *testing.T) {
	var model yaml.Node
	if err := yaml.Unmarshal([]byte("name: auto/model-b\nalias: model-b\ndisplay-name: keep-me\nthinking:\n  levels:\n    - low\n"), &model); err != nil {
		t.Fatal(err)
	}
	up := upstreamModel{ID: "auto/model-b", DisplayName: "upstream-name", Reasoning: true, Levels: []string{"low", "high"}}
	cfg := Config{Mode: "missing", Fields: allFields, OverwriteFields: []string{"display-name"}}
	changes := applyUpstreamFields(documentRoot(&model), up, cfg)
	if len(changes) != 1 || changes[0].Field != "display-name" {
		t.Fatalf("want only display-name change, got %+v", changes)
	}
	var out map[string]any
	encoded, _ := yaml.Marshal(documentRoot(&model))
	if err := yaml.Unmarshal(encoded, &out); err != nil {
		t.Fatal(err)
	}
	if out["display-name"] != "upstream-name" {
		t.Fatalf("display-name not overwritten: %v", out)
	}
	levels := out["thinking"].(map[string]any)["levels"].([]any)
	if len(levels) != 1 || levels[0] != "low" {
		t.Fatalf("thinking must stay untouched: %v", levels)
	}
}

func TestAliasModelsListSkipped(t *testing.T) {
	fixture := `config-version: 8
api-keys:
    openai-compatibility:
        - name: Magpie
          base-url: http://127.0.0.1:19001
          keys:
            - api-key: sk-test
          models: &shared
            - name: auto/model-a
              alias: model-a
        - name: Other
          base-url: http://127.0.0.1:19002
          keys:
            - api-key: sk-other
          models: *shared
`
	dir := t.TempDir()
	path := filepath.Join(dir, "config.yaml")
	if err := os.WriteFile(path, []byte(fixture), 0o644); err != nil {
		t.Fatal(err)
	}
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		_, _ = w.Write([]byte(`{"data":[{"id":"auto/model-a","display_name":"A","context_length":10}]}`))
	}))
	defer server.Close()
	// point both providers at the mock by editing nodes is overkill: only Magpie is selected.
	p := &pluginState{cfg: Config{ConfigPath: path, Providers: []string{"Magpie"}, Mode: "missing", Fields: allFields, TimeoutSeconds: 5, BackupKeep: 3, UpstreamModelsPath: "/v1/models"}, stop: make(chan struct{})}
	_ = server
	// Force the provider base URL to the mock through a copied config.
	text := strings.Replace(fixture, "http://127.0.0.1:19001", server.URL, 1)
	if err := os.WriteFile(path, []byte(text), 0o644); err != nil {
		t.Fatal(err)
	}
	before, _ := os.ReadFile(path)
	report, err := p.runSync(context.Background(), syncOptions{})
	if err != nil {
		t.Fatalf("sync: %v", err)
	}
	if report.Written {
		t.Fatal("alias-backed models must not be rewritten")
	}
	after, _ := os.ReadFile(path)
	if string(before) != string(after) {
		t.Fatal("config changed for alias models list")
	}
	if len(report.Providers) == 0 || !strings.Contains(report.Providers[0].Skipped, "aliases") {
		t.Fatalf("expected skip reason, got %+v", report.Providers)
	}
}

func TestNonTargetProviderUntouched(t *testing.T) {
	fixture := `config-version: 8
api-keys:
    openai-compatibility:
        - name: Other
          base-url: http://127.0.0.1:19999
          keys:
            - api-key: sk-other
          models:
            - name: other-model
              alias: other
        - name: Magpie
          base-url: MOCK
          keys:
            - api-key: sk-test
          models:
            - name: auto/model-a
              alias: model-a
`
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		_, _ = w.Write([]byte(`{"data":[{"id":"auto/model-a","display_name":"A","context_length":10}]}`))
	}))
	defer server.Close()
	dir := t.TempDir()
	path := filepath.Join(dir, "config.yaml")
	if err := os.WriteFile(path, []byte(strings.Replace(fixture, "MOCK", server.URL, 1)), 0o644); err != nil {
		t.Fatal(err)
	}
	p := &pluginState{cfg: Config{ConfigPath: path, Providers: []string{"Magpie"}, Mode: "missing", Fields: allFields, TimeoutSeconds: 5, BackupKeep: 3}, stop: make(chan struct{})}
	if _, err := p.runSync(context.Background(), syncOptions{}); err != nil {
		t.Fatal(err)
	}
	var cfg map[string]any
	raw, _ := os.ReadFile(path)
	if err := yaml.Unmarshal(raw, &cfg); err != nil {
		t.Fatal(err)
	}
	providers := cfg["api-keys"].(map[string]any)["openai-compatibility"].([]any)
	other := providers[0].(map[string]any)["models"].([]any)[0].(map[string]any)
	if other["display-name"] != nil || other["max-context-length"] != nil {
		t.Fatalf("non-target provider modified: %v", other)
	}
	magpie := providers[1].(map[string]any)["models"].([]any)[0].(map[string]any)
	if magpie["display-name"] != "A" {
		t.Fatalf("target provider not enriched: %v", magpie)
	}
}

func TestMalformedContextLengthReplacedSafely(t *testing.T) {
	var model yaml.Node
	if err := yaml.Unmarshal([]byte("name: m\nalias: m\nmax-context-length: \"abc\"\n"), &model); err != nil {
		t.Fatal(err)
	}
	up := upstreamModel{ID: "m", ContextLength: 1234}
	cfg := Config{Mode: "missing", Fields: allFields, OverwriteFields: []string{"max-context-length"}}
	changes := applyUpstreamFields(documentRoot(&model), up, cfg)
	if len(changes) != 1 {
		t.Fatalf("want 1 change, got %+v", changes)
	}
	encoded, _ := yaml.Marshal(documentRoot(&model))
	var out map[string]any
	if err := yaml.Unmarshal(encoded, &out); err != nil {
		t.Fatal(err)
	}
	if out["max-context-length"] != 1234 {
		t.Fatalf("malformed value not replaced: %v", out["max-context-length"])
	}
}

func TestDuplicateUpstreamIDsFirstWins(t *testing.T) {
	models := []upstreamModel{{ID: "x", DisplayName: "first"}, {ID: "x", DisplayName: "second"}}
	index := buildUpstreamIndex(models)
	if got, _ := index.lookup("x", ""); got.DisplayName != "first" {
		t.Fatalf("first should win, got %q", got.DisplayName)
	}
}

func TestPruneKeepsDeployBackup(t *testing.T) {
	dir := t.TempDir()
	config := filepath.Join(dir, "config.yaml")
	if err := os.WriteFile(config, []byte("x"), 0o644); err != nil {
		t.Fatal(err)
	}
	deploy := filepath.Join(dir, "config.yaml.bak-modelmetasync-deploy-20260101-000000")
	if err := os.WriteFile(deploy, []byte("d"), 0o644); err != nil {
		t.Fatal(err)
	}
	for i := 0; i < 8; i++ {
		name := filepath.Join(dir, fmt.Sprintf("config.yaml.bak-modelmetasync-202601%02d-000000", i+1))
		if err := os.WriteFile(name, []byte("s"), 0o644); err != nil {
			t.Fatal(err)
		}
	}
	pruneBackups(config, 3)
	if _, err := os.Stat(deploy); err != nil {
		t.Fatal("deploy backup must survive pruning")
	}
	entries, _ := os.ReadDir(dir)
	syncCount := 0
	for _, entry := range entries {
		if strings.HasPrefix(entry.Name(), "config.yaml.bak-modelmetasync-2026") {
			syncCount++
		}
	}
	if syncCount != 3 {
		t.Fatalf("want 3 sync backups, got %d", syncCount)
	}
}

func TestMultiDocumentConfigRejected(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, "config.yaml")
	raw := testConfigYAML("missing") + "---\nsecret: keep-me\n"
	if err := os.WriteFile(path, []byte(raw), 0o644); err != nil {
		t.Fatal(err)
	}
	if _, err := loadConfigDocument(path); err == nil {
		t.Fatal("multi-document config must be rejected")
	}
}

func TestDisabledTruthyValues(t *testing.T) {
	for _, value := range []string{"true", "yes", "Yes", "on", "ON", "True"} {
		var node yaml.Node
		if err := yaml.Unmarshal([]byte("disabled: "+value+"\n"), &node); err != nil {
			t.Fatal(err)
		}
		if !isTruthy(mapLookup(documentRoot(&node), "disabled")) {
			t.Errorf("disabled: %s must be truthy", value)
		}
	}
	for _, value := range []string{"false", "no", "off", ""} {
		var node yaml.Node
		source := "disabled: " + value + "\n"
		if value == "" {
			source = "disabled:\n"
		}
		if err := yaml.Unmarshal([]byte(source), &node); err != nil {
			t.Fatal(err)
		}
		if isTruthy(mapLookup(documentRoot(&node), "disabled")) {
			t.Errorf("disabled: %s must be falsy", value)
		}
	}
}

func TestItemLevelAliasSkipsProviderWithoutWrite(t *testing.T) {
	fixture := `config-version: 8
api-keys:
    openai-compatibility:
        - name: Magpie
          base-url: http://127.0.0.1:19999
          keys:
            - api-key: sk-test
          models:
            - &m
              name: auto/model-a
              alias: model-a
            - *m
`
	dir := t.TempDir()
	path := filepath.Join(dir, "config.yaml")
	if err := os.WriteFile(path, []byte(fixture), 0o644); err != nil {
		t.Fatal(err)
	}
	p := &pluginState{cfg: Config{ConfigPath: path, Providers: []string{"Magpie"}, Mode: "always", Fields: allFields, TimeoutSeconds: 5, BackupKeep: 3}, stop: make(chan struct{})}
	before, _ := os.ReadFile(path)
	report, err := p.runSync(context.Background(), syncOptions{})
	if err != nil {
		t.Fatalf("sync: %v", err)
	}
	if report.Written {
		t.Fatal("provider with aliased model entries must not be written")
	}
	after, _ := os.ReadFile(path)
	if string(before) != string(after) {
		t.Fatal("file changed despite alias skip")
	}
}

func TestSharedItemAnchorAcrossSelectedProvidersSkipped(t *testing.T) {
	fixture := `config-version: 8
api-keys:
    openai-compatibility:
        - name: Magpie
          base-url: http://127.0.0.1:19999
          keys:
            - api-key: sk-a
          models:
            - &shared
              name: auto/model-a
              alias: model-a
        - name: Magpie2
          base-url: http://127.0.0.1:19998
          keys:
            - api-key: sk-b
          models:
            - *shared
`
	dir := t.TempDir()
	path := filepath.Join(dir, "config.yaml")
	if err := os.WriteFile(path, []byte(fixture), 0o644); err != nil {
		t.Fatal(err)
	}
	p := &pluginState{cfg: Config{ConfigPath: path, Providers: []string{"Magpie", "Magpie2"}, Mode: "always", Fields: allFields, TimeoutSeconds: 5, BackupKeep: 3}, stop: make(chan struct{})}
	before, _ := os.ReadFile(path)
	report, err := p.runSync(context.Background(), syncOptions{})
	if err != nil {
		t.Fatalf("sync: %v", err)
	}
	if report.Written {
		t.Fatal("shared item anchors must not be written")
	}
	after, _ := os.ReadFile(path)
	if string(before) != string(after) {
		t.Fatal("file changed despite shared anchor")
	}
	for _, prov := range report.Providers {
		if !strings.Contains(prov.Skipped, "anchors") {
			t.Errorf("provider %s not skipped: %q", prov.Name, prov.Skipped)
		}
	}
}
