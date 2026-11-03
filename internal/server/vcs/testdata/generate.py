#!/usr/bin/env python3
"""Generate unchanged git-archive fixtures; requires Git 2.55.0 for byte parity."""

import argparse
import hashlib
import json
import os
from pathlib import Path
import subprocess
import tempfile

DATE = "2026-01-01T00:00:00Z"


def main():
    parser = argparse.ArgumentParser(description=__doc__)
    parser.add_argument("--output", type=Path, required=True)
    args = parser.parse_args()
    args.output.mkdir(parents=True, exist_ok=True)
    env = dict(os.environ, GIT_CONFIG_NOSYSTEM="1", GIT_CONFIG_GLOBAL=os.devnull,
               GIT_AUTHOR_NAME="VCS fixture", GIT_AUTHOR_EMAIL="fixture@example.invalid",
               GIT_COMMITTER_NAME="VCS fixture", GIT_COMMITTER_EMAIL="fixture@example.invalid",
               GIT_AUTHOR_DATE=DATE, GIT_COMMITTER_DATE=DATE)

    def git(root, *command):
        return subprocess.check_output(["git", "-c", "core.autocrlf=false", *command],
                                       cwd=root, env=env, stderr=subprocess.DEVNULL)

    manifest = {"generator": git(None, "--version").decode().strip(),
                "fixture_commit_date": DATE, "fixtures": {}}
    for marker in ("legacy-vcs", "refreshed-vcs"):
        with tempfile.TemporaryDirectory(prefix="vcs-git-archive-") as directory:
            root = Path(directory)
            git(root, "init", "--initial-branch=main")
            (root / "index.js").write_text(f'module.exports = "{marker}";\n')
            (root / "README.md").write_text("root readme\n")
            (root / "sub").mkdir()
            (root / "sub/README.md").write_text("nested readme\n")
            (root / "sub/ONLY.md").write_text("nested only\n")
            git(root, "add", ".")
            git(root, "commit", "-m", marker)
            record = {"commit": git(root, "rev-parse", "HEAD").decode().strip(), "files": {}}
            for suffix, fmt, paths in (
                    ("archive.tar.gz", "tar.gz", []), ("archive.zip", "zip", []),
                    ("index.zip", "zip", ["index.js"]),
                    ("readme.zip", "zip", ["README.md"]),
                    ("nested-readme.zip", "zip", ["sub/README.md"])):
                data = git(root, "archive", "--format=" + fmt, "HEAD", *paths)
                name = marker + "-" + suffix
                (args.output / name).write_bytes(data)
                record["files"][name] = hashlib.sha256(data).hexdigest()
            manifest["fixtures"][marker] = record
    (args.output / "git-archives.json").write_text(json.dumps(manifest, indent=2) + "\n")


if __name__ == "__main__":
    main()
