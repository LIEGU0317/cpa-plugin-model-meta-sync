package main

import (
	"bytes"
	"context"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"
)

// syncFixture writes a one-provider config pointing at upstream and returns
// its path.
func syncFixture(t *testing.T, upstream string) string {
	t.Helper()
	dir := t.TempDir()
	path := filepath.Join(dir, "config.yaml")
	fixture := strings.Replace(testConfigYAML("missing"), "http://127.0.0.1:19001", upstream, 1)
	if err := os.WriteFile(path, []byte(fixture), 0o644); err != nil {
		t.Fatal(err)
	}
	return path
}

func upstreamServer(t *testing.T) *httptest.Server {
	t.Helper()
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		_, _ = w.Write([]byte(testUpstreamBody))
	}))
	t.Cleanup(server.Close)
	return server
}

// A run that fails while writing must still be what /status reports;
// otherwise /status keeps showing the previous success.
func TestFailedWriteIsRecordedInStatus(t *testing.T) {
	if os.Geteuid() == 0 {
		t.Skip("root ignores directory permissions")
	}
	path := syncFixture(t, upstreamServer(t).URL)
	dir := filepath.Dir(path)
	if err := os.Chmod(dir, 0o555); err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = os.Chmod(dir, 0o755) })

	p := &pluginState{cfg: Config{ConfigPath: path, Mode: "missing", Fields: allFields, TimeoutSeconds: 5, BackupKeep: 3}}
	if _, err := p.runSync(context.Background(), syncOptions{}); err == nil {
		t.Fatal("expected the write to fail in a read-only directory")
	}
	p.mu.Lock()
	last := p.last
	p.mu.Unlock()
	if last == nil || last.Error == "" || last.OK || last.Written {
		t.Fatalf("failed run not recorded for /status: %+v", last)
	}
}

// Two writes inside the same second must keep two backups; the second one used
// to overwrite the first, losing the original file.
func TestBackupsWithinOneSecondDoNotCollide(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, "config.yaml")
	original := []byte(testConfigYAML("missing"))
	if err := os.WriteFile(path, original, 0o644); err != nil {
		t.Fatal(err)
	}
	cfg := Config{ConfigPath: path, BackupKeep: 10}
	for i, next := range []string{"# first\n", "# second\n"} {
		doc, err := loadConfigDocument(path)
		if err != nil {
			t.Fatal(err)
		}
		if _, err := writeConfigAtomic(cfg, doc, append(append([]byte(nil), doc.raw...), next...)); err != nil {
			t.Fatalf("write %d: %v", i, err)
		}
	}
	backups, _ := filepath.Glob(path + ".bak-modelmetasync-*")
	if len(backups) != 2 {
		t.Fatalf("want 2 backups, got %v", backups)
	}
	found := false
	for _, b := range backups {
		if raw, _ := os.ReadFile(b); string(raw) == string(original) {
			found = true
		}
	}
	if !found {
		t.Fatal("the pre-write original was not kept")
	}
}

// Upstream error bodies often echo the credential that was sent. Errors end up
// in the journal and in /status, so the key must not survive into them.
func TestUpstreamErrorRedactsCredentials(t *testing.T) {
	key := "sk-" + strings.Repeat("A", 28)
	hexToken := strings.Repeat("0123456789abcdef", 2)
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(http.StatusUnauthorized)
		_, _ = w.Write([]byte(`{"error":"invalid key ` + key + `","got":"` + r.Header.Get("Authorization") +
			`","token":"` + hexToken + `"}`))
	}))
	defer server.Close()
	_, err := fetchUpstream(context.Background(), Config{TimeoutSeconds: 5}, server.URL, []string{key}, nil, "", "/v1/models")
	if err == nil {
		t.Fatal("expected an error")
	}
	msg := err.Error()
	for _, secret := range []string{key, strings.TrimPrefix(key, "sk-"), hexToken} {
		if strings.Contains(msg, secret) {
			t.Fatalf("error leaks a credential: %s", msg)
		}
	}
	if !strings.Contains(msg, "HTTP 401") || !strings.Contains(msg, "invalid key") {
		t.Fatalf("error lost its diagnostic value: %s", msg)
	}
}

