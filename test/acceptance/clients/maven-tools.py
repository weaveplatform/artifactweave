#!/usr/bin/env python3
"""Prepare checksum-pinned Maven tools, never artifacts used by acceptance.

The object cache is disposable: every hit is checked and every scenario receives
only the manifest's bytes. Maven never writes to this shared directory.
"""

import argparse
from concurrent.futures import ThreadPoolExecutor
import hashlib
import json
import os
import re
from pathlib import Path, PurePosixPath
import shutil
import signal
import subprocess
import tempfile
import time
import xml.etree.ElementTree as ET

HERE = Path(__file__).resolve().parent
ROOT = HERE.parents[2]
LOCK = HERE / "maven-tools.lock.json"
SOURCES = (
    "https://maven-central.storage-download.googleapis.com/maven2/",
    "https://repo.maven.apache.org/maven2/",
)

JF_ACTION = ".github/actions/jfrog-cli/action.yml"
FIXTURES = "test/acceptance/testdata/maven"
PRODUCT = ("org/acme/", "org/weave/", "org/artifactweave/tooling/")
# A Maven command line names a plugin outside any POM by its full coordinate,
# such as maven-dependency-plugin:3.8.1:get in the features.
COMMAND_PLUGIN = re.compile(r"\b(org\.apache\.maven\.plugins):([A-Za-z0-9_.-]+):([0-9][A-Za-z0-9_.-]*)")


def digest(path):
    return hashlib.sha512(path.read_bytes()).hexdigest()


def required_plugins(root=ROOT):
    """Every plugin version the acceptance Maven runs name, from POMs and commands.

    Maven or image changes need no lock change when they keep these pins: the
    assertions block external repositories, so a tool the lock lacks fails the
    run at the command that needed it instead of being fetched silently.
    """
    found = set()
    for pom in sorted((root / FIXTURES).glob("*/pom.xml")):
        for plugin in ET.parse(pom).findall(".//{*}plugins/{*}plugin"):
            group = plugin.findtext("{*}groupId") or "org.apache.maven.plugins"
            artifact, version = plugin.findtext("{*}artifactId"), plugin.findtext("{*}version")
            if not version:
                raise ValueError("unpinned plugin in " + str(pom.relative_to(root)) + ": " + artifact)
            found.add((group, artifact, version))
    sources = [*(root / "test/acceptance/features").glob("*.feature"), *(root / "test/acceptance").glob("*.go")]
    for source in sorted(sources):
        found.update(COMMAND_PLUGIN.findall(source.read_text()))
    return found


def jf_version(root=ROOT):
    match = re.search(r"JF_VERSION:\s*'([^']+)'", (root / JF_ACTION).read_text())
    if match is None:
        raise ValueError("cannot read the pinned JF CLI version from " + JF_ACTION)
    return match.group(1)


def load_lock(path=LOCK, root=ROOT):
    lock = json.loads(path.read_text())
    if lock["schema"] != 2:
        raise ValueError("unsupported Maven tooling manifest")
    # The CLI release chooses the extractor it bootstraps, so a new jf pin
    # needs a requalified extractor; jf would otherwise download one itself.
    if lock["extractor"]["jf"] != jf_version(root):
        raise ValueError("JF CLI pin changed; regenerate and qualify the Maven extractor in " + str(path))
    entries = dict(lock["repository"])
    entries[lock["extractor"]["path"]] = lock["extractor"]["sha512"]
    for name, expected in entries.items():
        path_ = PurePosixPath(name)
        if (path_.is_absolute() or str(path_) != name or ".." in path_.parts
                or path_.suffix not in (".jar", ".pom")
                or name.startswith(PRODUCT)
                or len(expected) != 128 or any(c not in "0123456789abcdef" for c in expected)):
            raise ValueError("invalid or product artifact in Maven tooling manifest: " + name)
    for group, artifact, version in sorted(required_plugins(root)):
        base = group.replace(".", "/") + "/" + artifact + "/" + version + "/" + artifact + "-" + version
        for extension in (".pom", ".jar"):
            if base + extension not in lock["repository"]:
                raise ValueError("pinned plugin is absent from Maven tool supply: " + base + extension)
    return lock, entries


