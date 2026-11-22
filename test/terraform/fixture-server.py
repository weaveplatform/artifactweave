"""Launch the owned provider fixture, or verify its structured listener event."""

import json
import os
from pathlib import Path
import sys


def listening(log, address):
    # internal/app/run.go emits this event only after net.Listen succeeds.
    # Parse the JSON record: a diagnostic mentioning the message or a stale
    # event for a different address must never authorize authentication.
    for line in Path(log).read_text().splitlines():
        try:
            event = json.loads(line)
        except ValueError:
            continue
        if isinstance(event, dict) and event.get("msg") == "artifactweave listening" and event.get("addr") == address:
            return True
    return False


def launch(work, port, coverage):
    work = Path(work).resolve()
    data = work / "data"
    config = {
        "data_dir": str(data),
        "database": {"driver": "sqlite", "dsn": str(data / "artifactweave.db")},
        "blob": {"backend": "fs", "fs": {"root": str(data / "blobs")}},
        "cold_blob": {"backend": ""},
        "master_key": "terraform-master-key-0123456789abcdef0123456789abcdef",
        "bootstrap": {"admin_password": "terraform-admin"},
        "token": {"allow_basic_auth": True},
        "api": {"budget_timeout": "5s"},
        "log": {"format": "json", "level": "info"},
    }
    path = work / "artifactweave.json"
    with path.open("x") as out:
        json.dump(config, out)
    # Environment wins over the config file. Isolate the child rather than
    # changing the caller's environment, HOME, or the provider/client options.
    env = {key: value for key, value in os.environ.items() if not key.startswith("ARTIFACTWEAVE_")}
    env["GOCOVERDIR"] = coverage
    address = "127.0.0.1:" + port
    args = [str(work / "artifactweave"), "serve", "--config", str(path),
            "--listen", address, "--base-url", "http://" + address]
    os.execve(args[0], args, env)


if __name__ == "__main__":
    if sys.argv[1] == "launch":
        launch(*sys.argv[2:])
    elif sys.argv[1] == "listening":
        sys.exit(0 if listening(*sys.argv[2:]) else 1)
    else:
        sys.exit("unknown fixture-server command")
