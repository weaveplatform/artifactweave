"""Compare the installed module to the bytes in the served distribution."""

import hashlib
from pathlib import Path
import subprocess
import sys
import tarfile
import zipfile

venv, distribution, module, version = sys.argv[1:]
module_path = module + "/__init__.py"
if distribution.endswith(".whl"):
    with zipfile.ZipFile(distribution) as archive:
        expected = archive.read(module_path)
else:
    with tarfile.open(distribution) as archive:
        entries = [entry for entry in archive.getmembers() if entry.name.endswith("/" + module_path)]
        assert len(entries) == 1, entries
        expected = archive.extractfile(entries[0]).read()

# Use the venv's interpreter to prove that its import and metadata resolve to
# the expected installation, rather than finding a file in an unused folder.
script = """
import hashlib
import importlib
import importlib.metadata
from pathlib import Path
import sys
module = importlib.import_module(sys.argv[1])
assert importlib.metadata.version(sys.argv[1]) == sys.argv[2]
assert hashlib.sha256(Path(module.__file__).read_bytes()).hexdigest() == sys.argv[3]
"""
subprocess.run(
    [str(Path(venv, "bin/python")), "-c", script, module, version, hashlib.sha256(expected).hexdigest()],
    check=True,
)
