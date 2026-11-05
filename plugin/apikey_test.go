package main

import (
	"context"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"testing"
)

// A key override is a credential for one gateway. Sending it to every
// provider leaks it to third-party relays.
func TestParseConfigRejectsUnscopedAPIKey(t *testing.T) {
	for _, raw := range []string{
		"api_key: sk-override\n",
		"api_key: sk-override\nproviders: [A, B]\n",
	} {
		if _, err := parseConfig([]byte(raw)); err == nil {
			t.Fatalf("%q: an api_key that is not scoped to exactly one provider must be rejected", raw)
		}
	}
	cfg, err := parseConfig([]byte("api_key: sk-override\nproviders: [A]\n"))
	if err != nil {
		t.Fatalf("api_key scoped to one provider must be accepted: %v", err)
	}
	if got := cfg.keyOverride("a"); got != "sk-override" {
		t.Fatalf("scoped api_key not applied: %q", got)
	}
}

func TestAPIKeyOverrideReachesOnlyItsProvider(t *testing.T) {
	var mu sync.Mutex
	seen := map[string]string{}
	serve := func(name string) *httptest.Server {
		return httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
			mu.Lock()
			seen[name] = r.Header.Get("Authorization")
			mu.Unlock()
			_, _ = w.Write([]byte(`{"data":[{"id":"m-one","context_length":10}]}`))
		}))
	}
	serverA, serverB := serve("A"), serve("B")
	defer serverA.Close()
	defer serverB.Close()

	fixture := "api-keys:\n  openai-compatibility:\n" +
		"    - name: A\n      base-url: " + serverA.URL + "\n      api-key-entries:\n        - api-key: key-a\n      models:\n        - name: m-one\n" +
		"    - name: B\n      base-url: " + serverB.URL + "\n      api-key-entries:\n        - api-key: key-b\n      models:\n        - name: m-one\n"
	path := filepath.Join(t.TempDir(), "config.yaml")
	if err := os.WriteFile(path, []byte(fixture), 0o644); err != nil {
		t.Fatal(err)
	}
	cfg, err := parseConfig([]byte("config_path: " + path + "\napi_keys:\n  A: override-a\n"))
	if err != nil {
		t.Fatal(err)
	}
	p := &pluginState{cfg: cfg, stop: make(chan struct{})}
	if _, err := p.runSync(context.Background(), syncOptions{DryRun: true}); err != nil {
		t.Fatal(err)
	}
	if seen["A"] != "Bearer override-a" {
		t.Fatalf("provider A: Authorization=%q, want the override", seen["A"])
	}
	if seen["B"] != "Bearer key-b" {
		t.Fatalf("provider B: Authorization=%q, want its own key", seen["B"])
	}
}

func TestManagementPayloadsRedactKeyOverrides(t *testing.T) {
	raw := []byte("api_keys:\n  A: secret-a\napi_key: secret-legacy\nproviders: [A]\n")
	cfg, err := parseConfig(raw)
	if err != nil {
		t.Fatal(err)
	}
	p := &pluginState{cfg: cfg, configYAML: raw}
	for name, payload := range map[string]any{"status": p.statusPayload(), "config": p.configPayload()} {
		resp, _ := jsonResponse(200, payload)
		if body := string(resp.Body); strings.Contains(body, "secret-") {
			t.Fatalf("%s payload leaks a key: %s", name, body)
		}
	}
}
