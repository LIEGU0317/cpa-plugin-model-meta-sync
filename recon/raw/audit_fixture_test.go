package main

// Acceptance batch 2: upstream failures, non-target isolation, overwrite semantics, TOCTOU.

import (
	"context"
	"net/http"
	"net/http/httptest"
	"os"

	"strings"
	"testing"

	"gopkg.in/yaml.v3"
)

func serverRaw(t *testing.T, status int, body string, hdr map[string]string) *httptest.Server {
	t.Helper()
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		for k, v := range hdr {
			w.Header().Set(k, v)
		}
		w.WriteHeader(status)
		_, _ = w.Write([]byte(body))
	}))
	t.Cleanup(srv.Close)
	return srv
}

// A. upstream failure modes must never write.
func TestAudit_UpstreamFailuresNoWrite(t *testing.T) {
	cases := []struct {
		name   string
		status int
		body   string
		hdr    map[string]string
	}{
		{"401 unauthorised", 401, `{"error":"unauthorized"}`, map[string]string{"Content-Type": "application/json"}},
		{"html non-json", 200, `<html>oops</html>`, map[string]string{"Content-Type": "text/html"}},
		{"empty body", 200, ``, nil},
		{"json array not object", 200, `[{"id":"alpha"}]`, map[string]string{"Content-Type": "application/json"}},
		{"truncated json", 200, `{"data":[{"id":"alpha"`, map[string]string{"Content-Type": "application/json"}},
	}
	cfgTmpl := "api-keys:\n    openai-compatibility:\n        - name: \"Magpie\"\n          base-url: \"%s\"\n          models:\n            - name: \"alpha\"\n              max-context-length: 128\n"
	for _, c := range cases {
		srv := serverRaw(t, c.status, c.body, c.hdr)
		path := writeTempConfig(t, strings.Replace(cfgTmpl, "%s", srv.URL, 1))
		before := shaOf(t, path)
		st := newState(auditCfg(t, path, "display-name", "max-context-length"))
		rep, err := st.runSync(context.Background(), syncOptions{})
		after := shaOf(t, path)
		written := rep != nil && rep.Written
		verdict := "OK"
		if written || before != after {
			verdict = "DIVERGENCE(wrote on failure)"
		}
		if err == nil && !written {
			verdict = "NOTE(no error but no write)"
		}
		t.Logf("[%s] %-24s err=%v written=%v shaUnchanged=%v", verdict, c.name, err != nil, written, before == after)
	}
}

// B. duplicate ids + missing id upstream.
func TestAudit_UpstreamDuplicateAndMissingID(t *testing.T) {
	body := `{"object":"list","data":[
      {"id":"alpha","display_name":"FIRST","context_length":100},
      {"id":"alpha","display_name":"SECOND","context_length":999},
      {"display_name":"NO-ID","context_length":555},
      {"id":"","display_name":"EMPTY","context_length":777}
    ]}`
	srv := serverRaw(t, 200, body, map[string]string{"Content-Type": "application/json"})
	cfgBody := "api-keys:\n    openai-compatibility:\n        - name: \"Magpie\"\n          base-url: \"%s\"\n          models:\n            - name: \"alpha\"\n"
	path := writeTempConfig(t, strings.Replace(cfgBody, "%s", srv.URL, 1))
	st := newState(auditCfg(t, path, "display-name", "max-context-length"))
	rep, err := st.runSync(context.Background(), syncOptions{})
	out, _ := os.ReadFile(path)
	t.Logf("duplicate/missing id: err=%v written=%v", err, rep != nil && rep.Written)
	t.Logf("resulting model entries:\n%s", out)
	if strings.Contains(string(out), "SECOND") || strings.Contains(string(out), "999") {
		t.Logf("DIVERGENCE: duplicate id took the LAST entry, not the first")
	} else {
		t.Logf("OK: duplicate id took the first entry")
	}
	if strings.Contains(string(out), "NO-ID") || strings.Contains(string(out), "555") {
		t.Logf("DIVERGENCE: id-less upstream entry leaked into config")
	} else {
		t.Logf("OK: id-less upstream entries ignored")
	}
}

// C. non-target provider must be byte-identical.
func TestAudit_NonTargetProviderUntouched(t *testing.T) {
	srv := serverRaw(t, 200, `{"object":"list","data":[{"id":"alpha","display_name":"UP","context_length":262144}]}`,
		map[string]string{"Content-Type": "application/json"})
	cfgBody := "api-keys:\n    openai-compatibility:\n        - name: \"Magpie\"\n          base-url: \"%s\"\n          models:\n            - name: \"alpha\"\n        - name: \"Other\"\n          base-url: \"http://127.0.0.1:19999\"\n          keys:\n            - api-key: \"sk-other\"\n          models:\n            - name: \"alpha\"\n              display-name: \"KEEP\"\n              max-context-length: 4242\n          headers:\n            X-Test: \"yes\"\n"
	path := writeTempConfig(t, strings.Replace(cfgBody, "%s", srv.URL, 1))
	beforeBytes, _ := os.ReadFile(path)
	st := newState(auditCfg(t, path, "display-name", "max-context-length"))
	rep, err := st.runSync(context.Background(), syncOptions{})
	afterBytes, _ := os.ReadFile(path)
	t.Logf("non-target: err=%v written=%v", err, rep != nil && rep.Written)
	// extract the Other block from both
	seg := func(b []byte) string {
		s := string(b)
		i := strings.Index(s, "- name: \"Other\"")
		if i < 0 { return "<missing>" }
		return s[i:]
	}
	if seg(beforeBytes) == seg(afterBytes) {
		t.Logf("OK: non-target provider block byte-identical")
	} else {
		t.Logf("DIVERGENCE: non-target provider block changed\n--- before ---\n%s\n--- after ---\n%s", seg(beforeBytes), seg(afterBytes))
	}
}

