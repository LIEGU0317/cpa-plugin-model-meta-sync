#!/usr/bin/env bash
# Disable model-meta-sync on the CPA production host.
#
# Default (surgical): remove only the plugins.configs.model-meta-sync block from the
# current config, delete the plugin binaries, restart, then verify. Later edits made
# through the panel (new client keys, other plugins) are preserved.
#
# Full restore: ./rollback_prod.sh --restore <pre-install-backup>
# Restores the whole file. Only accepts a backup that does NOT contain the plugin
# block, and warns that anything added after that backup is lost.
set -euo pipefail
CONFIG="${CPA_CONFIG:-/srv/cliproxyapi/config.yaml}"
PLUGIN_DIR="${CPA_PLUGIN_DIR:-$(dirname "$CONFIG")/plugins/linux/amd64}"
SERVICE="${CPA_SERVICE:-cliproxyapi.service}"
SKIP_RESTART="${CPA_SKIP_RESTART:-0}"
PROBE_URL="${CPA_PROBE_URL:-http://127.0.0.1:8317/v1/models}"

has_block() {
  python3 - "$1" <<'PY'
import sys
try:
    import yaml
except Exception:
    sys.exit(2)
try:
    data = yaml.safe_load(open(sys.argv[1], encoding="utf-8")) or {}
except Exception:
    sys.exit(2)
configs = ((data.get("plugins") or {}).get("configs") or {})
sys.exit(0 if "model-meta-sync" in configs else 1)
PY
}

strip_block() {
  python3 - "$1" <<'PY'
import sys

path = sys.argv[1]
lines = open(path, encoding="utf-8").read().splitlines(keepends=True)
key = "        model-meta-sync:\n"
start = next((i for i, line in enumerate(lines) if line == key), None)
if start is None:
    sys.exit(3)
end = len(lines)
for index in range(start + 1, len(lines)):
    stripped = lines[index].strip()
    if not stripped or stripped.startswith("#"):
        continue
    indent = len(lines[index]) - len(lines[index].lstrip(" "))
    if indent <= 8:
        end = index
        break
del lines[start:end]
open(path, "w", encoding="utf-8").write("".join(lines))
PY
}

mode="surgical"
backup=""
if [[ "${1:-}" == "--restore" ]]; then
  mode="restore"
  backup="${2:-}"
  [[ -n "$backup" && -f "$backup" ]] || { echo "usage: $0 --restore <pre-install-backup>" >&2; exit 1; }
  set +e
  has_block "$backup"
  status=$?
  set -e
  if [[ "$status" -eq 0 ]]; then
    echo "refusing: backup $backup already contains the model-meta-sync block" >&2
    exit 1
  fi
  if [[ "$status" -eq 2 ]]; then
    echo "refusing: cannot parse $backup as YAML" >&2
    exit 1
  fi
  if [[ "$status" -ne 0 && "$status" -ne 1 ]]; then
    echo "refusing: cannot evaluate plugin block state in $backup (has_block=$status)" >&2
    exit 1
  fi
fi

if [[ "$mode" == "surgical" ]]; then
  set +e
  strip_block "$CONFIG"
  status=$?
  set -e
  case "$status" in
    0) echo "removed plugins.configs.model-meta-sync from $CONFIG" ;;
    3)
      set +e
      has_block "$CONFIG"
      still=$?
      set -e
      if [[ "$still" -eq 0 ]]; then
        echo "model-meta-sync block present but not strippable (unexpected indentation); edit $CONFIG manually" >&2
        exit 1
      fi
      echo "plugin block not present in $CONFIG (already disabled)"
      ;;
    *) echo "failed to strip plugin block from $CONFIG" >&2; exit 1 ;;
  esac
else
  echo "restoring whole config from $backup (later edits will be lost)"
  cp -a "$CONFIG" "$CONFIG.bak-modelmetasync-rollback-$(date +%Y%m%d-%H%M%S)"
  cp -a "$backup" "$CONFIG"
fi

set +e
has_block "$CONFIG"
status=$?
set -e
if [[ "$status" -eq 0 ]]; then
  echo "config still references model-meta-sync; aborting before deleting binaries" >&2
  exit 1
fi
if [[ "$status" -eq 2 ]]; then
  echo "config is not valid YAML after rollback; aborting" >&2
  exit 1
fi
if [[ "$status" -ne 0 && "$status" -ne 1 ]]; then
  echo "cannot evaluate plugin block state after rollback (has_block=$status); aborting" >&2
  exit 1
fi

find "$PLUGIN_DIR" -maxdepth 1 -name 'model-meta-sync-v*.so' -delete 2>/dev/null || true
remaining=$(find "$PLUGIN_DIR" -maxdepth 1 -name 'model-meta-sync-v*.so' 2>/dev/null | wc -l | tr -d ' ')
echo "plugin binaries remaining: $remaining"
if [[ "$remaining" != "0" ]]; then
  echo "plugin binary still present in $PLUGIN_DIR" >&2
  exit 1
fi

if [[ "$SKIP_RESTART" == "1" ]]; then
  echo "CPA_SKIP_RESTART=1: skipping service restart"
  echo "rollback simulated"
  exit 0
fi

XDG_RUNTIME_DIR=/run/user/0 systemctl --user restart "$SERVICE"
for _ in $(seq 1 40); do
  code=$(curl -s -o /dev/null -m 1 -w '%{http_code}' "$PROBE_URL" || true)
  if [[ -n "$code" && "$code" != "000" ]]; then
    echo "gateway answering (HTTP $code)"
    echo "rolled back"
    exit 0
  fi
  sleep 0.5
done
echo "gateway did not come back; inspect journalctl --user -u $SERVICE" >&2
exit 1