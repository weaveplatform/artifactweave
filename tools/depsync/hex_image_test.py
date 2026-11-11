import json
import os
from pathlib import Path
import re
import subprocess
import sys
import tempfile
import unittest
from unittest.mock import patch
from urllib.parse import parse_qs, urlencode, urlsplit

import hex_image
import sync


OLD = "sha256:" + "a" * 64
NEW = "sha256:" + "b" * 64
CURRENT = "1.20.5-erlang-29.1.2-alpine-3.24.3"


def hub(tags, page_size=100):
    """Docker Hub's tag API over tags, a {name: digest} map, filtered and paged as Hub does."""
    requests = []

    def fetch(url):
        requests.append(url)
        query = parse_qs(urlsplit(url).query)
        name, page = query["name"][0], int(query.get("page", ["1"])[0])
        matching = [{"name": tag, "digest": digest} for tag, digest in tags.items() if name in tag]
        start = (page - 1) * page_size
        more = start + page_size < len(matching)
        return {"results": matching[start:start + page_size],
                "next": hex_image.HUB + "?" + urlencode({"name": name, "page": page + 1}) if more else None}
    fetch.requests = requests
    return fetch


class HexImageTests(unittest.TestCase):
    def setUp(self):
        temp = tempfile.TemporaryDirectory()
        self.addCleanup(temp.cleanup)
        self.root = Path(temp.name)
        self.dockerfile = self.root / hex_image.DOCKERFILE
        self.dockerfile.parent.mkdir(parents=True)
        self.dockerfile.write_text((sync.ROOT / hex_image.DOCKERFILE).read_text().replace(
            re.search(r"^FROM (\S+)$", (sync.ROOT / hex_image.DOCKERFILE).read_text(), re.M)[1],
            "hexpm/elixir:" + CURRENT + "@" + OLD))

    def refresh(self, tags, elixir="1.20.5", **kwargs):
        return hex_image.refresh(self.root, elixir, fetch=hub(tags), **kwargs)

    def test_moves_to_the_newest_elixir_otp_and_alpine_and_depsync_reads_the_new_pins(self):
        before = self.dockerfile.read_text()
        old, new, changed = self.refresh({
            CURRENT: OLD,
            "1.20.5-erlang-29.1.2-alpine-3.24.4": "sha256:" + "c" * 64,
            "1.20.6-erlang-29.1.2-alpine-3.24.4": "sha256:" + "d" * 64,
            "1.20.6-erlang-29.1.3-alpine-3.24.4": NEW,
            "1.20.6-erlang-29.1.3-ubuntu-noble-20261024": "sha256:" + "e" * 64,
        }, elixir="1.20.6")
        self.assertTrue(changed)
        self.assertEqual(CURRENT + "@" + OLD, old)
        self.assertEqual("1.20.6-erlang-29.1.3-alpine-3.24.4@" + NEW, new)
        after = self.dockerfile.read_text()
        self.assertEqual(before.replace(CURRENT + "@" + OLD, "1.20.6-erlang-29.1.3-alpine-3.24.4@" + NEW), after)
        catalog = json.loads(sync.CATALOG.read_text())
        pin = lambda name, text: re.findall(catalog["pins"][name]["pattern"], text, re.M)
        self.assertEqual(["1.20.6"], pin("hex.version", after))
        self.assertEqual(["29.1.3"], pin("otp", after))
        self.assertEqual(["hexpm/elixir:1.20.6-erlang-29.1.3-alpine-3.24.4@" + NEW], pin("hex.image", after))
        self.assertEqual(pin("hex", before), pin("hex", after))

    def test_never_moves_a_part_of_the_pin_backwards(self):
        _, new, changed = self.refresh({
            CURRENT: OLD,
            "1.20.5-erlang-29.1.3-alpine-3.24.2": NEW,
            "1.20.5-erlang-28.5.0.8-alpine-3.24.4": NEW,
            "1.20.6-erlang-29.1.3-alpine-3.23.8": NEW,
        }, elixir="1.20.6")
        self.assertFalse(changed)
        self.assertEqual(CURRENT + "@" + OLD, new)

    def test_an_older_or_backported_elixir_never_replaces_the_pin(self):
        _, new, changed = self.refresh({CURRENT: OLD, "1.19.6-erlang-29.1.3-alpine-3.24.4": NEW}, elixir="1.19.6")
        self.assertFalse(changed)
        self.assertEqual(CURRENT + "@" + OLD, new)

    def test_a_rebuilt_tag_moves_only_the_digest(self):
        before = self.dockerfile.read_text()
        _, new, changed = self.refresh({CURRENT: NEW})
        self.assertTrue(changed)
        self.assertEqual(before.replace(OLD, NEW), self.dockerfile.read_text())
        self.assertEqual(CURRENT + "@" + NEW, new)

    def test_skips_other_systems_prereleases_substring_matches_and_missing_digests(self):
        _, new, changed = self.refresh({
            CURRENT: OLD,
            "1.20.5-erlang-30.0-rc1-alpine-3.24.4": NEW,
            "1.20.5-erlang-30.0-ubuntu-noble-20261024": NEW,
            "1.20.5-erlang-30.0-alpine-3.24.4-slim": NEW,
            "11.20.5-erlang-30.0-alpine-3.24.4": NEW,
            "1.20.5-erlang-30.0-alpine-3.24.4": "",
            "1.20.5-erlang-30.0.1-alpine-3.24.4": "sha256:short",
        })
        self.assertFalse(changed)
        self.assertEqual(CURRENT + "@" + OLD, new)

    def test_dry_run_changes_nothing(self):
        before = self.dockerfile.read_text()
        _, _, changed = self.refresh({CURRENT: OLD, "1.20.5-erlang-29.1.2-alpine-3.24.4": NEW}, dry_run=True)
        self.assertTrue(changed)
        self.assertEqual(before, self.dockerfile.read_text())

    def test_follows_every_page_but_stays_on_the_tag_list(self):
        tags = {"1.20.5-erlang-29.1.%d-ubuntu-noble-20261024" % n: OLD for n in range(250)}
        tags[CURRENT] = OLD
        tags["1.20.5-erlang-29.1.2-alpine-3.24.4"] = NEW
        fetch = hub(tags)
        self.assertEqual(sorted(tags), sorted(name for name, _ in hex_image.hub_tags("1.20.5-erlang-", fetch)))
        self.assertEqual(3, len(fetch.requests))
        def outside(url):
            return {"results": [], "next": "https://example.com/v2/tags?page=2"}
        with self.assertRaisesRegex(ValueError, "outside the hexpm/elixir tag list"):
            list(hex_image.hub_tags("1.20.5-erlang-", outside))
        with patch.object(hex_image, "MAX_PAGES", 2), self.assertRaisesRegex(ValueError, "more than 2 pages"):
            list(hex_image.hub_tags("1.20.5-erlang-", hub(tags)))

    def test_the_pin_must_be_one_hexpm_alpine_image(self):
        for line in ["FROM elixir:1.20.5-alpine@" + OLD, "FROM hexpm/elixir:1.20.5-erlang-29.1.2-ubuntu-noble-20261024@" + OLD,
                     "FROM hexpm/elixir:" + CURRENT]:
            self.dockerfile.write_text(line + "\n")
            with self.assertRaisesRegex(ValueError, "expected one FROM hexpm/elixir"):
                self.refresh({CURRENT: OLD})
        self.dockerfile.write_text("FROM hexpm/elixir:" + CURRENT + "@" + OLD + "\n")
        with self.assertRaisesRegex(ValueError, "no hexpm/elixir tag for the pinned image"):
            self.refresh({})

    def test_newest_elixir_is_the_highest_stable_release(self):
        releases = [{"tag_name": "v1.19.6"}, {"tag_name": "v1.21.0-rc.0", "prerelease": True},
                    {"tag_name": "v1.22.0", "prerelease": True}, {"tag_name": "v1.21.0", "draft": True},
                    {"tag_name": "v1.20.10"}, {"tag_name": "v1.20.9"}]
        def run(args, **kwargs):
            self.assertEqual(["gh", "api", "repos/elixir-lang/elixir/releases?per_page=30"], args)
            return subprocess.CompletedProcess(args, 0, stdout=json.dumps(releases))
        self.assertEqual("1.20.10", hex_image.newest_elixir(run))
        releases = [{"tag_name": "v1.21.0-rc.0", "prerelease": True}]
        with self.assertRaisesRegex(ValueError, "no stable Elixir release"):
            hex_image.newest_elixir(run)

    def test_main_reports_the_move_to_the_workflow(self):
        output = self.root / "github-output"
        fetch = hub({CURRENT: OLD, "1.20.5-erlang-29.1.2-alpine-3.24.4": NEW})
        def run(args, **kwargs):
            return subprocess.CompletedProcess(args, 0, stdout=json.dumps([{"tag_name": "v1.20.5"}]))
        argv = ["hex_image.py", "--root", str(self.root)]
        with patch.object(hex_image, "fetch_json", fetch), patch.object(subprocess, "run", run), \
                patch.object(sys, "argv", argv), patch.dict(os.environ, {"GITHUB_OUTPUT": str(output)}), \
                patch("builtins.print"):
            hex_image.main()
        self.assertEqual("changed=true\ntag=1.20.5-erlang-29.1.2-alpine-3.24.4\n", output.read_text())
        self.assertIn("1.20.5-erlang-29.1.2-alpine-3.24.4@" + NEW, self.dockerfile.read_text())


if __name__ == "__main__":
    unittest.main()
