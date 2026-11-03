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
	"testing"

	"gopkg.in/yaml.v3"
)

func mockGateway(t *testing.T, key, body string, status int) *httptest.Server {
	t.Helper()
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path != "/v1/models" {
			http.NotFound(w, r)
			return
		}
		if key != "" && r.Header.Get("Authorization") != "Bearer "+key {
			w.WriteHeader(http.StatusUnauthorized)
			return
		}
		if status != 0 {
			w.WriteHeader(status)
			_, _ = w.Write([]byte(`{"error":"boom"}`))
			return
		}
		_, _ = w.Write([]byte(body))
	}))
	t.Cleanup(server.Close)
	return server
}

type scopeFixture struct {
	path      string
	one       *httptest.Server
	two       *httptest.Server
	disabled  *httptest.Server
	rawConfig string
}

func newScopeFixture(t *testing.T, twoStatus int) *scopeFixture {
	t.Helper()
	one := mockGateway(t, "key-one", `{"data":[{"id":"auto/model-a","display_name":"Model A","context_length":111}]}`, 0)
	two := mockGateway(t, "key-two", `{"data":[{"id":"plain-model","name":"Plain Model","context_length":222}]}`, twoStatus)
	disabled := mockGateway(t, "key-three", `{"data":[{"id":"off-model","display_name":"Off","context_length":333}]}`, 0)

	fixture := fmt.Sprintf(`config-version: 8
api-keys:
    openai-compatibility:
        - name: Gateway One
          base-url: %s
          keys:
            - api-key: key-one
          models:
            - name: auto/model-a
        - name: Gateway Two
          base-url: %s
          keys:
            - api-key: key-two
          models:
            - name: plain-model
        - name: Disabled Gateway
          base-url: %s
          disabled: true
          keys:
            - api-key: key-three
          models:
            - name: off-model
`, one.URL, two.URL, disabled.URL)

	dir := t.TempDir()
	path := filepath.Join(dir, "config.yaml")
	if err := os.WriteFile(path, []byte(fixture), 0o644); err != nil {
		t.Fatalf("write fixture: %v", err)
	}
	return &scopeFixture{path: path, one: one, two: two, disabled: disabled, rawConfig: fixture}
}

func (f *scopeFixture) plugin() *pluginState {
	return &pluginState{cfg: Config{
		ConfigPath:     f.path,
		Mode:           "missing",
		Fields:         normalizeFields(nil),
		TimeoutSeconds: 5,
		BackupKeep:     3,
	}, stop: make(chan struct{})}
}

func readProviders(t *testing.T, path string) []map[string]any {
	t.Helper()
	raw, err := os.ReadFile(path)
	if err != nil {
		t.Fatal(err)
	}
	var cfg map[string]any
	if err := yaml.Unmarshal(raw, &cfg); err != nil {
		t.Fatalf("written yaml invalid: %v", err)
	}
	list := cfg["api-keys"].(map[string]any)["openai-compatibility"].([]any)
	out := make([]map[string]any, 0, len(list))
	for _, item := range list {
		out = append(out, item.(map[string]any))
	}
	return out
}

func firstModel(t *testing.T, provider map[string]any) map[string]any {
	t.Helper()
	models, ok := provider["models"].([]any)
	if !ok || len(models) == 0 {
		t.Fatalf("provider has no models: %v", provider["name"])
	}
	return models[0].(map[string]any)
}

// The plugin must sync every provider it finds, without being told which
// vendors exist and without hard-coded names.
func TestAllProvidersSyncedWithoutFilter(t *testing.T) {
	f := newScopeFixture(t, 0)
	report, err := f.plugin().runSync(context.Background(), syncOptions{})
	if err != nil {
		t.Fatalf("sync: %v (%s)", err, report.Error)
	}
	if !report.Written {
		t.Fatal("expected a write")
	}
	if len(report.Providers) != 3 {
		t.Fatalf("want 3 provider reports (including disabled), got %d: %+v", len(report.Providers), report.Providers)
	}
	providers := readProviders(t, f.path)
	if got := firstModel(t, providers[0])["display-name"]; got != "Model A" {
		t.Fatalf("gateway one not synced: %v", got)
	}
	if got := firstModel(t, providers[1])["display-name"]; got != "Plain Model" {
		t.Fatalf("gateway two name field not used: %v", got)
	}
	// Disabled provider stays untouched.
	if got := firstModel(t, providers[2])["display-name"]; got != nil {
		t.Fatalf("disabled provider must not be synced: %v", got)
	}
	for _, provider := range report.Providers {
		if provider.Skipped != "" && !strings.Contains(provider.Skipped, "disabled") {
			t.Fatalf("unexpected skip: %+v", provider)
		}
	}
}

