# model-meta-sync (plugin source)

This directory is the plugin module: `go.mod`, the CGO `c-shared` entry point (`abi.go`), the config
parser, the sync engine and the tests. Build, install, configuration and the management API are
documented once, in the [repository README](../README.md) - it is the only description that is kept
current.

Quick reference for working in this directory:

```bash
go test ./...                                   # unit tests, including the metadata contract
CGO_ENABLED=1 go build -buildmode=c-shared -o ../dist/model-meta-sync.so .
```

Release packaging (`make dist`, the archive layout the plugin store requires) is driven from the
repository root; see [Releases](../README.md#releases).
