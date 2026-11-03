package main

import (
	"context"
	"strings"
	"testing"
)

// V4-1: anchor guard must emit a warning visible in the report.
func TestV4_AnchorWarning(t *testing.T) {
	cfgBody := "api-keys:\n    openai-compatibility:\n        - name: \"Magpie\"\n          base-url: \"%s\"\n          models:\n            - &shared\n              name: \"alpha\"\n        - name: \"Magpie2\"\n          base-url: \"%s\"\n          models:\n            - *shared\n"
	srv := mockUpstream(t, "alpha")
	path := writeTempConfig(t, strings.ReplaceAll(cfgBody, "%s", srv.URL))
	c := auditCfg(t, path, "display-name")
	c.Providers = []string{"Magpie", "Magpie2"}
	st := newState(c)
	rep, err := st.runSync(context.Background(), syncOptions{})
	t.Logf("err=%v written=%v warnings=%d", err, rep != nil && rep.Written, func() int {
		if rep == nil { return -1 }
		return len(rep.Warnings)
	}())
	if rep != nil {
		for i, w := range rep.Warnings {
			t.Logf("  warning[%d]=%q", i, w)
		}
		if len(rep.Warnings) == 0 {
			t.Logf("DIVERGENCE: no warning emitted")
		} else if len(rep.Warnings) > 1 {
			t.Logf("NOTE: warning duplicated per selected provider (%d copies)", len(rep.Warnings))
		}
	}
}

// V4-2: clean config must NOT emit the anchor warning.
func TestV4_NoSpuriousWarning(t *testing.T) {
	cfgBody := "api-keys:\n    openai-compatibility:\n        - name: \"Magpie\"\n          base-url: \"%s\"\n          models:\n            - name: \"alpha\"\n"
	srv := mockUpstream(t, "alpha")
	path := writeTempConfig(t, strings.Replace(cfgBody, "%s", srv.URL, 1))
	st := newState(auditCfg(t, path, "display-name"))
	rep, err := st.runSync(context.Background(), syncOptions{})
	n := -1
	if rep != nil { n = len(rep.Warnings) }
	if n == 0 {
		t.Logf("OK: no warning on clean config (err=%v)", err)
	} else {
		t.Logf("DIVERGENCE: spurious warnings=%d", n)
	}
}
