"""Render current dependency references from committed pins; never execute inputs.

The catalog and templates belong to the trusted automation checkout. --root may
point at an inert snapshot of a dependency PR. Historical qualification reports
are deliberately not outputs: a new pin does not rewrite an old test result.
"""

import argparse
import json
from pathlib import Path, PurePosixPath
import re

ROOT = Path(__file__).resolve().parents[2]
CATALOG = Path(__file__).with_name("catalog.json")
TOKEN = re.compile(r"\{\{([a-zA-Z0-9_.-]+)\}\}")
FROM = re.compile(r"^FROM\s+(?:--\S+\s+)*(\S+)", re.MULTILINE)
TAG_VERSION = re.compile(r"v?[0-9]+(?:\.[0-9]+)*")
# What follows a tag's version is split into parts at -, ., _ and +, and a
# part that is one of these words, with or without a number, marks a
# prerelease: 3.15.0-118.1.beta (Dart), 3.16.0a1 and 3.16.0rc1 (Python),
# 1.19.0-rc.0 (Elixir), 4.0.0-rc-4 (Maven), 3.5.0-preview1 (Ruby),
# 3.23.0_alpha20250612 (Alpine). A whole part has to match, so variants such
# as alpine, bookworm or bullseye pass. Python's a and b need their number,
# since no one marks a prerelease with a bare a or b. Every part counts, not
# only the first: the Hex tag names an Erlang/OTP release after the Elixir
# one, and an OTP release candidate there is a prerelease client too. The
# catalog's version patterns stop at the first character after the number,
# so without this check depsync records 3.15.0-118.1.beta as the release
# 3.15.0 (#1304).
PRERELEASE = re.compile(r"[ab][0-9]+|(?:rc|alpha|beta|dev|pre|preview|ea|nightly|snapshot|canary|unstable)[0-9]*",
                        re.IGNORECASE)


def safe_path(root, name):
    path = PurePosixPath(name)
    if not path.parts or path.is_absolute() or str(path) != name or ".." in path.parts:
        raise ValueError("expected a repository-relative path: " + name)
    result = root.resolve()
    for part in path.parts:
        result = result / part
        if result.is_symlink():
            raise ValueError("dependency inputs and outputs cannot be symlinks: " + name)
    return result


def expand(template, values):
    return TOKEN.sub(lambda match: values[match[1]], template)


def prerelease_marker(tag):
    version = TAG_VERSION.match(tag)
    if not version:
        return None
    for part in re.split(r"[-._+]", tag[version.end():]):
        if PRERELEASE.fullmatch(part):
            return part
    return None


def refuse_prerelease_images(root, catalog):
    refused = []
    for path in sorted(catalog["images"]):
        for reference in FROM.findall(safe_path(root, path).read_text()):
            name = reference.split("@", 1)[0].rsplit("/", 1)[-1]
            tag = name.partition(":")[2]
            marker = prerelease_marker(tag)
            if marker:
                refused.append(path + ": " + name + " is a prerelease, marked " + marker
                               + " after its version " + TAG_VERSION.match(tag)[0]
                               + "; pin the newest stable release")
    if refused:
        raise ValueError("\n".join(refused))


def pins(root, catalog):
    values = {}
    for name, source in catalog["pins"].items():
        data = safe_path(root, source["path"]).read_text()
        matches = re.findall(source["pattern"], data, re.MULTILINE)
        if len(matches) != 1 or not isinstance(matches[0], str):
            raise ValueError(name + ": expected exactly one canonical pin, found " + str(len(matches))
                             + " in " + source["path"] + " matching " + source["pattern"])
        value = matches[0]
        if not re.fullmatch(r"[A-Za-z0-9._:/@+-]+", value):
            raise ValueError(name + ": invalid pin value")
        values[name] = value
    return values


def render(root, catalog, templates):
    # Before pins: a Python alpha such as 3.16.0a1-slim matches no version
    # pattern, and the refusal should name the marker, not a missing pin.
    refuse_prerelease_images(root, catalog)
    values = pins(root, catalog)
    outputs = {}
    recipe_paths = set(p.name[:-5] for p in (root / "docs/client-recipes/src").glob("*.json"))
    if recipe_paths != set(catalog["recipes"]):
        raise ValueError("every recipe must have a dependency-label template in tools/depsync/catalog.json")
    for name, template in sorted(catalog["recipes"].items()):
        path = "docs/client-recipes/src/" + name + ".json"
        data = json.loads(safe_path(root, path).read_text())
        data["client"] = expand(template, values)
        outputs[path] = json.dumps(data, indent=2, ensure_ascii=False) + "\n"
    for output, template in catalog["documents"].items():
        outputs[output] = expand(safe_path(templates, template).read_text(), values)
    for rule in catalog["references"]:
        path = rule["path"]
        data = outputs.get(path, safe_path(root, path).read_text())
        replacement = expand(rule["replacement"], values)
        data, count = re.subn(rule["pattern"], lambda match: match.expand(replacement), data, flags=re.MULTILINE)
        if count != rule.get("count", 1):
            raise ValueError(path + ": secondary reference no longer matches its declared pattern")
        outputs[path] = data
    image_paths = {str(p.relative_to(root)) for p in (root / "test/acceptance/clients").rglob("*")
                   if p.name == "Dockerfile" or p.name.endswith(".Dockerfile")}
    if image_paths != set(catalog["images"]):
        raise ValueError("every acceptance Dockerfile must be present in the dependency image inventory")
    return outputs


def sync(root=ROOT, *, check=False, catalog_path=CATALOG):
    catalog = json.loads(catalog_path.read_text())
    outputs = render(root, catalog, catalog_path.parent)
    changed = []
    for name, content in sorted(outputs.items()):
        path = safe_path(root, name)
        if path.read_text() == content:
            continue
        changed.append(name)
        if not check:
            path.write_text(content)
    if check and changed:
        raise ValueError("stale dependency references; run make deps-sync:\n" + "\n".join(changed))
    return changed


def main():
    parser = argparse.ArgumentParser(description=__doc__)
    parser.add_argument("--root", type=Path, default=ROOT)
    parser.add_argument("--check", action="store_true")
    args = parser.parse_args()
    changed = sync(args.root.resolve(), check=args.check)
    print("Dependency references current" if not changed else "Updated " + str(len(changed)) + " dependency references")


if __name__ == "__main__":
    main()