func TestProviderNameFilterStillWorks(t *testing.T) {
	f := newScopeFixture(t, 0)
	p := f.plugin()
	p.cfg.Providers = []string{"gateway two"}
	report, err := p.runSync(context.Background(), syncOptions{})
	if err != nil {
		t.Fatalf("sync: %v", err)
	}
	if len(report.Providers) != 1 || report.Providers[0].Name != "Gateway Two" {
		t.Fatalf("filter not applied by name: %+v", report.Providers)
	}
	providers := readProviders(t, f.path)
	if got := firstModel(t, providers[0])["display-name"]; got != nil {
		t.Fatalf("filtered-out provider was written: %v", got)
	}
	if got := firstModel(t, providers[1])["display-name"]; got != "Plain Model" {
		t.Fatalf("selected provider not synced: %v", got)
	}
}

func TestIncludeDisabledOptIn(t *testing.T) {
	f := newScopeFixture(t, 0)
	p := f.plugin()
	p.cfg.IncludeDisabled = true
	if _, err := p.runSync(context.Background(), syncOptions{}); err != nil {
		t.Fatalf("sync: %v", err)
	}
	providers := readProviders(t, f.path)
	if got := firstModel(t, providers[2])["display-name"]; got != "Off" {
		t.Fatalf("include_disabled must sync disabled providers: %v", got)
	}
}

// A gateway that is down must not block the others, and its entries must stay
// byte-identical.
func TestPartialUpstreamFailureIsIsolated(t *testing.T) {
	f := newScopeFixture(t, http.StatusServiceUnavailable)
	report, err := f.plugin().runSync(context.Background(), syncOptions{})
	if err != nil {
		t.Fatalf("partial failure must not abort: %v (%s)", err, report.Error)
	}
	if !report.OK || !report.Written {
		t.Fatalf("expected successful write: %+v", report)
	}
	joined := strings.Join(report.Warnings, " ")
	if !strings.Contains(joined, "Gateway Two") {
		t.Fatalf("failure not reported by name: %v", report.Warnings)
	}
	providers := readProviders(t, f.path)
	if got := firstModel(t, providers[0])["display-name"]; got != "Model A" {
		t.Fatalf("healthy provider not synced: %v", got)
	}
	if got := firstModel(t, providers[1])["display-name"]; got != nil {
		t.Fatalf("failed provider must stay untouched: %v", got)
	}
	if report.Providers[1].UpstreamErr == "" {
		t.Fatal("failed provider must carry an error")
	}
}

func TestAllUpstreamsFailingWritesNothing(t *testing.T) {
	one := mockGateway(t, "key-one", "", http.StatusInternalServerError)
	two := mockGateway(t, "key-two", "", http.StatusBadGateway)
	fixture := fmt.Sprintf(`config-version: 8
api-keys:
    openai-compatibility:
        - name: A
          base-url: %s
          models:
            - name: m1
        - name: B
          base-url: %s
          models:
            - name: m2
`, one.URL, two.URL)
	dir := t.TempDir()
	path := filepath.Join(dir, "config.yaml")
	if err := os.WriteFile(path, []byte(fixture), 0o644); err != nil {
		t.Fatal(err)
	}
	before, _ := os.ReadFile(path)
	p := &pluginState{cfg: Config{ConfigPath: path, Mode: "missing", Fields: normalizeFields(nil), TimeoutSeconds: 5, BackupKeep: 3}, stop: make(chan struct{})}
	report, err := p.runSync(context.Background(), syncOptions{})
	if err == nil || report.OK {
		t.Fatalf("expected whole-run failure, got %+v err=%v", report, err)
	}
	after, _ := os.ReadFile(path)
	if string(before) != string(after) {
		t.Fatal("config must not change when every upstream fails")
	}
}

// Reports are keyed by provider name only: no address, host or port.
func TestReportCarriesNoAddress(t *testing.T) {
	f := newScopeFixture(t, 0)
	report, err := f.plugin().runSync(context.Background(), syncOptions{DryRun: true})
	if err != nil {
		t.Fatalf("sync: %v", err)
	}
	blob, errMarshal := json.Marshal(report)
	if errMarshal != nil {
		t.Fatal(errMarshal)
	}
	text := string(blob)
	for _, secret := range []string{"127.0.0.1", "base_url", "http://"} {
		if strings.Contains(text, secret) {
			t.Fatalf("report leaks %q: %s", secret, text)
		}
	}
	if !strings.Contains(text, "Gateway One") {
		t.Fatalf("report should still name providers: %s", text)
	}
}
