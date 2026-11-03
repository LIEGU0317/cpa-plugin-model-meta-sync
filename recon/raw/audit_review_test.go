package main

// Independent adversarial acceptance tests for model-meta-sync 0.1.1.
// Author: acceptance reviewer (not the plugin author).

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"gopkg.in/yaml.v3"
)

func auditCfg(t *testing.T, path string, fields ...string) Config {
	t.Helper()
	c := Config{ConfigPath: path, Mode: "missing", TimeoutSeconds: 5, BackupKeep: 3, Providers: []string{"Magpie"}}
	c.Fields = normalizeFields(fields)
	c.OverwriteFields = normalizeFields(fields)
	return c
}

func writeTempConfig(t *testing.T, body string) string {
	t.Helper()
	dir := t.TempDir()
	p := filepath.Join(dir, "config.yaml")
	if err := os.WriteFile(p, []byte(body), 0o644); err != nil {
		t.Fatalf("write temp config: %v", err)
	}
	return p
}

func shaOf(t *testing.T, p string) string {
	t.Helper()
	b, err := os.ReadFile(p)
	if err != nil {
		t.Fatalf("read %s: %v", p, err)
	}
	s := sha256.Sum256(b)
	return hex.EncodeToString(s[:])
}

func parseDoc(t *testing.T, raw []byte) *configDocument {
	t.Helper()
	var node yaml.Node
	if err := yaml.Unmarshal(raw, &node); err != nil {
		t.Fatalf("parseDoc: %v", err)
	}
	sum := sha256.Sum256(raw)
	return &configDocument{path: "audit.yaml", raw: raw, mode: 0o644, doc: &node, hash: hex.EncodeToString(sum[:])}
}

type upModel struct {
	ID            string `json:"id"`
	DisplayName   string `json:"display_name"`
	ContextLength int64  `json:"context_length"`
	Modalities    struct {
		Input  []string `json:"input"`
		Output []string `json:"output"`
	} `json:"modalities"`
}

func mockUpstream(t *testing.T, ids ...string) *httptest.Server {
	t.Helper()
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if !strings.HasSuffix(r.URL.Path, "/models") {
			http.NotFound(w, r)
			return
		}
		out := struct {
			Object string    `json:"object"`
			Data   []upModel `json:"data"`
		}{Object: "list"}
		for _, id := range ids {
			item := upModel{ID: id, DisplayName: "UP-" + id, ContextLength: 262144}
			item.Modalities.Input = []string{"text"}
			item.Modalities.Output = []string{"text"}
			out.Data = append(out.Data, item)
		}
		w.Header().Set("Content-Type", "application/json")
		_ = json.NewEncoder(w).Encode(out)
	}))
	t.Cleanup(srv.Close)
	return srv
}

func newState(cfg Config) *pluginState {
	return &pluginState{cfg: cfg, stop: make(chan struct{})}
}

func TestAudit_PathAllowed_0_1_1(t *testing.T) {
	cfg := auditCfg(t, "x", "display-name", "max-context-length", "thinking")
	allowed := map[string]bool{
		"display-name":       cfg.hasField("display-name"),
		"max-context-length": cfg.hasField("max-context-length"),
		"input-modalities":   cfg.hasField("input-modalities"),
		"output-modalities":  cfg.hasField("output-modalities"),
		"thinking":           cfg.hasField("thinking"),
	}
	sel := map[int]bool{0: true}
	cases := []struct{ path string; want bool; note string }{
		{"api-keys.openai-compatibility.0.models.3.display-name", true, "legit"},
		{"api-keys.openai-compatibility.0.models.3.thinking.levels.0", true, "legit thinking"},
		{"api-keys.openai-compatibility.0.models.3.name", false, "name locked"},
		{"api-keys.openai-compatibility.0.models.x.display-name", false, "non-numeric model idx"},
		{"api-keys.openai-compatibility.X.models.0.display-name", false, "non-numeric provider idx"},
		{"api-keys.openai-compatibility.1.models.0.display-name", false, "UNSELECTED provider"},
		{"api-keys.openai-compatibility.0.models", false, "count change"},
		{"completely.unrelated.0.models.0.display-name", false, "wrong ancestor"},
		{"api-keys.openai-compatibility.0.models.3.thinking.levels.x", false, "non-numeric level"},
		{"openai-compatibility.0.models.0.display-name", true, "legacy layout accepted"},
	}
	for _, c := range cases {
		got := pathAllowed(c.path, allowed, sel)
		mark := "OK"
		if got != c.want { mark = "DIVERGENCE" }
		t.Logf("[%s] %-56s want=%-5v got=%-5v (%s)", mark, c.path, c.want, got, c.note)
	}
}

