# Plugin-store submission

CPA loads plugins from its own release assets, not from this repository, so the
files here are the **store metadata** rather than a binary distribution channel.

- [`registry-entry.json`](registry-entry.json) — the entry to append to
  `plugins[]` in `router-for-me/CLIProxyAPI-Plugins-Store`'s `registry.json`.
  The store merges pull requests in order; an update to `logo`, `tags` or
  `description` keeps the entry's original position.
- `version` is intentionally omitted: the store reads the repository's latest
  release and derives the version from the tag, so the entry never goes stale.

## What a release must contain

Every release tag is `v<version>` with a dotted numeric version, and carries one
zip per platform plus one checksum file:

```text
model-meta-sync_0.3.1_linux_amd64.zip
model-meta-sync_0.3.1_linux_arm64.zip
model-meta-sync_0.3.1_darwin_amd64.zip
model-meta-sync_0.3.1_darwin_arm64.zip
model-meta-sync_0.3.1_windows_amd64.zip
checksums.txt
```

Each zip holds exactly one dynamic library at its root — `model-meta-sync.so`,
`model-meta-sync.dylib` or `model-meta-sync.dll`. `checksums.txt` is sha256sum
format:

```text
<sha256>  model-meta-sync_0.3.1_linux_amd64.zip
```

`make dist` writes one zip for the current `GOOS`/`GOARCH`; `make checksums`
covers every zip present in `dist/`; `make verify-dist` rejects an archive whose
layout the CPA installer would refuse. `.github/workflows/release.yml` runs the
same targets per platform and publishes the assets.

## Verifying an entry locally

The validation rules live in CPA's `internal/pluginstore` package. Against a
CLIProxyAPI checkout at the release you target:

```bash
cd /path/to/CLIProxyAPI
cat > /tmp/registry-check_test.go <<'EOF'
package pluginstore

import (
	"os"
	"testing"
)

func TestSubmittedEntry(t *testing.T) {
	entry, err := os.ReadFile("/path/to/store/registry-entry.json")
	if err != nil {
		t.Fatal(err)
	}
	registry := []byte(`{"schema_version":1,"plugins":[` + string(entry) + `]}`)
	if _, err := ParseRegistry(registry); err != nil {
		t.Fatal(err)
	}
}
EOF
cp /tmp/registry-check_test.go internal/pluginstore/
go test ./internal/pluginstore -run TestSubmittedEntry
```

## Submitting

Open one pull request against `router-for-me/CLIProxyAPI-Plugins-Store` that
changes only `registry.json`, and include:

- the repository URL and the latest `v<version>` tag,
- evidence that the platform zip and `checksums.txt` exist in that release,
- one sentence on the capability the plugin adds.

Submitted: [CLIProxyAPI-Plugins-Store#327](https://github.com/router-for-me/CLIProxyAPI-Plugins-Store/pull/327)
(entry appended at the end, per the ordering rule for newly introduced plugins;
the fork branch is `LIEGU0317:add-model-meta-sync`). Until it merges, install
from a release archive as described in the root README.
