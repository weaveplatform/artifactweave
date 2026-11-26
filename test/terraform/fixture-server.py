"""Launch the owned provider fixture, or verify its structured listener event."""

import json
import os
from pathlib import Path
import select
import signal
import socket
import socketserver
import subprocess
import sys
import threading
import time


def listener_address(log):
    # internal/app/run.go emits this event only after net.Listen succeeds.
    # Parse the JSON record: a diagnostic mentioning the message or a stale
    # event for a different address must never authorize authentication.
    for line in Path(log).read_text().splitlines():
        try:
            event = json.loads(line)
        except ValueError:
            continue
        if isinstance(event, dict) and event.get("msg") == "artifactweave listening":
            address = event.get("addr", "")
            host, separator, port = address.rpartition(":") if isinstance(address, str) else ("", "", "")
            if host == "127.0.0.1" and separator and port.isdecimal() and 0 < int(port) < 65536:
                return address
    return None


def owns_connection(pid, connection):
    """Prove the child's accepted socket is the peer of this exact connection.

    A LISTEN check is insufficient: Shutdown can close it while requests drain,
    leaving the PID alive and allowing another process to bind the same port.
    Once proved, this TCP connection cannot reconnect to a replacement process.
    """
    local = connection.getsockname()
    peer = connection.getpeername()
    if sys.platform == "linux":
        process = Path("/proc") / str(pid)
        owned = set()
        for fd in (process / "fd").iterdir():
            try:
                target = os.readlink(fd)
            except FileNotFoundError:
                continue
            if target.startswith("socket:[") and target.endswith("]"):
                owned.add(target[8:-1])

        def address(endpoint):
            return socket.inet_aton(endpoint[0])[::-1].hex().upper() + f":{endpoint[1]:04X}"

        for line in (process / "net/tcp").read_text().splitlines()[1:]:
            fields = line.split()
            if (fields[1], fields[2], fields[3]) == (address(peer), address(local), "01") and fields[9] in owned:
                return True
        return False
    if sys.platform == "darwin":
        result = subprocess.run(["/usr/sbin/lsof", "-nP", "-a", "-p", str(pid),
                                 "-iTCP", "-sTCP:ESTABLISHED", "-Fpn"],
                                capture_output=True, text=True, timeout=1)
        fields = result.stdout.splitlines()
        expected = f"n{peer[0]}:{peer[1]}->{local[0]}:{local[1]}"
        return result.returncode == 0 and f"p{pid}" in fields and expected in fields
    return False


def forward(pid, address, port, marker):
    pid = int(pid)
    host, backend_port = address.rsplit(":", 1)
    if host != "127.0.0.1" or sys.platform not in ("linux", "darwin"):
        sys.exit("fixture socket ownership is supported only on Linux and macOS loopback")
    stop = threading.Event()
    failed = threading.Event()

    class Handler(socketserver.BaseRequestHandler):
        def handle(self):
            if failed.is_set():
                return
            proven = False
            try:
                upstream = socket.create_connection((host, int(backend_port)), timeout=1)
                with upstream:
                    deadline = time.monotonic() + 1
                    while not owns_connection(pid, upstream):
                        if time.monotonic() >= deadline:
                            raise OSError("connected backend socket is not owned by fixture child")
                        time.sleep(.01)
                    proven = True
                    # Do not read or forward client bytes before ownership is
                    # proved. No reconnect or HTTP retry can change this peer.
                    upstream.settimeout(5)
                    self.request.settimeout(5)
                    while not stop.is_set() and not failed.is_set():
                        readable, _, _ = select.select([self.request, upstream], [], [], .2)
                        for source in readable:
                            data = source.recv(65536)
                            if not data:
                                return
                            target = upstream if source is self.request else self.request
                            target.sendall(data)
            except (OSError, subprocess.SubprocessError) as error:
                # Never include client bytes, headers or credentials in logs.
                phase = "transfer" if proven else "connect-or-proof"
                action = "closing verified connection" if proven else "quarantining owned frontend"
                print(f"fixture {phase} failure: {type(error).__name__}, errno={getattr(error, 'errno', None)}; {action}",
                      file=sys.stderr, flush=True)
                if proven:
                    # EOF/reset/broken pipe cannot change this fixed peer.
                    # Native clients routinely discard pooled connections;
                    # close only this one and prove any new connection anew.
                    return
                failed.set()
                Path(marker).with_suffix(".failed").write_text("fixture connection ownership or transport failed\n")
                # Keep the frontend bound until the parent has waited for
                # its active client command. Releasing it here would let a
                # concurrent provider retry reach a replacement frontend.

    class Server(socketserver.ThreadingTCPServer):
        allow_reuse_address = True
        # server_close joins handlers (including their bounded lsof probes)
        # so cleanup leaves no owned relay/probe process behind.
        daemon_threads = False

    signal.signal(signal.SIGTERM, lambda *_: stop.set())
    signal.signal(signal.SIGINT, lambda *_: stop.set())
    with Server(("127.0.0.1", int(port)), Handler) as server:
        server.timeout = .1
        with Path(marker).open("x") as out:
            json.dump({"pid": os.getpid()}, out)
        while not stop.is_set():
            server.handle_request()
    if failed.is_set():
        sys.exit(1)


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
    if os.environ.get("TERRAFORM_FIXTURE_ORIGINS"):
        config["remote"] = {"allowed_private_origins": json.loads(os.environ["TERRAFORM_FIXTURE_ORIGINS"])}
    path = work / "artifactweave.json"
    with path.open("x") as out:
        json.dump(config, out)
    # Environment wins over the config file. Isolate the child rather than
    # changing the caller's environment, HOME, or the provider/client options.
    env = {key: value for key, value in os.environ.items() if not key.startswith("ARTIFACTWEAVE_")}
    env["GOCOVERDIR"] = coverage
    address = "127.0.0.1:" + port
    args = [str(work / "artifactweave"), "serve", "--config", str(path),
            "--listen", "127.0.0.1:0", "--base-url", "http://" + address]
    os.execve(args[0], args, env)


if __name__ == "__main__":
    if sys.argv[1] == "launch":
        launch(*sys.argv[2:])
    elif sys.argv[1] == "address":
        address = listener_address(*sys.argv[2:])
        if not address:
            sys.exit(1)
        print(address)
    elif sys.argv[1] == "forward":
        forward(*sys.argv[2:])
    else:
        sys.exit("unknown fixture-server command")
