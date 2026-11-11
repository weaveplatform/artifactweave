"""Move the Hex acceptance client to the newest hexpm/elixir Alpine image.

Dependabot cannot: Docker Hub's registry answers 504 after 30 seconds when
asked for hexpm/elixir's tag list, even one page of 100, because the
repository holds a tag for every combination of Elixir, Erlang/OTP and OS
release, about a million (#1240). Docker Hub's own tag API filters by name,
so one query per Elixir release finds its images without listing the rest.

The newest Elixir release comes from GitHub. Among the Alpine tags for it and
for the pinned release, the newest that moves no part of the pin backwards
wins: hexpm builds each new Elixir, OTP or Alpine release against the others
over several hours, and a half-finished set must not pull the pinned OTP or
Alpine back. The pin takes the index digest Docker Hub reports for the tag.

  hex_image.py            move the pin in place
  hex_image.py --dry-run  print the move and change nothing

--elixir names a release instead of GitHub's newest. With GITHUB_OUTPUT set,
it writes changed (true|false) and tag for deps-refresh.yml.
"""

import argparse
import json
import os
from pathlib import Path
import re
import subprocess
import urllib.request
from urllib.parse import urlencode

from sync import ROOT, safe_path

DOCKERFILE = "test/acceptance/clients/hex.Dockerfile"
HUB = "https://hub.docker.com/v2/namespaces/hexpm/repositories/elixir/tags"
TAG = re.compile(r"(?P<elixir>[0-9]+(?:\.[0-9]+)+)-erlang-(?P<otp>[0-9]+(?:\.[0-9]+)*)"
                 r"-alpine-(?P<alpine>[0-9]+(?:\.[0-9]+)+)")
PIN = re.compile(r"^FROM hexpm/elixir:(?P<tag>\S+)@(?P<digest>sha256:[0-9a-f]{64})$", re.MULTILINE)
DIGEST = re.compile(r"sha256:[0-9a-f]{64}")
# One Elixir release has a few hundred tags across OTP and OS releases; a
# query that runs past this many pages has stopped narrowing the list.
MAX_PAGES = 50


def version(text):
    numbers = [int(number) for number in text.split(".")]
    while len(numbers) > 1 and numbers[-1] == 0:
        numbers.pop()
    return tuple(numbers)


def parts(tag):
    match = TAG.fullmatch(tag)
    if not match:
        return None
    return tuple(version(match[name]) for name in ("elixir", "otp", "alpine"))


def fetch_json(url):
    request = urllib.request.Request(url, headers={"Accept": "application/json",
                                                   "User-Agent": "artifactweave-deps-refresh"})
    with urllib.request.urlopen(request, timeout=60) as response:
        return json.load(response)


def hub_tags(prefix, fetch):
    # Docker Hub's name filter matches anywhere in a tag, so 1.20.5-erlang-
    # would also return a hypothetical 11.20.5-erlang- tag.
    url = HUB + "?" + urlencode({"page_size": 100, "name": prefix})
    for _ in range(MAX_PAGES):
        page = fetch(url)
        for result in page.get("results", []):
            if result.get("name", "").startswith(prefix):
                yield result["name"], result.get("digest") or ""
        url = page.get("next")
        if not url:
            return
        if not url.startswith(HUB + "?"):
            raise ValueError("Docker Hub paged outside the hexpm/elixir tag list: " + url)
    raise ValueError("more than " + str(MAX_PAGES) + " pages of tags matching " + prefix)


def newest_elixir(run):
    result = run(["gh", "api", "repos/elixir-lang/elixir/releases?per_page=30"],
                 stdout=subprocess.PIPE, text=True, check=True)
    releases = [release["tag_name"][1:] for release in json.loads(result.stdout)
                if not release.get("draft") and not release.get("prerelease")
                and re.fullmatch(r"v[0-9]+(?:\.[0-9]+)+", release.get("tag_name", ""))]
    if not releases:
        raise ValueError("no stable Elixir release on GitHub")
    return max(releases, key=version)


def choose(current, tags):
    floor = parts(current)
    best = None
    for name, digest in tags:
        key = parts(name)
        if key is None or not DIGEST.fullmatch(digest) or any(new < old for new, old in zip(key, floor)):
            continue
        rank = key + (name == current,)
        if best is None or rank > best[0]:
            best = (rank, name, digest)
    if best is None:
        raise ValueError("Docker Hub lists no hexpm/elixir tag for the pinned image " + current)
    return best[1], best[2]


def refresh(root=ROOT, elixir=None, *, dry_run=False, fetch=None, run=None):
    fetch, run = fetch or fetch_json, run or subprocess.run
    path = safe_path(root, DOCKERFILE)
    content = path.read_text()
    pins = PIN.findall(content)
    if len(pins) != 1 or parts(pins[0][0]) is None:
        raise ValueError(DOCKERFILE + ": expected one FROM hexpm/elixir"
                         ":<elixir>-erlang-<otp>-alpine-<alpine>@sha256 line")
    current, digest = pins[0]
    pinned = TAG.fullmatch(current)["elixir"]
    releases = {pinned, elixir or newest_elixir(run)}
    tags = [tag for release in sorted(releases, key=version) for tag in hub_tags(release + "-erlang-", fetch)]
    tag, new_digest = choose(current, tags)
    changed = (tag, new_digest) != (current, digest)
    if changed and not dry_run:
        path.write_text(PIN.sub(lambda _: "FROM hexpm/elixir:" + tag + "@" + new_digest, content))
    return current + "@" + digest, tag + "@" + new_digest, changed


def main():
    parser = argparse.ArgumentParser(description=__doc__, formatter_class=argparse.RawDescriptionHelpFormatter)
    parser.add_argument("--root", type=Path, default=ROOT)
    parser.add_argument("--elixir", help="an Elixir release instead of GitHub's newest")
    parser.add_argument("--dry-run", action="store_true")
    args = parser.parse_args()
    if args.elixir and not re.fullmatch(r"[0-9]+(?:\.[0-9]+)+", args.elixir):
        parser.error("--elixir takes a release such as 1.20.5")
    old, new, changed = refresh(args.root.resolve(), args.elixir, dry_run=args.dry_run)
    print(("hexpm/elixir " + old + " -> " + new) if changed else ("hexpm/elixir " + old + " is current"))
    if os.environ.get("GITHUB_OUTPUT"):
        with open(os.environ["GITHUB_OUTPUT"], "a") as output:
            output.write("changed=" + str(changed).lower() + "\ntag=" + new.split("@")[0] + "\n")


if __name__ == "__main__":
    main()
