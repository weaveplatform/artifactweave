"""Check recipe snippet syntax without executing configuration or client commands."""

import ast
import json
from pathlib import Path
import re
import subprocess
import sys


def check() -> list[str]:
    errors = []
    for path in sorted(Path("docs/client-recipes/src").glob("*.json")):
        recipe = json.loads(path.read_text())
        groups = {
            "configure": recipe["configure"],
            "publish": recipe["publish"]["steps"],
            "install": recipe["install"]["steps"],
        }
        for action, snippets in groups.items():
            for number, snippet in enumerate(snippets, start=1):
                label = f"{path}: {action} snippet {number}"
                code = snippet["code"]
                python = []
                if snippet["language"] in ("sh", "bash"):
                    result = subprocess.run(
                        ["bash", "-n"], input=code, text=True,
                        capture_output=True, timeout=10, check=False,
                    )
                    if result.returncode:
                        errors.append(f"{label}: {result.stderr.strip()}")
                    # A shell syntax check treats heredoc contents as opaque.
                    # Parse literal Python heredocs too: an unescaped newline
                    # in a credential-file writer otherwise passes bash -n.
                    python.extend(
                        match.group(2)
                        for match in re.finditer(
                            r"python3?[^\n]*<<\s*'([A-Z_0-9]+)'\n(.*?)\n\1(?:\n|$)",
                            code, re.DOTALL,
                        )
                    )
                elif snippet["language"] == "python":
                    python.append(code)
                for source in python:
                    try:
                        ast.parse(source)
                    except SyntaxError as error:
                        errors.append(f"{label}: Python line {error.lineno}: {error.msg}")
    return errors


if __name__ == "__main__":
    failures = check()
    if failures:
        print("\n".join(failures), file=sys.stderr)
        sys.exit(1)
    print("Recipe shell and Python syntax checks passed (no commands executed).")
