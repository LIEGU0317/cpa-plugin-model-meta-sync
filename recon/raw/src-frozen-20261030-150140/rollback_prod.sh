#!/usr/bin/env bash
# Roll back model-meta-sync on the CPA production host.
# Usage: ./rollback_prod.sh [config-backup-path]
#
# Default: newest deploy backup that does NOT contain the plugin block (i.e. the
# pre-install config). Backups taken while the plugin was already installed are
# skipped on purpose: restoring one would leave the plugin enabled.
set -euo pipefail
CONFIG="${CPA_CONFIG:-/srv/cliproxyapi/config.yaml}"
PLUGIN_DIR="${CPA_PLUGIN_DIR:-$(dirname "$CONFIG")/plugins/linux/amd64}"
PLUGIN_GLOB="$PLUGIN_DIR/model-meta-sync-v"
SERVICE="${CPA_SERVICE:-cliproxyapi.service}"
SKIP_RESTART="${CPA_SKIP_RESTART:-0}"

pick_backup() {
  local explicit="${1:-}"
  if [[ -n "$explicit" ]]; then
    printf '%s\n' "$explicit"
    return 0
  fi
  local candidate
  while IFS= read -r candidate; do
    [[ -n "$candidate" ]] || continue
    if ! grep -q 'model-meta-sync' "$candidate"; then
      printf '%s\n' "$candidate"
      return 0
    fi
  done < <(ls -1t "$CONFIG".bak-modelmetasync-deploy-* 2>/dev/null || true)
  return 1
}

backup=$(pick_backup "${1:-}") || {
  echo "no pre-install backup found (every deploy backup contains the plugin block);" >&2
  echo "pass the pre-install backup explicitly, e.g. $CONFIG.bak-modelmetasync-deploy-20261030-132722" >&2
  exit 1
}
[[ -f "$backup" ]] || { echo "backup not found: $backup" >&2; exit 1; }

echo "restoring $backup -> $CONFIG"
cp -a "$CONFIG" "$CONFIG.bak-modelmetasync-rollback-$(date +%Y%m%d-%H%M%S)"
cp -a "$backup" "$CONFIG"
rm -f "${PLUGIN_GLOB}"*.so

if grep -q 'model-meta-sync' "$CONFIG"; then
  echo "config still references model-meta-sync after restore; aborting before restart" >&2
  exit 1
fi

if [[ "$SKIP_RESTART" == "1" ]]; then
  echo "CPA_SKIP_RESTART=1: skipping service restart"
else
  XDG_RUNTIME_DIR=/run/user/0 systemctl --user restart "$SERVICE"
fi
if [[ "$SKIP_RESTART" == "1" ]]; then
  remaining=$(ls -1 "${PLUGIN_GLOB}"*.so 2>/dev/null | wc -l)
  echo "rollback simulated; plugin binaries remaining: $remaining"
  [[ "$remaining" -eq 0 ]] && exit 0
  exit 1
fi

for _ in $(seq 1 40); do
  code=$(curl -s -o /dev/null -m 1 -w '%{http_code}' "${CPA_PROBE_URL:-http://127.0.0.1:8317/v1/models}" || true)
  if [[ "$code" != "000" && -n "$code" ]]; then
    echo "gateway answering (HTTP $code)"
    remaining=$(ls -1 "${PLUGIN_GLOB}"*.so 2>/dev/null | wc -l)
    echo "plugin binaries remaining: $remaining"
    if [[ "$remaining" -ne 0 ]]; then
      echo "plugin file still present; remove it manually" >&2
      exit 1
    fi
    echo "rolled back"
    exit 0
  fi
  sleep 0.5
done
echo "gateway did not come back; inspect journalctl --user -u $SERVICE" >&2
exit 1