def prepare(cache, entries, fetcher=HERE / "fetch-pinned.sh", sources=SOURCES, budget=600):
    objects = cache / "objects"
    objects.mkdir(parents=True, exist_ok=True)
    deadline = time.monotonic() + budget

    def fetch(item):
        name, expected = item
        target = objects / expected
        if target.is_file() and digest(target) == expected:
            return
        remaining = deadline - time.monotonic()
        if remaining <= 0:
            raise TimeoutError("Maven tool preparation exceeded its time budget")
        with tempfile.TemporaryDirectory(prefix="fetch-", dir=cache) as work:
            download = Path(work) / "artifact"
            with subprocess.Popen(
                ["sh", str(fetcher), expected, str(download), *[base + name for base in sources]],
                stdout=subprocess.PIPE, stderr=subprocess.PIPE, text=True, start_new_session=True,
            ) as process:
                try:
                    _, stderr = process.communicate(timeout=min(90, remaining))
                except subprocess.TimeoutExpired as error:
                    # Killing only the shell leaves curl running after setup
                    # has failed. The whole owned process group must terminate.
                    os.killpg(process.pid, signal.SIGKILL)
                    process.communicate()
                    raise TimeoutError("Maven tool download timed out: " + name) from error
                if process.returncode or not download.is_file() or digest(download) != expected:
                    raise RuntimeError("cannot prepare pinned Maven tool " + name + "\n" + stderr)
            download.replace(target)

    # Each fetch has a deadline and the whole preparation has a ten-minute
    # budget. No acceptance command is retried if preparation fails.
    with ThreadPoolExecutor(max_workers=8) as workers:
        list(workers.map(fetch, entries.items()))
    print("Verified", len(entries), "pinned Maven tooling files")


def seed(cache, repository, lock, jf_home=None):
    """Copy verified tools into a fresh scenario repository and/or JF home."""
    # Verify everything before exposing any tool to a scenario. Cache restore
    # success alone says nothing about missing or corrupted objects.
    entries = dict(lock["repository"])
    extractor = lock["extractor"]
    entries[extractor["path"]] = extractor["sha512"]
    for name, expected in entries.items():
        source = cache / "objects" / expected
        if not source.is_file() or digest(source) != expected:
            raise ValueError("Maven tools missing or corrupt; run preparation first: " + name)
    if repository is not None:
        if repository.exists():
            raise ValueError("Maven tool repository must be fresh: " + str(repository))
        repository.mkdir(parents=True)
        for name, expected in lock["repository"].items():
            target = repository / name
            target.parent.mkdir(parents=True, exist_ok=True)
            shutil.copyfile(cache / "objects" / expected, target)
    if jf_home is not None:
        # The pinned CLI looks for its extractor here before downloading one
        # from releases.jfrog.io, an external source the assertions must not use.
        target = jf_home / "dependencies" / "maven" / extractor["version"] / Path(extractor["path"]).name
        target.parent.mkdir(parents=True, exist_ok=True)
        shutil.copyfile(cache / "objects" / extractor["sha512"], target)


def write_lock(repository, extractor_file, output=LOCK, root=ROOT):
    lock = json.loads(output.read_text())

    def verified(path):
        # Older Central releases publish SHA-1 only. Verify the publisher's
        # downloaded sidecar, then pin the exact bytes with SHA-512 for CI.
        sidecar = Path(str(path) + ".sha1")
        expected = sidecar.read_text().split()[0]
        if hashlib.sha1(path.read_bytes()).hexdigest() != expected:
            raise ValueError("upstream checksum mismatch: " + str(path))
        return digest(path)

    match = re.fullmatch(r"build-info-extractor-maven3-(.+)-uber\.jar", extractor_file.name)
    if match is None:
        raise ValueError("expected build-info-extractor-maven3-VERSION-uber.jar: " + str(extractor_file))
    version = match.group(1)
    files = {}
    for path in sorted(repository.rglob("*")):
        if path.suffix not in (".jar", ".pom"):
            continue
        name = path.relative_to(repository).as_posix()
        if name.startswith("org/artifactweave/tooling/"):
            continue
        files[name] = verified(path)
    lock["schema"] = 2
    lock["repository"] = files
    lock["extractor"] = {
        "jf": jf_version(root),
        "version": version,
        "path": "org/jfrog/buildinfo/build-info-extractor-maven3/" + version + "/" + extractor_file.name,
        "sha512": verified(extractor_file),
    }
    with tempfile.TemporaryDirectory(prefix="lock-", dir=output.parent) as work:
        candidate = Path(work) / "manifest.json"
        candidate.write_text(json.dumps(lock, indent=2) + "\n")
        load_lock(candidate, root)
        candidate.replace(output)


def main():
    parser = argparse.ArgumentParser(description=__doc__)
    parser.add_argument("command", choices=("prepare", "seed", "lock"))
    parser.add_argument("--cache", type=Path)
    parser.add_argument("--repository", type=Path)
    parser.add_argument("--jf-home", type=Path)
    parser.add_argument("--extractor", type=Path)
    args = parser.parse_args()
    if args.command == "lock":
        if args.repository is None or args.extractor is None:
            parser.error("lock requires --repository and --extractor with upstream .sha1 sidecars")
        write_lock(args.repository, args.extractor)
        return
    if args.cache is None:
        parser.error("prepare and seed require --cache")
    lock, entries = load_lock()
    if args.command == "prepare":
        prepare(args.cache, entries)
    else:
        if args.repository is None and args.jf_home is None:
            parser.error("seed requires --repository, --jf-home or both")
        seed(args.cache, args.repository, lock, args.jf_home)


if __name__ == "__main__":
    main()
