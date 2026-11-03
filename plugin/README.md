# model-meta-sync - CPA plugin

Fills CPA's per-provider model metadata (`api-keys.openai-compatibility[].models[]`) from each
gateway's own OpenAI-compatible `GET /v1/models`: `display-name`, `max-context-length`,
`input-modalities`, `output-modalities`, `thinking.levels`.

- Host: CPA v8 (ABI 1, schema 8). Go plugin, CGO `c-shared`.
- Scope: every enabled `openai-compatibility` provider; `providers:` is an optional name filter.
- Matching: providers by name, models by name (exact, case, bare name, separator-free spelling,
  catalog suffixes). No address/host/IP takes part in any decision.
- Writes `config.yaml` in place and relies on CPA's fsnotify hot reload. No management key needed.

## Build

```bash
CGO_ENABLED=1 GOOS=linux GOARCH=amd64 \
  go build -trimpath -buildmode=c-shared -o model-meta-sync-v0.2.0.so .
```

Requires Go >= 1.26 and a C toolchain for the target arch.

## Install

1. Copy the `.so` to `<cpa>/plugins/<goos>/<goarch>/model-meta-sync-v0.2.0.so`.
2. Add the config block and restart CPA once (the plugin host scans `.so` files at startup):

```yaml
plugins:
    configs:
        model-meta-sync:
            enabled: true
            config_path: /srv/cliproxyapi/config.yaml   # config that owns the provider entries
            mode: missing            # missing | always
            fields: [display-name, max-context-length, input-modalities, output-modalities, thinking]
            # providers: [Gateway A] # optional name filter; omit = every enabled provider
            # include_disabled: false
            # overwrite_fields: []   # absent = never overwrite existing values
            timeout_seconds: 20
            backup_keep: 10
            sync_on_start: true
            dry_run_on_start: false  # true = startup run only reports
            interval_minutes: 0      # >0 repeats the sync on a timer
            # api_key: sk-...        # optional; defaults to the provider's first configured key
```

## Management API

All routes require normal management auth (`X-Management-Key` or `Authorization: Bearer ...`).

| Method | Path | Purpose |
|---|---|---|
| GET | `/v0/management/model-meta-sync/status` | last report + effective config (api_key redacted) |
| GET | `/v0/management/model-meta-sync/config` | effective config + raw plugin config yaml |
| GET | `/v0/management/model-meta-sync/preview` | dry run, returns the patch, writes nothing |
| POST | `/v0/management/model-meta-sync/sync` | fetch upstream `/v1/models` and write; `?dry_run=1`, `?provider=Name` |

Response highlights: `changed_fields`, `written`, `backup`, `providers[].upstream_error`,
`providers[].changes[].fields[]` with `before`/`after`. Provider reports carry the provider name and
counts only - never `base_url`. Upstream errors name the provider and mask the address.

## Behavior contract

- `mode: missing` fills empty fields only; `overwrite_fields` opts specific fields into authoritative
  overwrite; `mode: always` recomputes every listed field.
- Per-provider isolation: one unreachable gateway is reported and skipped, the rest are still written.
  Nothing is written only when every selected upstream failed.
- Never adds/removes models, never edits `name`, `alias`, `base-url`, keys or other providers.
- Upstream values that are absent are skipped (no guessing `["text"]`).
- `thinking.levels` come from `reasoning_levels` / `supported_reasoning_levels[].effort`; models with
  `reasoning:false` are left alone. A single `reasoning_effort` is not expanded into a ladder.
- Safety: timestamped backup, semantic diff validation (provider-scoped allow list), sha256 conflict
  check before writing, staged copy for recovery, in-place write so CPA's inode watcher triggers the
  hot reload.
- YAML anchors/aliases around one provider's `models` list skip that provider instead of rewriting it.

## Limitations

- CPA has no field for upstream output-token limits (`max_output_tokens`), `native_endpoints`,
  `pricing` or `service_tiers`; those cannot be synced.
- Gateways whose model list is not at `<base-url>/v1/models` (e.g. rooted at `/v2`) need a
  per-provider path override, which does not exist yet; they are reported and skipped.
- Aliases are CPA's global model key; colliding aliases keep hiding entries from `/v1/models`.
- Overwriting `thinking` can silently downgrade clients that request an unsupported level, because CPA
  clamps to the nearest level (ties choose the lower one). Keep `thinking` out of `overwrite_fields`
  unless you accept that.
