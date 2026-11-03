# Changelog

## 0.3.0

- Endpoint discovery now mirrors CPA's "fetch from endpoint": try `/v1/models`, then `/models`,
  falling back only on 404/405. Gateways rooted below `/v1` (讯飞星火 `/v2/models`) are supported.
- `models_paths` maps a provider name to a custom catalog path for anything unusual.
- Reports carry the path that worked (`models_path`), never the host.
- Applying models in the CPAMC editor triggers a config reload and therefore the plugin's startup
  sync, so metadata lands in the same operation (`sync_on_start: true`).

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
