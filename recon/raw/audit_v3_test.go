package main

// v3 focused re-check: global alias detection + regression of the v2 bypass.

import (
	"context"
	"os"
	"strings"
	"testing"
)

// V3-1: the v2 bypass — shared entry-level anchor ACROSS providers, both selected.
func TestV3_CrossProviderEntryAnchorBothSelected(t *testing.T) {
	cfgBody := "api-keys:\n    openai-compatibility:\n        - name: \"Magpie\"\n          base-url: \"%s\"\n          models:\n            - &shared\n              name: \"alpha\"\n        - name: \"Magpie2\"\n          base-url: \"%s\"\n          models:\n            - *shared\n"
	srv := mockUpstream(t, "alpha")
	path := writeTempConfig(t, strings.ReplaceAll(cfgBody, "%s", srv.URL))
	before, _ := os.ReadFile(path)
	c := auditCfg(t, path, "display-name")
	c.Providers = []string{"Magpie", "Magpie2"}
	st := newState(c)
	rep, err := st.runSync(context.Background(), syncOptions{})
	after, _ := os.ReadFile(path)
	if string(before) == string(after) && (rep == nil || !rep.Written) {
		t.Logf("OK: no write, bytes unchanged (err=%v)", err)
		for _, p := range rep.Providers {
			t.Logf("   provider=%s skipped=%q", p.Name, p.Skipped)
		}
	} else {
		t.Logf("BYPASS-STILL-PRESENT: written=%v changed=%v", rep != nil && rep.Written, string(before) != string(after))
		t.Logf("result:\n%s", after)
	}
}

// V3-2: single provider, item anchor aliased to a top-level anchor, no second provider.
func TestV3_TopLevelItemAlias(t *testing.T) {
	cfgBody := "anchors:\n    m: &m\n        name: \"alpha\"\napi-keys:\n    openai-compatibility:\n        - name: \"Magpie\"\n          base-url: \"%s\"\n          models:\n            - *m\n"
	srv := mockUpstream(t, "alpha")
	path := writeTempConfig(t, strings.Replace(cfgBody, "%s", srv.URL, 1))
	before, _ := os.ReadFile(path)
	st := newState(auditCfg(t, path, "display-name"))
	rep, err := st.runSync(context.Background(), syncOptions{})
	after, _ := os.ReadFile(path)
	if string(before) == string(after) {
		t.Logf("OK: skipped, unchanged (err=%v, skipped=%q)", err, func() string {
			if rep != nil && len(rep.Providers) > 0 { return rep.Providers[0].Skipped }
			return ""
		}())
	} else {
		t.Logf("CHECK: file changed")
	}
}

// V3-3: anchor on some OTHER provider (not selected) must still block the batch (global).
func TestV3_AnchorInUnselectedProviderBlocksBatch(t *testing.T) {
	cfgBody := "api-keys:\n    openai-compatibility:\n        - name: \"Magpie\"\n          base-url: \"%s\"\n          models:\n            - name: \"alpha\"\n        - name: \"Other\"\n          base-url: \"http://127.0.0.1:19999\"\n          models:\n            - &o\n              name: \"beta\"\n            - *o\n"
	srv := mockUpstream(t, "alpha")
	path := writeTempConfig(t, strings.Replace(cfgBody, "%s", srv.URL, 1))
	before, _ := os.ReadFile(path)
	st := newState(auditCfg(t, path, "display-name"))
	rep, err := st.runSync(context.Background(), syncOptions{})
	after, _ := os.ReadFile(path)
	skipped := ""
	if rep != nil && len(rep.Providers) > 0 { skipped = rep.Providers[0].Skipped }
	if string(before) == string(after) {
		t.Logf("OK: global alias guard blocked batch (skipped=%q, err=%v)", skipped, err)
	} else {
		t.Logf("CHECK: file changed despite anchor in unselected provider")
	}
}

// V3-4: clean config (no anchors) must still sync — guard must not over-block.
func TestV3_NoAnchorStillSyncs(t *testing.T) {
	cfgBody := "api-keys:\n    openai-compatibility:\n        - name: \"Magpie\"\n          base-url: \"%s\"\n          models:\n            - name: \"alpha\"\n"
	srv := mockUpstream(t, "alpha")
	path := writeTempConfig(t, strings.Replace(cfgBody, "%s", srv.URL, 1))
	st := newState(auditCfg(t, path, "display-name"))
	rep, err := st.runSync(context.Background(), syncOptions{})
	if rep != nil && rep.Written {
		t.Logf("OK: normal sync still works (err=%v)", err)
	} else {
		t.Logf("DIVERGENCE: over-blocked a clean config (err=%v)", err)
	}
}
