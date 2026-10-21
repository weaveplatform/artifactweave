"""A dependency-free PEP 517 backend for the acceptance sdist."""

import base64
import email
import hashlib
from pathlib import Path
import re
import zipfile


def build_wheel(wheel_directory, config_settings=None, metadata_directory=None):
    metadata = Path("PKG-INFO").read_bytes()
    fields = email.message_from_bytes(metadata)
    name = re.sub(r"[-_.]+", "_", fields["Name"]).lower()
    version = fields["Version"]
    info = f"{name}-{version}.dist-info"
    files = {
        f"{name}/__init__.py": Path(name, "__init__.py").read_bytes(),
        f"{info}/METADATA": metadata,
        f"{info}/WHEEL": b"Wheel-Version: 1.0\nRoot-Is-Purelib: true\nTag: py3-none-any\n",
    }
    record = ""
    for filename, data in files.items():
        digest = base64.urlsafe_b64encode(hashlib.sha256(data).digest()).rstrip(b"=").decode()
        record += f"{filename},sha256={digest},{len(data)}\n"
    files[f"{info}/RECORD"] = (record + f"{info}/RECORD,,\n").encode()
    wheel = f"{name}-{version}-py3-none-any.whl"
    with zipfile.ZipFile(Path(wheel_directory, wheel), "w") as archive:
        for filename, data in files.items():
            archive.writestr(filename, data)
    return wheel
