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

exec /opt/vscode/bin/code --no-sandbox \
  --user-data-dir "${HOME}/vscode-user-${1}" \
  --extensions-dir "${HOME}/vscode-extensions-${1}" \
  --install-extension ms-vscode.hexeditor --force
