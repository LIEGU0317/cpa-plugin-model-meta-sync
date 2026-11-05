package main

import (
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"
)

// A Go c-shared library is never unloaded, so anything the plugin started
// keeps running after the host retires it. Shutdown must stop the timer loop,
// or a retired version keeps rewriting config.yaml next to its successor.
func TestShutdownStopsBackgroundLoop(t *testing.T) {
	if _, err := buildPlugin([]byte("sync_on_start: false\ninterval_minutes: 5\nconfig_path: /nonexistent\n"), ""); err != nil {
		t.Fatal(err)
	}
	activeBackground.Lock()
	stop := activeBackground.stop
	activeBackground.Unlock()
	if stop == nil {
		t.Fatal("background loop was not started")
	}

	ModelMetaSyncPluginShutdown()

	select {
	case <-stop:
	default:
		t.Fatal("shutdown left the background loop running")
	}
}

// A sync already talking to a slow upstream must be cancelled by shutdown
// instead of finishing (and writing) after the plugin was retired.
func TestShutdownCancelsInFlightBackgroundSync(t *testing.T) {
	released := make(chan struct{})
	cancelled := make(chan struct{})
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		close(released)
		select {
		case <-r.Context().Done():
			close(cancelled)
		case <-time.After(10 * time.Second):
		}
	}))
	defer server.Close()

	dir := t.TempDir()
	path := filepath.Join(dir, "config.yaml")
	fixture := "api-keys:\n  openai-compatibility:\n    - name: Slow\n      base-url: " + server.URL + "\n      models:\n        - name: m-one\n"
	if err := os.WriteFile(path, []byte(fixture), 0o644); err != nil {
		t.Fatal(err)
	}

	defer func(previous time.Duration) { startupSyncDelay = previous }(startupSyncDelay)
	startupSyncDelay = 10 * time.Millisecond
	if _, err := buildPlugin([]byte("sync_on_start: true\ntimeout_seconds: 30\nconfig_path: "+path+"\n"), ""); err != nil {
		t.Fatal(err)
	}
	select {
	case <-released:
	case <-time.After(5 * time.Second):
		t.Fatal("startup sync never reached the upstream")
	}

	ModelMetaSyncPluginShutdown()

	select {
	case <-cancelled:
	case <-time.After(3 * time.Second):
		t.Fatal("shutdown did not cancel the in-flight upstream request")
	}
	raw, _ := os.ReadFile(path)
	if !strings.Contains(string(raw), "name: m-one") {
		t.Fatalf("config changed: %s", raw)
	}
}
