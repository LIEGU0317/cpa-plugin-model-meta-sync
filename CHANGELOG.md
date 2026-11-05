# Changelog

## 0.3.2

Review fixes. Each change has a regression test that fails on 0.3.1.

- **Catalog-suffix folding is one-way.** 0.3.1 indexed suffix variants of every upstream id and
  also tried them on lookup, so a configured `gpt-5-preview` resolved to the plain upstream `gpt-5`
  and inherited its metadata. A configured bare name still matches a decorated catalog id
  (`glm-4-flash` -> `glm-4-flash-free`).
- **`name` wins over `alias`.** In CPA `name` is the upstream id the request goes to. 0.3.1 tried an
  exact `alias` before the name's relaxed spellings, so `name: deepseek-v4-flash, alias: deepseek-chat`
  received deepseek-chat's metadata. The alias is now used only when no name is configured.
- **Unknown field names are an error.** A misspelt entry in `fields` or `overwrite_fields` (for
  example `thinking.levels`) used to widen the list to every field - overwrite-everything for
  `overwrite_fields`. It now fails registration and names the offending entries.
- **Shutdown stops the background loop.** A Go `c-shared` library is never unloaded; with
  `interval_minutes > 0` a retired version kept rewriting `config.yaml`. Shutdown and every
  re-registration now stop the loop, cancel a sync in flight and wait for it.
- **Key overrides are scoped to one provider.** New `api_keys: {<provider>: <key>}`. The legacy
  `api_key` is accepted only with exactly one `providers` entry; an unscoped `api_key`, which 0.3.1
  sent to every gateway, is now rejected. `/status` and `/config` redact `api_keys`.
- CI runs the unit tests under the race detector.

**Upgrade note:** a config with an unscoped `api_key` or a misspelt field name stops loading; fix the
config before upgrading.

## 0.3.1

- **Plugin-store layout.** Releases now ship `<id>_<version>_<goos>_<goarch>.zip` with
  `model-meta-sync.<ext>` at the archive root plus `checksums.txt` - the only shape CPA's store
  installer accepts (a bare `.so` asset is refused). `make dist` writes the archive, `make checksums`
  covers every archive in `dist/`, `make verify-dist` re-checks the layout, and
  `.github/workflows/build.yml` builds linux/amd64, linux/arm64, darwin/amd64, darwin/arm64 and
  windows/amd64 from a `v*` tag and publishes the assets.
- **Metadata.** The plugin registers `Logo` (`assets/logo.png`), so the store card and the management
  plugin list show the icon. `ConfigFields` were refreshed: `models_paths` is advertised, every field
  carries the default `parseConfig` applies, and `upstream_models_path` is marked deprecated.
  `plugin/metadata_test.go` keeps the advertised surface identical to the parsed one.
- **`sync_on_start` really defaults to true.** An omitted key used to parse as `false` while the docs
  promised the CPAMC "apply models, get metadata in the same reload" behaviour. An explicit `false`
  still disables the startup run.
- **Version stamping.** `pluginVersion` is a variable filled from the release tag
  (`-ldflags "-X main.pluginVersion=..."`), and `make version-check` fails when the tag, the
  Makefile `VERSION` and the source default disagree.
- `store/` documents the store entry and the release contract; `scripts/render-icon.sh` regenerates
  the PNG icon set from `assets/icon.svg`; `plugin/README.md` no longer duplicates (and contradict)
  the root README.
- **Platform smoke tests.** `scripts/smoke-test.sh` loads each built archive in a real CLIProxyAPI
  binary of the same platform, asserts the registered metadata, syncs a mock upstream, verifies the
  config was enriched and hot-reloaded and that a second run writes nothing. CI runs it for
  linux/amd64, linux/arm64, windows/amd64, darwin/arm64 (all pass) and darwin/amd64 (informational:
  macOS Intel hosts fail to load any Go `c-shared` plugin, including the official
  `antigravity-priority`).

## 0.3.0

- Endpoint discovery now mirrors CPA's "fetch from endpoint": try `/v1/models`, then `/models`,
  falling back only on 404/405. Gateways rooted below `/v1` (讯飞星火 `/v2/models`) are supported.
- `models_paths` maps a provider name to a custom catalog path for anything unusual.
- Reports carry the path that worked (`models_path`), never the host.
- Applying models in the CPAMC editor triggers a config reload and therefore the plugin's startup
  sync, so metadata lands in the same operation (`sync_on_start: true`).
- Prebuilt `plugins/linux/amd64/model-meta-sync-v0.3.0.so` (with `.sha256`) ships in the repository;
  `make build` now writes to `plugins/<goos>/<goarch>/` instead of `build/`.

## 0.2.2

- Recognise `supports_reasoning` / `only_reasoning` as reasoning support (workbuddy2api and other
  relays do not send `reasoning`).
- Read the ladder from `reasoning_supported_efforts`, in addition to `reasoning_levels` and
  `supported_reasoning_levels[].effort`.
- Reasoning-only models that publish a single `reasoning_effort` get that one level.
- Filter and lower-case level names to CPA's schema (`none`, `minimal`, `low`, `medium`, `high`,
  `xhigh`, `max`); unknown names are never written.

## 0.2.1

- `config_path` is optional. The plugin resolves the host's config from `-config`/`--config` in the
  process arguments, otherwise `config.yaml` in the process working directory (CPA's own fallback).
  Explicit `config_path` still wins; nothing is guessed when no file is found.

## 0.2.0

- **Provider-agnostic scope**: sync every enabled `openai-compatibility` provider by default. The
  `providers` key is now an optional name filter instead of a mandatory allow-list; `include_disabled`
  opts disabled providers in.
- **Name-based matching** (inspired by `dsh-model-metadata`): exact id → case-insensitive → bare name →
  separator-free spelling → catalog suffixes (`-exp`, `-preview`, …) → deployment qualifiers
  (`-self`, `-official`). Ambiguous folded keys are refused instead of guessed.
- **Display names from `name`**: OpenAI-compatible relays such as workbuddy2api publish the human
  label as `name`; it is used when `display_name`/`display-name` are absent and differs from the id.
- **Per-provider isolation**: one unreachable gateway no longer aborts the whole run; its entries stay
  untouched and the failure is reported by name. Nothing is written only when every upstream fails.
- **Privacy**: provider reports, logs and error messages no longer carry `base-url`, hosts, IPs or
  ports; identity is the provider name. Anchor/alias guard now skips only the affected provider.
- **Safe rollout**: `dry_run_on_start` makes the startup sync report-only; `overwrite_fields` absent
  now means "never overwrite" (previously an empty list was normalised to all fields).
- Dropped the hard-coded `Magpie` default and the deploy-specific default config path.

## 0.1.4

- Startup-sync warnings when the anchor guard fires; hardened rollback exit codes.

## 0.1.3

- Provider-level alias pre-scan, surgical config block removal on rollback.

## 0.1.2

- Multi-document YAML refusal, `disabled: yes/on`, deeper diff validation, `/config` redaction.

## 0.1.1

- Concurrency serialization, YAML anchor/alias protection, provider-scoped diff validation.

## 0.1.0

- First production release: fill `display-name`, `max-context-length`, modalities and
  `thinking.levels` from an upstream `/v1/models`, with backups, semantic diff validation, sha256
  conflict detection and in-place write for CPA's hot reload.
