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
