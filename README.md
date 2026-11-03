# cpa-plugin-model-meta-sync

A native [CLIProxyAPI](https://github.com/router-for-me/CLIProxyAPI) (CPA) plugin that fills the
model metadata CPA stores per provider entry (`api-keys.openai-compatibility[].models[]`) from each
gateway's own OpenAI-compatible `GET /v1/models` response, so `display-name`,
`max-context-length`, `input-modalities`, `output-modalities` and `thinking.levels` no longer have to
be typed by hand.

- Host: **CPA v8** (plugin ABI 1 / schema 8), Go `c-shared` plugin (`CGO`).
- Scope: **every enabled `openai-compatibility` provider** in the config. No vendor, host or address
  is hard-coded and no allow-list is required; `providers:` only narrows the set when you want to.
- Matching: provider entries are selected **by provider name**. Models are matched **by model name**
  (exact -> case -> bare name -> separator-free spelling -> catalog suffixes), so a gateway's naming
  style does not have to be declared anywhere.
- Current release: **0.2.2**.

## Why a plugin

CPA never queries an openai-compatibility provider's `/v1/models`; the provider model list only
exists in `config.yaml`. This plugin fills that gap: it fetches each gateway's catalog, maps the
fields CPA understands, merges them into the provider entries, validates the change and lets CPA's
file watcher hot-reload the config. Models are never added or removed.

## What gets synced

| Upstream (OpenAI-compatible) | CPA provider model key | Rule |
|---|---|---|
| `id` | `name` | exact match first, then the relaxed name spellings below |
| `display_name` (fallback `display-name`, then `name`) | `display-name` | fill if empty; overwrite only when opted in |
| `context_length` (`context_window`, `max_input_tokens` fallbacks) | `max-context-length` | fill if `0`; overwrite only when opted in. `max_output_tokens` is an **output** limit, not context |
| `modalities.input` | `input-modalities` | skipped when the upstream omits it (never guessed) |
| `modalities.output` | `output-modalities` | same |
| `reasoning_levels` / `supported_reasoning_levels[].effort` / `reasoning_supported_efforts` | `thinking.levels` | requires `reasoning` / `supports_reasoning` / `only_reasoning`; level names are filtered to CPA's vocabulary |

Gateways such as workbuddy2api publish the human label as `name` and only `modalities.input`; that is
handled, and a label identical to the id is ignored. A single `reasoning_effort` value is **not**
turned into a thinking ladder - that would narrow the levels CPA offers.

Not representable in CPA config: `max_output_tokens` (no field), `native_endpoints`, `pricing`,
`service_tiers`, `supported_parameters`.

## Name matching

A configured model name is resolved against the provider's own upstream list, in this order:

1. exact `name` / `alias`, then case-insensitive;
2. the bare name (everything after the last `/`);
3. a separator-free spelling (`DeepSeek-V4.1-Flash`, `deepseek-v41-flash` and `deepseek v4.1 flash`
   all fold to `deepseekv41flash`);
4. the same spelling plus a catalog decoration suffix (`-exp`, `-experimental`, `-latest`,
   `-preview`, `-free`);
5. the configured name with a trailing deployment qualifier stripped (`-self`, `-official`), and only
   after every other spelling failed.

Every relaxation is refused when two different upstream ids collapse to the same key - the plugin
prefers no metadata over the wrong metadata. One-way suffixes are intentional: `gpt-4o-free` is a
different endpoint from `gpt-4o`.

## Behavior contract

- `mode: missing` (default) fills empty fields only. `overwrite_fields` opts specific fields into
  authoritative overwrite; `mode: always` recomputes every listed field. An absent
  `overwrite_fields` means "never overwrite" - it does not inherit the empty-list-means-all rule.
- One unreachable gateway does not block the others: failures are reported per provider and that
  provider's entries stay untouched. Only when every selected upstream fails is nothing written.
- Disabled providers are skipped unless `include_disabled: true`.
- Provider identity is the configured **name** only. Reports, logs and error messages never echo
  `base-url`, hosts, IPs or ports.
- Never adds/removes models and never edits `name`, `alias`, `base-url`, keys or other providers.
- Safety: timestamped backup, provider-scoped semantic-diff validation, sha256 conflict detection
  before writing, staged copy for recovery, in-place write so CPA's inode watcher fires the hot reload.
- YAML anchors/aliases around a provider's model list skip **that provider** (reported in
  `warnings[]`) instead of leaking writes across providers.