// D. missing + overwrite_fields: thinking must NOT be overwritten.
func TestAudit_OverwriteFieldsRespectsThinking(t *testing.T) {
	body := `{"object":"list","data":[{"id":"alpha","display_name":"UP-NAME","context_length":262144,
      "modalities":{"input":["text","image"],"output":["text"]},
      "reasoning":true,"reasoning_levels":["low","high"]}]}`
	srv := serverRaw(t, 200, body, map[string]string{"Content-Type": "application/json"})
	cfgBody := "api-keys:\n    openai-compatibility:\n        - name: \"Magpie\"\n          base-url: \"%s\"\n          models:\n            - name: \"alpha\"\n              display-name: \"HAND-SET\"\n              max-context-length: 1000\n              thinking:\n                levels: [\"low\",\"medium\",\"high\"]\n"
	path := writeTempConfig(t, strings.Replace(cfgBody, "%s", srv.URL, 1))
	c := auditCfg(t, path, "display-name", "max-context-length", "input-modalities", "output-modalities", "thinking")
	c.OverwriteFields = normalizeFields([]string{"display-name", "max-context-length", "input-modalities", "output-modalities"})
	st := newState(c)
	rep, err := st.runSync(context.Background(), syncOptions{})
	out, _ := os.ReadFile(path)
	t.Logf("overwrite_fields: err=%v written=%v out=\n%s", err, rep != nil && rep.Written, out)
	if strings.Contains(string(out), "\"medium\"") || strings.Contains(string(out), "- medium") {
		t.Logf("OK: thinking.levels preserved (not overwritten)")
	} else {
		t.Logf("DIVERGENCE: thinking.levels was overwritten despite not being in overwrite_fields")
	}
	if strings.Contains(string(out), "UP-NAME") {
		t.Logf("OK: display-name overwritten per overwrite_fields")
	} else {
		t.Logf("DIVERGENCE: display-name not overwritten although listed")
	}
}

// E. TOCTOU: file changes between load and write.
func TestAudit_TOCTOU_ConflictDetection(t *testing.T) {
	path := writeTempConfig(t, "api-keys:\n    openai-compatibility:\n        - name: \"Magpie\"\n          base-url: \"http://x\"\n          models:\n            - name: \"alpha\"\n")
	doc, err := loadConfigDocument(path)
	if err != nil { t.Fatalf("load: %v", err) }
	newRaw, _ := doc.encode()
	// someone else saves the config now
	if err := os.WriteFile(path, []byte("api-keys:\n    openai-compatibility: []\n# human edit\n"), 0o644); err != nil {
		t.Fatalf("concurrent write: %v", err)
	}
	_, errWrite := writeConfigAtomic(Config{ConfigPath: path, BackupKeep: 3}, doc, newRaw)
	if errWrite == nil {
		t.Logf("DIVERGENCE: writeConfigAtomic accepted a stale document (no conflict detection)")
	} else {
		t.Logf("OK: conflict detected: %v", errWrite)
	}
	// and the human edit must survive
	b, _ := os.ReadFile(path)
	if strings.Contains(string(b), "human edit") {
		t.Logf("OK: concurrent human edit preserved")
	} else {
		t.Logf("DIVERGENCE: concurrent human edit was clobbered")
	}
}

// F. anomaly types must not corrupt.
func TestAudit_AnomalyTypes(t *testing.T) {
	srv := serverRaw(t, 200, `{"object":"list","data":[{"id":"alpha","display_name":"UP","context_length":262144,
      "modalities":{"input":["text"]},"reasoning":true,"reasoning_levels":["low"]}]}`,
		map[string]string{"Content-Type": "application/json"})
	cases := []struct{ name, modelBlock string }{
		{"max-context-length: abc", "              max-context-length: \"abc\"\n"},
		{"max-context-length: {} map", "              max-context-length: {a: b}\n"},
		{"thinking: scalar string", "              thinking: \"off\"\n"},
		{"thinking: list", "              thinking: [a, b]\n"},
		{"modalities: scalar", "              input-modalities: \"text\"\n"},
		{"duplicate key in model", "              display-name: \"one\"\n              display-name: \"two\"\n"},
	}
	for _, c := range cases {
		cfgBody := "api-keys:\n    openai-compatibility:\n        - name: \"Magpie\"\n          base-url: \"%s\"\n          models:\n            - name: \"alpha\"\n" + c.modelBlock
		path := writeTempConfig(t, strings.Replace(cfgBody, "%s", srv.URL, 1))
		before := shaOf(t, path)
		st := newState(auditCfg(t, path, "display-name", "max-context-length", "input-modalities", "thinking"))
		rep, err := st.runSync(context.Background(), syncOptions{})
		after := shaOf(t, path)
		out, _ := os.ReadFile(path)
		valid := true
		var parsed map[string]any
		if e := yaml.Unmarshal(out, &parsed); e != nil { valid = false }
		t.Logf("[%s] %-28s err=%v written=%v shaChanged=%v stillValidYAML=%v",
			map[bool]string{true: "OK", false: "CHECK"}[valid], c.name, err != nil, rep != nil && rep.Written, before != after, valid)
	}
}
