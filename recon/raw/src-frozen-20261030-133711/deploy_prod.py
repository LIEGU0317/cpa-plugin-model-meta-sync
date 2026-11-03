#!/usr/bin/env python3
"""Install and verify the model-meta-sync CPA plugin on the production host.

Runs on the CPA host as root. Idempotent: re-running keeps one plugin block and
one .so. Always writes a timestamped config backup before editing.
"""
import hashlib
import json
import os
import shutil
import subprocess
import sys
import time

import yaml

CPA_DIR = "/srv/cliproxyapi"
CONFIG = os.path.join(CPA_DIR, "config.yaml")
PLUGIN_ID = "model-meta-sync"
PLUGIN_VERSION = "0.1.1"
PLUGIN_DST = os.path.join(CPA_DIR, "plugins", "linux", "amd64", f"{PLUGIN_ID}-v{PLUGIN_VERSION}.so")
SERVICE = "cliproxyapi.service"
STAMP = time.strftime("%Y%m%d-%H%M%S")

PLUGIN_BLOCK = """        model-meta-sync:
            enabled: true
            config_path: /srv/cliproxyapi/config.yaml
            providers:
                - Magpie
            mode: missing
            fields:
                - display-name
                - max-context-length
                - input-modalities
                - output-modalities
                - thinking
            overwrite_fields:
                - display-name
                - max-context-length
                - input-modalities
                - output-modalities
            timeout_seconds: 20
            backup_keep: 10
            sync_on_start: true
            interval_minutes: 0
"""


def run(cmd, **kwargs):
    return subprocess.run(cmd, shell=isinstance(cmd, str), capture_output=True, text=True, **kwargs)


def main():
    so_source = sys.argv[1] if len(sys.argv) > 1 else "/tmp/model-meta-sync-v0.1.1.so"
    if not os.path.isfile(so_source):
        raise SystemExit(f"plugin binary not found: {so_source}")

    before = open(CONFIG, "rb").read()
    before_hash = hashlib.sha256(before).hexdigest()
    before_cfg = yaml.safe_load(before)
    backup = f"{CONFIG}.bak-modelmetasync-deploy-{STAMP}"
    shutil.copy2(CONFIG, backup)

    access_keys = (before_cfg.get("access") or {}).get("api-keys") or []
    client_key = access_keys[0] if access_keys else ""

    # 1. install plugin binary
    os.makedirs(os.path.dirname(PLUGIN_DST), exist_ok=True)
    shutil.copy2(so_source, PLUGIN_DST)
    os.chmod(PLUGIN_DST, 0o644)

    # 2. insert plugin config block once
    text = before.decode()
    cfg_after = yaml.safe_load(text)
    configs = ((cfg_after.get("plugins") or {}).get("configs") or {})
    if PLUGIN_ID not in configs:
        lines = text.splitlines(keepends=True)
        insert_at = None
        for index, line in enumerate(lines):
            if line.rstrip("\n") == "    configs:":
                insert_at = index + 1
                break
        if insert_at is None:
            raise SystemExit("plugins.configs not found in config.yaml")
        lines.insert(insert_at, PLUGIN_BLOCK)
        text = "".join(lines)
        parsed = yaml.safe_load(text)
        if PLUGIN_ID not in ((parsed.get("plugins") or {}).get("configs") or {}):
            raise SystemExit("plugin block did not parse")
        with open(CONFIG, "w", encoding="utf-8") as handle:
            handle.write(text)
        print(f"inserted plugins.configs.{PLUGIN_ID}")
    else:
        print(f"plugins.configs.{PLUGIN_ID} already present")

    # 3. reload service (user unit)
    env = dict(os.environ, XDG_RUNTIME_DIR="/run/user/0")
    result = run(["systemctl", "--user", "restart", SERVICE], env=env)
    print("restart rc", result.returncode, result.stderr.strip())
    if result.returncode != 0:
        raise SystemExit("restart failed")

    # 4. wait for gateway
    ok = False
    for _ in range(40):
        probe = run(["curl", "-s", "-o", "/dev/null", "-m", "1", "-H", f"Authorization: Bearer {client_key}", "http://127.0.0.1:8317/v1/models"])
        if probe.returncode == 0:
            ok = True
            break
        time.sleep(0.5)
    print("gateway up:", ok)

    # 5. wait for plugin auto-sync window
    time.sleep(12)

    after = open(CONFIG, "rb").read()
    after_hash = hashlib.sha256(after).hexdigest()
    after_cfg = yaml.safe_load(after)
    models_before = before_cfg["api-keys"]["openai-compatibility"]
    models_after = after_cfg["api-keys"]["openai-compatibility"]
    summary = {
        "config_before_sha256": before_hash,
        "config_after_sha256": after_hash,
        "config_changed": before_hash != after_hash,
        "backup": backup,
        "plugin_installed": PLUGIN_DST,
        "gateway_up": ok,
        "provider_count_before": len(models_before),
        "provider_count_after": len(models_after),
    }
    print(json.dumps(summary, indent=2))
    with open(f"/srv/cliproxyapi/model-meta-sync-deploy-{STAMP}.json", "w") as handle:
        json.dump(summary, handle, indent=2)


if __name__ == "__main__":
    main()