func TestAudit_CollectDiff_DepthCapFailOpen(t *testing.T) {
	deepOld := map[string]any{}
	deepNew := map[string]any{}
	curOld, curNew := deepOld, deepNew
	for i := 0; i < 14; i++ {
		no, nn := map[string]any{}, map[string]any{}
		curOld["k"] = no
		curNew["k"] = nn
		curOld, curNew = no, nn
	}
	curOld["leaf"] = "a"
	curNew["leaf"] = "b"
	var out []string
	collectDiff("", deepOld, deepNew, &out, 0)
	if len(out) == 0 {
		t.Logf("DIVERGENCE: change at depth 15 produced NO diff path -> validator cannot see it (fail-open, cap=12)")
	} else {
		t.Logf("OK: deep change reported: %v", out)
	}
}

func TestAudit_MultiDocument(t *testing.T) {
	old := []byte("api-keys:\n    openai-compatibility: []\n---\nsecret: keep-me\n")
	doc := parseDoc(t, old)
	out, err := doc.encode()
	if err != nil {
		t.Logf("NOTE: encode refused multi-doc: %v", err)
		return
	}
	if strings.Contains(string(out), "keep-me") {
		t.Logf("OK: 2nd document preserved")
	} else {
		t.Logf("DIVERGENCE: encode dropped the 2nd YAML document -> silent data loss. out=%q", string(out))
	}
}

func TestAudit_EntryLevelAnchorSameProvider(t *testing.T) {
	cfgBody := "api-keys:\n    openai-compatibility:\n        - name: \"Magpie\"\n          base-url: \"%s\"\n          models:\n            - &m\n              name: \"alpha\"\n            - *m\n"
	srv := mockUpstream(t, "alpha")
	path := writeTempConfig(t, strings.Replace(cfgBody, "%s", srv.URL, 1))
	before := shaOf(t, path)
	st := newState(auditCfg(t, path, "display-name"))

	rep, err := st.runSync(context.Background(), syncOptions{})
	after := shaOf(t, path)
	written := rep != nil && rep.Written
	t.Logf("err=%v written=%v changed_fields=%v shaChanged=%v", err, written, rep != nil && rep.ChangedFields > 0, before != after)
	if rep != nil {
		for _, p := range rep.Providers {
			t.Logf("  provider=%s skipped=%q changed=%d fieldCount=%d", p.Name, p.Skipped, p.Changed, p.FieldCount)
		}
	}
	if rep != nil && written && len(rep.Providers) > 0 && rep.Providers[0].Skipped != "" {
		t.Logf("DIVERGENCE: reported Skipped=%q yet file WAS written and semantically changed", rep.Providers[0].Skipped)
	}
}

func TestAudit_CrossProviderAnchorBothSelected(t *testing.T) {
	cfgBody := "api-keys:\n    openai-compatibility:\n        - name: \"Magpie\"\n          base-url: \"%s\"\n          models:\n            - &shared\n              name: \"alpha\"\n        - name: \"Magpie2\"\n          base-url: \"%s\"\n          models:\n            - *shared\n"
	srv := mockUpstream(t, "alpha")
	path := writeTempConfig(t, strings.ReplaceAll(cfgBody, "%s", srv.URL))
	c := auditCfg(t, path, "display-name")
	c.Providers = []string{"Magpie", "Magpie2"}
	st := newState(c)

	rep, err := st.runSync(context.Background(), syncOptions{})
	t.Logf("both providers selected: err=%v written=%v", err, rep != nil && rep.Written)
	if rep != nil {
		for _, p := range rep.Providers {
			t.Logf("  provider=%s skipped=%q changed=%d", p.Name, p.Skipped, p.Changed)
		}
	}
	if rep != nil && rep.Written {
		for _, p := range rep.Providers {
			if p.Skipped != "" {
				t.Logf("DIVERGENCE: %q reported skipped but write proceeded (shared anchor leaked)", p.Name)
			}
		}
	}
}

func TestAudit_DisabledYesNotRecognised(t *testing.T) {
	cfgBody := "api-keys:\n    openai-compatibility:\n        - name: \"Magpie\"\n          base-url: \"%s\"\n          disabled: yes\n          models:\n            - name: \"alpha\"\n"
	srv := mockUpstream(t, "alpha")
	path := writeTempConfig(t, strings.Replace(cfgBody, "%s", srv.URL, 1))
	before := shaOf(t, path)
	st := newState(auditCfg(t, path, "display-name"))
	rep, err := st.runSync(context.Background(), syncOptions{})
	after := shaOf(t, path)
	skipped := ""
	if rep != nil && len(rep.Providers) > 0 { skipped = rep.Providers[0].Skipped }
	if strings.EqualFold(skipped, "provider disabled") {
		t.Logf("OK: `disabled: yes` recognised as disabled")
	} else {
		t.Logf("DIVERGENCE: `disabled: yes` not recognised; skipped=%q written=%v shaChanged=%v err=%v", skipped, rep != nil && rep.Written, before != after, err)
	}
}
