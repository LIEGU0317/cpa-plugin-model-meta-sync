package main

import (
	"context"
	"fmt"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"testing"
)

func discoveryPlugin(t *testing.T, config string) (*pluginState, string) {
	t.Helper()
	dir := t.TempDir()
	path := filepath.Join(dir, "config.yaml")
	if err := os.WriteFile(path, []byte(config), 0o644); err != nil {
		t.Fatalf("write fixture: %v", err)
	}
	return &pluginState{cfg: Config{
		ConfigPath:     path,
		Mode:           "missing",
		Fields:         normalizeFields(nil),
		TimeoutSeconds: 5,
		BackupKeep:     3,
	}, stop: make(chan struct{})}, path
}

func providerFixture(baseURL string) string {
	return fmt.Sprintf(`config-version: 8
api-keys:
    openai-compatibility:
        - name: Spark
          base-url: %s
          keys:
            - api-key: key-spark
          models:
            - name: spark-x2.6
`, baseURL)
}

// Gateways rooted below /v1 (讯飞星火 serves /v2/models) 404 on /v1/models; the
// plugin must fall back to the path CPA's own "fetch from endpoint" uses.
func TestModelsPathFallsBackToModels(t *testing.T) {
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		switch r.URL.Path {
		case "/v1/models":
			http.NotFound(w, r)
		case "/v2/models":
			_, _ = w.Write([]byte(`{"data":[{"id":"spark-x2.6","name":"Spark X2.6","context_length":128000,"modalities":{"input":["text"]}}]}`))
		default:
			http.NotFound(w, r)
		}
	}))
	defer server.Close()

	p, path := discoveryPlugin(t, providerFixture(server.URL+"/v2"))
	report, err := p.runSync(context.Background(), syncOptions{})
	if err != nil {
		t.Fatalf("sync: %v (%s)", err, report.Error)
	}
	if report.Providers[0].Endpoint != "/models" {
		t.Fatalf("expected /models fallback, got %q", report.Providers[0].Endpoint)
	}
	providers := readProviders(t, path)
	model := firstModel(t, providers[0])
	if model["display-name"] != "Spark X2.6" || model["max-context-length"] != 128000 {
		t.Fatalf("metadata not written after fallback: %v", model)
	}
}

func TestModelsPathExplicitOverride(t *testing.T) {
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path != "/custom/catalog" {
			http.NotFound(w, r)
			return
		}
		_, _ = w.Write([]byte(`{"data":[{"id":"spark-x2.6","display_name":"Spark"}]}`))
	}))
	defer server.Close()

	p, path := discoveryPlugin(t, providerFixture(server.URL))
	p.cfg.ModelsPaths = map[string]string{"Spark": "/custom/catalog"}
	report, err := p.runSync(context.Background(), syncOptions{})
	if err != nil {
		t.Fatalf("sync: %v", err)
	}
	if report.Providers[0].Endpoint != "/custom/catalog" {
		t.Fatalf("override not used: %q", report.Providers[0].Endpoint)
	}
	if got := firstModel(t, readProviders(t, path)[0])["display-name"]; got != "Spark" {
		t.Fatalf("metadata not written: %v", got)
	}
}

// Only a wrong path (404/405) is retried; a real upstream error is reported at
// once instead of hammering the gateway with every candidate.
func TestModelsPathDoesNotRetryServerErrors(t *testing.T) {
	var calls int
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		calls++
		w.WriteHeader(http.StatusInternalServerError)
	}))
	defer server.Close()

	p, _ := discoveryPlugin(t, providerFixture(server.URL))
	report, err := p.runSync(context.Background(), syncOptions{})
	if err == nil || report.OK {
		t.Fatalf("expected failure, got %+v", report)
	}
	if calls != 1 {
		t.Fatalf("500 must not trigger path fallback, got %d calls", calls)
	}
}

func TestModelsPathCandidates(t *testing.T) {
	cfg := Config{}
	got := cfg.modelsPathCandidates("Any")
	if len(got) != 2 || got[0] != "/v1/models" || got[1] != "/models" {
		t.Fatalf("default candidates: %v", got)
	}
	cfg.ModelsPaths = map[string]string{"spark": "/v2/catalog"}
	if got := cfg.modelsPathCandidates("Spark"); len(got) != 1 || got[0] != "/v2/catalog" {
		t.Fatalf("override candidates: %v", got)
	}
}