// The reload triggered by the plugin's own write must not start another sync;
// any other change to the file still must.
func TestOwnWriteSkipsStartupSync(t *testing.T) {
	defer func(previous time.Duration) { startupSyncDelay = previous }(startupSyncDelay)
	startupSyncDelay = 10 * time.Millisecond

	path := syncFixture(t, upstreamServer(t).URL)
	first := &pluginState{cfg: Config{ConfigPath: path, Mode: "missing", Fields: allFields, TimeoutSeconds: 5, BackupKeep: 3}}
	report, err := first.runSync(context.Background(), syncOptions{})
	if err != nil || !report.Written {
		t.Fatalf("setup sync: written=%v err=%v", report.Written, err)
	}
	before, _ := filepath.Glob(path + ".bak-modelmetasync-*")

	_, err = buildPlugin([]byte("sync_on_start: true\nmode: always\ntimeout_seconds: 5\nconfig_path: "+path+"\n"), "")
	if err != nil {
		t.Fatal(err)
	}
	stopBackground()
	after, _ := filepath.Glob(path + ".bak-modelmetasync-*")
	if len(after) != len(before) {
		t.Fatalf("startup sync ran for the plugin's own write: %d backups became %d", len(before), len(after))
	}

	doc, err := loadConfigDocument(path)
	if err != nil {
		t.Fatal(err)
	}
	panelEdit := append([]byte(nil), doc.raw...)
	panelEdit = append(panelEdit[:len(panelEdit)-1], []byte("  # panel edit\n"+string(doc.raw[len(doc.raw)-1:]))...)
	panelEdit = bytes.Replace(panelEdit, []byte("display-name: Model A"), []byte("display-name: renamed"), 1)
	if err := os.WriteFile(path, panelEdit, 0o644); err != nil {
		t.Fatal(err)
	}
	_, err = buildPlugin([]byte("sync_on_start: true\nmode: always\ntimeout_seconds: 5\nconfig_path: "+path+"\n"), "")
	if err != nil {
		t.Fatal(err)
	}
	deadline := time.Now().Add(5 * time.Second)
	for time.Now().Before(deadline) {
		if after2, _ := filepath.Glob(path + ".bak-modelmetasync-*"); len(after2) > len(after) {
			stopBackground()
			return
		}
		time.Sleep(20 * time.Millisecond)
	}
	stopBackground()
	t.Fatal("startup sync skipped a change the plugin did not make")
}

// A 2-space file must stay 2-space. The panel and the deploy scripts write
// 2 spaces, and rewriting everything at 4 spaces churns the whole file.
func TestRewriteKeepsIndent(t *testing.T) {
	for _, spaces := range []int{2, 4} {
		path := syncFixture(t, upstreamServer(t).URL)
		original, err := os.ReadFile(path)
		if err != nil {
			t.Fatal(err)
		}
		rewritten := bytes.ReplaceAll(original, []byte("    "), bytes.Repeat([]byte(" "), spaces))
		if err := os.WriteFile(path, rewritten, 0o644); err != nil {
			t.Fatal(err)
		}
		p := &pluginState{cfg: Config{ConfigPath: path, Mode: "missing", Fields: allFields, TimeoutSeconds: 5, BackupKeep: 3}}
		if _, err := p.runSync(context.Background(), syncOptions{}); err != nil {
			t.Fatalf("%d spaces: %v", spaces, err)
		}
		out, _ := os.ReadFile(path)
		if got := detectIndent(out); got != spaces {
			t.Fatalf("%d-space file rewritten at %d spaces", spaces, got)
		}
	}
}

// The provider's headers must be sent, a rejected first key must fall through
// to the next one, and the request must leave through the configured proxy.
func TestFetchUsesHeadersFallbackKeyAndProxy(t *testing.T) {
	proxy := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if got := r.Header.Get("Authorization"); got == "Bearer sk-first" {
			w.WriteHeader(http.StatusUnauthorized)
			return
		}
		if r.Header.Get("X-Upstream-Auth") != "tenant-1" {
			t.Errorf("header not forwarded: %v", r.Header)
		}
		if got := r.Header.Get("Authorization"); got != "Bearer sk-second" {
			t.Errorf("authorization = %q, want the second key", got)
		}
		_, _ = w.Write([]byte(`{"object":"list","data":[{"id":"m","display_name":"M","context_length":1}]}`))
	}))
	defer proxy.Close()

	calls := 0
	upstream := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		calls++
		w.WriteHeader(http.StatusUnauthorized)
	}))
	defer upstream.Close()

	models, err := fetchUpstream(context.Background(), Config{TimeoutSeconds: 5},
		upstream.URL, []string{"sk-first", "sk-second"}, map[string]string{"X-Upstream-Auth": "tenant-1"}, proxy.URL, "/v1/models")
	if err != nil {
		t.Fatal(err)
	}
	if len(models) != 1 || models[0].DisplayName != "M" {
		t.Fatalf("got %+v", models)
	}
	if calls != 0 {
		t.Fatalf("request bypassed the proxy: upstream saw %d calls", calls)
	}
}
