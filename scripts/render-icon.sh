#!/usr/bin/env bash
# Regenerate the PNG icon set from assets/icon.svg.
#
# The SVG is the master: every PNG the store, the management UI and the README
# use is rendered from it, so the icon never drifts between sizes. Rendering
# needs a headless Chromium (no other image tooling is required); the PNG
# downscales are produced with Pillow when it is available, otherwise the
# 512px render is left as the only output.
set -euo pipefail

root="$(cd "$(dirname "${BASH_SOURCE[0]}")/.." && pwd)"
svg="${root}/assets/icon.svg"
out="${root}/assets/logo.png"

browser="${CHROMIUM:-}"
if [[ -z "${browser}" ]]; then
    for candidate in chromium chromium-browser google-chrome google-chrome-stable; do
        if command -v "${candidate}" >/dev/null 2>&1; then
            browser="${candidate}"
            break
        fi
    done
fi
if [[ -z "${browser}" ]]; then
    echo "no chromium found; set CHROMIUM=/path/to/chromium" >&2
    exit 1
fi

"${browser}" \
    --headless \
    --no-sandbox \
    --disable-gpu \
    --hide-scrollbars \
    --force-device-scale-factor=1 \
    --default-background-color=00000000 \
    --window-size=512,512 \
    --screenshot="${out}" \
    "file://${svg}" 2>/dev/null

echo "wrote ${out}"

if command -v python3 >/dev/null 2>&1 && python3 -c 'import PIL' >/dev/null 2>&1; then
    python3 - "${out}" <<'PY'
import sys
from PIL import Image

source = Image.open(sys.argv[1]).convert("RGBA")
for size in (128, 32):
    target = sys.argv[1].replace("logo.png", f"logo-{size}.png")
    source.resize((size, size), Image.LANCZOS).save(target)
    print(f"wrote {target}")
PY
else
    echo "pillow not available; skipped logo-128.png and logo-32.png" >&2
fi
