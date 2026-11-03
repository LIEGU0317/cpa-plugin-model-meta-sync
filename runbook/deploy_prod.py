#!/usr/bin/env python3
"""Install and verify the model-meta-sync CPA plugin on the production host.

Runs on the CPA host as root. Idempotent: keeps exactly one plugin block (updates
it if present) and one .so. Always writes a timestamped config backup first.
"""
import hashlib
import json
import os
import shutil
import subprocess
import sys
import time

import yaml

CPA_DIR = os.environ.get("CPA_DIR", "/srv/cliproxyapi")
CONFIG = os.path.join(CPA_DIR, "config.yaml")
PLUGIN_ID = "model-meta-sync"
PLUGIN_VERSION = "0.3.0"
PLUGIN_DST = os.path.join(CPA_DIR, "plugins", "linux", "amd64", f"{PLUGIN_ID}-v{PLUGIN_VERSION}.so")
SERVICE = "cliproxyapi.service"
STAMP = time.strftime("%Y%m%d-%H%M%S")
BLOCK_INDENT = 8

PLUGIN_BLOCK = """        model-meta-sync:
            enabled: true
            mode: missing
            fields:
                - display-name
                - max-context-length
                - input-modalities
                - output-modalities
                - thinking
            timeout_seconds: 20
            backup_keep: 10
            sync_on_start: true
            dry_run_on_start: __DRY_RUN__
            interval_minutes: 0
"""


def run(cmd, **kwargs):
    return subprocess.run(cmd, shell=isinstance(cmd, str), capture_output=True, text=True, **kwargs)


def indent_of(line):
    return len(line) - len(line.lstrip(" "))


DRY_RUN_ON_START = os.environ.get("MMS_DRY_RUN_ON_START", "false").strip().lower() in ("1", "true", "yes", "on")


def upsert_block(text):
    """Install the plugin block under plugins.configs.

    Works on the parsed document instead of raw lines: the CPA panel, the
    plugin's own writer and this script all reshape the file, so indentation is
    not stable enough to match text blocks reliably.
    """
    doc = yaml.safe_load(text)
    if not isinstance(doc, dict):
        raise SystemExit("config.yaml root is not a mapping")
    plugins = doc.get("plugins")
    if not isinstance(plugins, dict):
        raise SystemExit("plugins not found in config.yaml")
    configs = plugins.get("configs")
    if not isinstance(configs, dict):
        raise SystemExit("plugins.configs not found in config.yaml")

    block = yaml.safe_load(
        PLUGIN_BLOCK.replace("__DRY_RUN__", "true" if DRY_RUN_ON_START else "false")
    )[PLUGIN_ID]

    # Repair a block that a previous text edit nested inside another plugin.
    for name, value in list(configs.items()):
        if name != PLUGIN_ID and isinstance(value, dict) and PLUGIN_ID in value:
            value.pop(PLUGIN_ID, None)

    action = "updated" if PLUGIN_ID in configs else "inserted"
    configs[PLUGIN_ID] = block
    return yaml.safe_dump(doc, allow_unicode=True, sort_keys=False), action


def client_key(cfg):
    access = cfg.get("access") or {}
    keys = access.get("api-keys") or []
    if keys and isinstance(keys[0], str):
        return keys[0]
    legacy = (cfg.get("api-keys") or {}).get("access")
    if isinstance(legacy, list) and legacy:
        return str(legacy[0])
    if isinstance(legacy, dict):
        values = legacy.get("api-keys") or []
        if values:
            return str(values[0])
    return ""


def main():
    so_source = sys.argv[1] if len(sys.argv) > 1 else f"/tmp/{PLUGIN_ID}-v{PLUGIN_VERSION}.so"
    if not os.path.isfile(so_source):
        raise SystemExit(f"plugin binary not found: {so_source}")

    before = open(CONFIG, "rb").read()
    before_hash = hashlib.sha256(before).hexdigest()
    before_cfg = yaml.safe_load(before)
    backup = f"{CONFIG}.bak-modelmetasync-deploy-{STAMP}"
    shutil.copy2(CONFIG, backup)
    key = client_key(before_cfg)

    os.makedirs(os.path.dirname(PLUGIN_DST), exist_ok=True)
    shutil.copy2(so_source, PLUGIN_DST)
    os.chmod(PLUGIN_DST, 0o644)

    updated_text, action = upsert_block(before.decode())
    parsed = yaml.safe_load(updated_text)
    block = (((parsed.get("plugins") or {}).get("configs") or {}).get(PLUGIN_ID))
    if block is None or block.get("enabled") is not True:
        raise SystemExit("plugin block did not parse")
    if updated_text != before.decode():
        with open(CONFIG, "w", encoding="utf-8") as handle:
            handle.write(updated_text)
    print(f"plugins.configs.{PLUGIN_ID} {action}")

    env = dict(os.environ, XDG_RUNTIME_DIR="/run/user/0")
    result = run(["systemctl", "--user", "restart", SERVICE], env=env)
    print("restart rc", result.returncode, result.stderr.strip())
    if result.returncode != 0:
        raise SystemExit("restart failed")

    ok = False
    for _ in range(40):
        headers = ["-H", f"Authorization: Bearer {key}"] if key else []
        probe = run(["curl", "-s", "-o", "/dev/null", "-m", "1", "-w", "%{http_code}", *headers, "http://127.0.0.1:8317/v1/models"])
        code = (probe.stdout or "").strip()
        if code not in ("", "000"):
            ok = True
            print(f"gateway answering HTTP {code}")
            break
        time.sleep(0.5)

    time.sleep(12)

    after = open(CONFIG, "rb").read()
    after_hash = hashlib.sha256(after).hexdigest()
    after_cfg = yaml.safe_load(after)
    summary = {
        "config_before_sha256": before_hash,
        "config_after_sha256": after_hash,
        "config_changed": before_hash != after_hash,
        "backup": backup,
        "plugin_installed": PLUGIN_DST,
        "gateway_up": ok,
        "provider_count_before": len(before_cfg["api-keys"]["openai-compatibility"]),
        "provider_count_after": len(after_cfg["api-keys"]["openai-compatibility"]),
    }
    print(json.dumps(summary, indent=2))
    with open(os.path.join(CPA_DIR, f"model-meta-sync-deploy-{STAMP}.json"), "w") as handle:
        json.dump(summary, handle, indent=2)


if __name__ == "__main__":
    main()