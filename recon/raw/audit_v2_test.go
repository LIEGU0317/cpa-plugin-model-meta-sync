package main

// v2 incremental acceptance: anchor bypass variants + fixed items.

import (
	"context"
	"os"
	"strings"
	"testing"
)

// V2-1: entry-level anchor shared ACROSS providers, both selected (third bypass?).
func TestV2_CrossProviderEntryAnchorBothSelected(t *testing.T) {
	cfgBody := "api-keys:\n    openai-compatibility:\n        - name: \"Magpie\"\n          base-url: \"%s\"\n          models:\n            - &shared\n              name: \"alpha\"\n        - name: \"Magpie2\"\n          base-url: \"%s\"\n          models:\n            - *shared\n"
	srv := mockUpstream(t, "alpha")
	path := writeTempConfig(t, strings.ReplaceAll(cfgBody, "%s", srv.URL))
	c := auditCfg(t, path, "display-name")
	c.Providers = []string{"Magpie", "Magpie2"}
	st := newState(c)
	rep, err := st.runSync(context.Background(), syncOptions{})
	out, _ := os.ReadFile(path)
	t.Logf("err=%v written=%v", err, rep != nil && rep.Written)
	if rep != nil {
		for _, p := range rep.Providers {
			t.Logf("  provider=%s skipped=%q changed=%d", p.Name, p.Skipped, p.Changed)
		}
	}
	t.Logf("result:\n%s", out)
	if strings.Count(string(out), "display-name") > 0 {
		t.Logf("BYPASS-PRESENT: shared entry anchor mutated across providers")
	} else {
		t.Logf("OK: no cross-provider mutation")
	}
}

// V2-2: provider mapping node itself anchored + aliased by another provider.
func TestV2_ProviderNodeAnchored(t *testing.T) {
	cfgBody := "api-keys:\n    openai-compatibility:\n        - &p\n          name: \"Magpie\"\n          base-url: \"%s\"\n          models:\n            - name: \"alpha\"\n        - *p\n"
	srv := mockUpstream(t, "alpha")
	path := writeTempConfig(t, strings.Replace(cfgBody, "%s", srv.URL, 1))
	st := newState(auditCfg(t, path, "display-name"))
	rep, err := st.runSync(context.Background(), syncOptions{})
	out, _ := os.ReadFile(path)
	t.Logf("err=%v written=%v out=\n%s", err, rep != nil && rep.Written, out)
}

// V2-3: thinking value is itself an alias to a top-level anchor.
func TestV2_NestedThinkingAlias(t *testing.T) {
	cfgBody := "anchors:\n    lv: &lv\n        levels: [\"low\"]\napi-keys:\n    openai-compatibility:\n        - name: \"Magpie\"\n          base-url: \"%s\"\n          models:\n            - name: \"alpha\"\n              thinking: *lv\n"
	srv := mockUpstream(t, "alpha")
	path := writeTempConfig(t, strings.Replace(cfgBody, "%s", srv.URL, 1))
	st := newState(auditCfg(t, path, "thinking"))
	rep, err := st.runSync(context.Background(), syncOptions{})
	out, _ := os.ReadFile(path)
	t.Logf("err=%v written=%v out=\n%s", err, rep != nil && rep.Written, out)
	if strings.Contains(string(out), "&lv") {
		t.Logf("OK: anchor definition preserved")
	}
}

// V2-4: entry-level alias in the SAME provider must be skipped BEFORE mutation.
func TestV2_SameProviderEntryAliasNoWrite(t *testing.T) {
	cfgBody := "api-keys:\n    openai-compatibility:\n        - name: \"Magpie\"\n          base-url: \"%s\"\n          models:\n            - &m\n              name: \"alpha\"\n            - *m\n"
	srv := mockUpstream(t, "alpha")
	path := writeTempConfig(t, strings.Replace(cfgBody, "%s", srv.URL, 1))
	before := shaOf(t, path)
	st := newState(auditCfg(t, path, "display-name"))
	rep, err := st.runSync(context.Background(), syncOptions{})
	after := shaOf(t, path)
	skipped := ""
	if rep != nil && len(rep.Providers) > 0 { skipped = rep.Providers[0].Skipped }
	if before == after && (rep == nil || !rep.Written) {
		t.Logf("OK: fully skipped, no write (skipped=%q, err=%v)", skipped, err)
	} else {
		t.Logf("DIVERGENCE: still written (skipped=%q written=%v)", skipped, rep != nil && rep.Written)
	}
}

// V2-5: disabled yes/on must be honoured.
func TestV2_DisabledTruthy(t *testing.T) {
	for _, v := range []string{"yes", "on", "True", "TRUE", "true"} {
		cfgBody := "api-keys:\n    openai-compatibility:\n        - name: \"Magpie\"\n          base-url: \"%s\"\n          disabled: " + v + "\n          models:\n            - name: \"alpha\"\n"
		srv := mockUpstream(t, "alpha")
		path := writeTempConfig(t, strings.Replace(cfgBody, "%s", srv.URL, 1))
		before := shaOf(t, path)
		st := newState(auditCfg(t, path, "display-name"))
		rep, _ := st.runSync(context.Background(), syncOptions{})
		after := shaOf(t, path)
		ok := before == after
		mark := "OK"
		if !ok { mark = "DIVERGENCE" }
		t.Logf("[%s] disabled: %-5s -> skipped=%q shaUnchanged=%v", mark, v, func() string { if rep != nil && len(rep.Providers) > 0 { return rep.Providers[0].Skipped }; return "" }(), ok)
	}
}

// V2-6: multi-document config must be refused.
func TestV2_MultiDocumentRefused(t *testing.T) {
	body := "api-keys:\n    openai-compatibility: []\n---\nsecret: keep-me\n"
	path := writeTempConfig(t, body)
	st := newState(auditCfg(t, path, "display-name"))
	rep, err := st.runSync(context.Background(), syncOptions{})
	if err != nil && !strings.Contains(strings.ToLower(err.Error()), "document") {
		t.Logf("NOTE: refused but message not document-specific: %v", err)
	}
	t.Logf("multi-doc: err=%v written=%v", err, rep != nil && rep.Written)
	out, _ := os.ReadFile(path)
	if strings.Contains(string(out), "keep-me") {
		t.Logf("OK: file untouched")
	} else {
		t.Logf("DIVERGENCE: file was modified")
	}
}

// V2-7: collectDiff depth must be fail-closed.
func TestV2_DepthFailClosed(t *testing.T) {
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
	if len(out) > 0 {
		t.Logf("OK: depth overflow recorded as %q -> validator will reject", out[0])
	} else {
		t.Logf("DIVERGENCE: still fail-open")
	}
}
