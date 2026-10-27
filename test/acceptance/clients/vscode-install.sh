#!/bin/sh
set -eu

python3 - <<'PY'
import json
import os
from pathlib import Path

path = Path('/opt/vscode/resources/app/product.json')
product = json.loads(path.read_text())
product['extensionsGallery']['serviceUrl'] = os.environ['ARTIFACTWEAVE_GALLERY_URL']
path.write_text(json.dumps(product))
PY

if /opt/vscode/bin/code --no-sandbox \
  --user-data-dir "${HOME}/vscode-user-${1}" \
  --extensions-dir "${HOME}/vscode-extensions-${1}" \
  --install-extension ms-vscode.hexeditor --force; then
  exit 0
else
  install_exit=$?
  find "${HOME}/vscode-user-${1}/logs" -type f -name '*.log' \
    -exec grep -h -E 'Extension signature verification result|vsce-sign|Error while installing' {} + || true
  exit "$install_exit"
fi
