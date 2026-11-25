package main

import (
	"testing"

	"gopkg.in/yaml.v3"
)

// CPA resolves proxy-url like this: a credential's own value wins, otherwise
// the group value applies. The sync has to read the same value, or it fetches
// the catalog through a different route than the one the requests use.
func TestProviderProxyURLPrecedence(t *testing.T) {
	cases := []struct {
		name string
		yaml string
		want string
	}{
		{
			name: "per-key override wins over the group",
			yaml: "base-url: http://up\nproxy-url: socks5://127.0.0.1:7891\nkeys:\n  - api-key: a\n    proxy-url: direct\n",
			want: "direct",
		},
		{
			name: "group value applies when the key has none",
			yaml: "base-url: http://up\nproxy-url: socks5://127.0.0.1:7891\nkeys:\n  - api-key: a\n",
			want: "socks5://127.0.0.1:7891",
		},
		{
			name: "no proxy anywhere stays empty",
			yaml: "base-url: http://up\nkeys:\n  - api-key: a\n",
			want: "",
		},
	}
	for _, tc := range cases {
		var node yaml.Node
		if err := yaml.Unmarshal([]byte(tc.yaml), &node); err != nil {
			t.Fatalf("%s: %v", tc.name, err)
		}
		prov := node.Content[0]
		if got := providerProxyURL(prov); got != tc.want {
			t.Fatalf("%s: got %q, want %q", tc.name, got, tc.want)
		}
	}
}