- Multi-document YAML configs are rejected, never rewritten.

## Install

1. Build (Go >= 1.26 + C toolchain for the target arch):

   ```bash
   make build GOOS=linux GOARCH=amd64
   # -> build/model-meta-sync-v0.2.0.so
   ```

2. Copy the `.so` to `<cpa>/plugins/<goos>/<goarch>/model-meta-sync-v0.2.0.so`.

3. Add the config block to `config.yaml` and restart CPA once (the plugin host scans `.so` files at
   startup):

   ```yaml
   plugins:
       configs:
           model-meta-sync:
               enabled: true
               # config_path: /path/to/config.yaml          # optional; omitted = host -config / ./config.yaml
               mode: missing                               # missing | always
               fields: [display-name, max-context-length, input-modalities, output-modalities, thinking]
               # providers: [Gateway A]                    # optional filter; omit to sync every provider
               # include_disabled: false
               timeout_seconds: 20
               backup_keep: 10
               sync_on_start: true
               dry_run_on_start: false                     # true = startup run only reports
               interval_minutes: 0                         # >0 repeats the sync on a timer
               # api_key: sk-...                           # optional; defaults to the provider's first key
   ```

`config_path` is optional. The plugin resolves the CPA config from the host process: its `-config`
flag when present, otherwise `config.yaml` in the process working directory - the same fallback CPA
itself uses. It needs the path only because it writes the file directly (the host ABI exposes no
config read/write), which is also why other plugins never ask for it.

Details and rollback: [`runbook/deploy.md`](runbook/deploy.md).

## Management API

Requires the normal CPA management key (`X-Management-Key` or `Authorization: Bearer ...`).

| Method | Path | Purpose |
|---|---|---|
| GET | `/v0/management/model-meta-sync/status` | last run report + effective config |
| GET | `/v0/management/model-meta-sync/config` | effective config + redacted raw plugin config |
| GET | `/v0/management/model-meta-sync/preview` | dry run: compute the patch, write nothing |
| POST | `/v0/management/model-meta-sync/sync` | fetch upstream `/v1/models` and write; `?dry_run=1`, `?provider=Name` |

Every upstream failing -> HTTP 502, `ok:false`, file untouched. Unknown provider name -> 400.
Reports identify providers by name only - no `base_url` field exists.

## Build, test, deploy

```bash
make build            # plugin .so for the host arch
make test             # go test ./...
make fmt vet
```

The sandbox integration suite (mock upstream + real CPA binary) lives in the development workspace and
is not part of this repository; results are archived under [`evidence/`](evidence/).

## Evidence

- [`recon/models-diff.md`](recon/models-diff.md) - field-level upstream vs CPA comparison.
- [`recon/plugin-landscape.md`](recon/plugin-landscape.md) - plugin-store survey + CPA v8 ABI matrix.
- [`recon/independent-acceptance.md`](recon/independent-acceptance.md) - acceptance rounds, PASS/FAIL.
- [`evidence/`](evidence/) - unit/integration output and production deploy records.

## Known limitations

- CPA has no field for upstream output-token limits; `max_output_tokens` cannot be synced.
- Aliases are CPA's global model key; colliding aliases hide entries from `/v1/models`. Use provider
  `prefix` or unique aliases - out of scope here.
- Overwriting `thinking` can downgrade clients requesting an unsupported level, because CPA clamps to
  the nearest level (ties choose the lower one). `thinking` stays fill-if-empty by default.
- Installing/upgrading the plugin needs one CPA restart; syncing data afterwards is hot-reload only.
- A gateway whose model-list path is not `<base-url>/v1/models` (for example one rooted at `/v2`) has
  no per-provider path override yet; it is reported as an upstream error and skipped.

## License

MIT - see [LICENSE](LICENSE).
