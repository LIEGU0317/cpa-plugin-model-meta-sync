#!/usr/bin/env bash
# Roll back model-meta-sync on the CPA production host.
# Usage: ./rollback_prod.sh [config-backup-path]
set -euo pipefail
CONFIG=/srv/cliproxyapi/config.yaml
SO=/srv/cliproxyapi/plugins/linux/amd64/model-meta-sync-v0.1.0.so

backup="${1:-}"
if [[ -z "$backup" ]]; then
  backup=$(ls -1t "$CONFIG".bak-modelmetasync-deploy-* 2>/dev/null | head -1 || true)
fi
if [[ -z "$backup" || ! -f "$backup" ]]; then
  echo "no deploy backup found; pass one explicitly" >&2
  exit 1
fi

echo "restoring $backup -> $CONFIG"
cp -a "$CONFIG" "$CONFIG.bak-modelmetasync-rollback-$(date +%Y%m%d-%H%M%S)"
cp -a "$backup" "$CONFIG"
rm -f "$SO"
XDG_RUNTIME_DIR=/run/user/0 systemctl --user restart cliproxyapi.service
for _ in $(seq 1 40); do
  if curl -s -o /dev/null -m 1 http://127.0.0.1:8317/v1/models; then
    echo "rolled back and gateway is up"
    exit 0
  fi
  sleep 0.5
done
echo "gateway did not come back; inspect journalctl --user -u cliproxyapi" >&2
exit 1
