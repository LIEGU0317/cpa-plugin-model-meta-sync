#!/usr/bin/env bash
# Load one built plugin archive into a real CLIProxyAPI binary and prove it works.
#
# This is the platform proof the release packaging alone cannot give: the host
# actually dlopen()s the library, registers the plugin metadata (logo + config
# fields), fetches a mock upstream catalog, writes the metadata into config.yaml
# and hot-reloads it.
#
# Required environment:
#   GOOS, GOARCH   platform of the archive and of the CPA binary
#   EXT            so | dylib | dll
#   CPA_BIN        path to the CLIProxyAPI executable for that platform
#   ARTIFACTS_DIR  directory holding model-meta-sync_<version>_<goos>_<goarch>.zip
#   VERSION        expected plugin version
# Optional:
#   PORT           CPA port (default 28399), UPSTREAM_PORT (default 19091)
set -euo pipefail

: "${GOOS:?GOOS is required}"
: "${GOARCH:?GOARCH is required}"
: "${EXT:?EXT is required}"
: "${CPA_BIN:?CPA_BIN is required}"
: "${ARTIFACTS_DIR:?ARTIFACTS_DIR is required}"
: "${VERSION:?VERSION is required}"

PORT="${PORT:-28399}"
UPSTREAM_PORT="${UPSTREAM_PORT:-19091}"
MANAGEMENT_KEY="smoke-key-$$"
work="${WORK_DIR:-$(pwd)/smoke-${GOOS}-${GOARCH}}"
repo_root="$(cd "$(dirname "${BASH_SOURCE[0]}")/.." && pwd)"

rm -rf "${work}"
mkdir -p "${work}/plugins/${GOOS}/${GOARCH}"

archive="$(ls "${ARTIFACTS_DIR}"/model-meta-sync_"${VERSION}"_"${GOOS}"_"${GOARCH}".zip 2>/dev/null | head -1 || true)"
if [[ -z "${archive}" ]]; then
    echo "no archive for ${GOOS}/${GOARCH} in ${ARTIFACTS_DIR}" >&2
    exit 1
fi
unzip -o -q "${archive}" -d "${work}/plugins/${GOOS}/${GOARCH}"
if [[ ! -f "${work}/plugins/${GOOS}/${GOARCH}/model-meta-sync.${EXT}" ]]; then
    echo "${archive} does not contain model-meta-sync.${EXT}" >&2
    exit 1
fi
mv "${work}/plugins/${GOOS}/${GOARCH}/model-meta-sync.${EXT}" \
   "${work}/plugins/${GOOS}/${GOARCH}/model-meta-sync-v${VERSION}.${EXT}"

cat > "${work}/config.yaml" <<YAML
port: ${PORT}
auth-dir: ${work}/auths
remote-management:
  allow-remote: false
  secret-key: ${MANAGEMENT_KEY}
plugins:
  enabled: true
  dir: plugins
  configs:
    model-meta-sync:
      enabled: true
      mode: missing
      timeout_seconds: 10
      sync_on_start: false
openai-compatibility:
  - name: Smoke Gateway
    base-url: http://127.0.0.1:${UPSTREAM_PORT}/v1
    api-keys:
      - smoke-upstream-key
    models:
      - name: smoke-model
YAML

python3 "${repo_root}/scripts/smoke-upstream.py" "${UPSTREAM_PORT}" &
upstream_pid=$!
cleanup() {
    kill "${cpa_pid:-0}" 2>/dev/null || true
    kill "${upstream_pid}" 2>/dev/null || true
}
trap cleanup EXIT

cd "${work}"
"${CPA_BIN}" -config "${work}/config.yaml" > "${work}/cpa.log" 2>&1 &
cpa_pid=$!

base="http://127.0.0.1:${PORT}"
ready=""
for _ in $(seq 1 60); do
    if curl -fsS -H "X-Management-Key: ${MANAGEMENT_KEY}" "${base}/v0/management/plugins" -o "${work}/plugins.json"; then
        ready="yes"
        break
    fi
    sleep 1
done
if [[ -z "${ready}" ]]; then
    echo "CPA did not answer on ${base}" >&2
    echo "--- cpa.log (head) ---" >&2
    head -60 "${work}/cpa.log" >&2 || true
    echo "--- fatal lines ---" >&2
    grep -m 10 -E "fatal error|SIG[A-Z]+|panic:|dyld|code signature|not valid" "${work}/cpa.log" >&2 || true
    echo "--- cpa.log (tail) ---" >&2
    tail -20 "${work}/cpa.log" >&2 || true
    exit 1
fi

python3 - "${work}/plugins.json" "${VERSION}" <<'PY'
import json
import sys

payload = json.load(open(sys.argv[1]))
want = sys.argv[2]
plugins = [p for p in payload["plugins"] if p["id"] == "model-meta-sync"]
if not plugins:
    sys.exit("plugin not discovered by the host")
plugin = plugins[0]
if not plugin.get("registered"):
    sys.exit(f"plugin not registered: {plugin}")
metadata = plugin.get("metadata") or {}
if metadata.get("version") != want:
    sys.exit(f"registered version {metadata.get('version')!r}, want {want!r}")
if not str(plugin.get("logo", "")).startswith("https://"):
    sys.exit(f"plugin has no logo: {plugin.get('logo')!r}")
fields = plugin.get("config_fields") or []
if len(fields) != 14:
    sys.exit(f"expected 14 config fields, got {len(fields)}")
print(f"registration ok: version={metadata['version']} logo={plugin['logo']} config_fields={len(fields)}")
PY

# Exercise the plugin for real: fetch the mock catalog, patch config.yaml, reload.
curl -fsS -X POST -H "X-Management-Key: ${MANAGEMENT_KEY}" "${base}/v0/management/model-meta-sync/sync" -o "${work}/sync.json"
python3 - "${work}/sync.json" <<'PY'
import json
import sys

report = json.load(open(sys.argv[1]))
if not report.get("ok") or not report.get("written"):
    sys.exit(f"sync did not write: {json.dumps(report)[:400]}")
provider = report["providers"][0]
if provider.get("matched") != 1 or provider.get("changed_fields", 0) == 0:
    sys.exit(f"unexpected provider report: {json.dumps(provider)[:400]}")
print(f"sync ok: matched={provider['matched']} changed_fields={provider['changed_fields']}")
PY

sleep 2
grep -q "config successfully reloaded" "${work}/cpa.log" || {
    echo "host did not hot-reload the config after the sync" >&2
    tail -20 "${work}/cpa.log" >&2
    exit 1
}
grep -q "max-context-length: 65536" "${work}/config.yaml" || {
    echo "config.yaml was not enriched with the upstream metadata" >&2
    sed -n '/openai-compatibility/,$p' "${work}/config.yaml" >&2
    exit 1
}

# A second run must be a no-op.
curl -fsS -X POST -H "X-Management-Key: ${MANAGEMENT_KEY}" "${base}/v0/management/model-meta-sync/sync" -o "${work}/sync2.json"
python3 - "${work}/sync2.json" <<'PY'
import json
import sys

report = json.load(open(sys.argv[1]))
if report.get("written"):
    sys.exit("second sync rewrote the config; the plugin is not idempotent")
print("idempotency ok: written=false")
PY

echo "smoke ok: ${GOOS}/${GOARCH} plugin loads, registers, syncs and hot-reloads"
