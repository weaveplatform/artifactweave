import json
from pathlib import Path
import re
import shutil
import tempfile
import unittest

import sync


class ReferenceTests(unittest.TestCase):
    def setUp(self):
        self.temp = tempfile.TemporaryDirectory()
        self.addCleanup(self.temp.cleanup)
        self.root = Path(self.temp.name)
        self.catalog = json.loads(sync.CATALOG.read_text())
        paths = {pin["path"] for pin in self.catalog["pins"].values()}
        paths |= set(self.catalog["documents"]) | set(self.catalog["images"])
        paths |= {rule["path"] for rule in self.catalog["references"]}
        paths |= {"docs/client-recipes/src/" + name + ".json" for name in self.catalog["recipes"]}
        for name in paths:
            path = self.root / name
            path.parent.mkdir(parents=True, exist_ok=True)
            shutil.copyfile(sync.ROOT / name, path)
        sync.sync(self.root)
        self.values = sync.pins(self.root, self.catalog)

    def set_pin(self, name, value):
        source = self.catalog["pins"][name]
        path = self.root / source["path"]
        content = path.read_text()
        match = re.search(source["pattern"], content, re.MULTILINE)
        self.assertIsNotNone(match)
        path.write_text(content[:match.start(1)] + value + content[match.end(1):])

    def read(self, path):
        return (self.root / path).read_text()

    def test_r_tag_updates_assertion_inventory_and_recipe(self):
        self.set_pin("r.version", "99.7.0")
        with self.assertRaisesRegex(ValueError, "stale dependency references"):
            sync.sync(self.root, check=True)
        sync.sync(self.root)
        self.assertIn('getRversion()) == "99.7.0"', self.read("test/acceptance/clients/r.Dockerfile"))
        self.assertIn('"client": "R 99.7.0"', self.read("docs/client-recipes/src/cran.json"))
        self.assertIn("r-base:99.7.0@", self.read("docs/testing/ci-client-images.md"))
        self.assertEqual([], sync.sync(self.root))
        self.assertEqual([], sync.sync(self.root, check=True))

    def test_node_lanes_are_independent_and_digest_only_updates_render(self):
        legacy = self.read("test/acceptance/clients/node-npm12/Dockerfile")
        self.set_pin("node.version", "99.14.0")
        self.set_pin("node.image", self.values["node.image"].split("@sha256:")[0].replace(self.values["node.version"], "99.14.0") + "@sha256:" + "a" * 64)
        sync.sync(self.root)
        self.assertEqual(legacy, self.read("test/acceptance/clients/node-npm12/Dockerfile"))
        self.assertIn("Node 99.14.0; npm " + self.values["npm12"] + " on Node " + self.values["node-npm12.version"], self.read("docs/client-recipes/src/npm.json"))
        self.assertIn("@sha256:" + "a" * 64, self.read("docs/testing/ci-client-images.md"))

    def test_tool_action_and_go_pins_update_all_consumers(self):
        self.set_pin("go", "1.99")
        self.set_pin("jfrog", "99.129.0")
        sync.sync(self.root)
        self.assertIn("Go 1.99 (module toolchain); JFrog CLI 99.129.0", self.read("docs/client-recipes/src/go.json"))
        for name in ["generic", "skills", "helmoci", "terraform", "agentplugins"]:
            self.assertIn("JFrog CLI 99.129.0", self.read("docs/client-recipes/src/" + name + ".json"))
        self.assertIn("releases/tag/v99.129.0", self.read("docs/testing/ci-native-client-inventory.md"))

    def test_tool_download_references_follow_the_canonical_url(self):
        self.set_pin("gradle", "99.9.0")
        sync.sync(self.root)
        content = self.read("test/acceptance/clients/maven.Dockerfile")
        self.assertNotIn(self.values["gradle"], content)
        self.assertIn("/opt/gradle/gradle-99.9.0/bin/gradle", content)
        self.assertIn("grep -F 'Gradle 99.9.0'", content)

    def test_workflow_versions_update_current_inventory_but_not_history(self):
        self.set_pin("hf-hub", "99.4.0")
        sync.sync(self.root)
        self.assertIn("huggingface_hub 99.4.0", self.read("docs/client-recipes/src/huggingfaceml.json"))
        content = self.read("docs/testing/ci-native-client-inventory.md")
        self.assertIn("huggingface-hub/99.4.0/#files", content)
        self.assertIn("Hub 2.3 then passed", content)

    def test_ruby_client_pins_update_downloads_installation_and_recipe_independently(self):
        self.set_pin("rubygems", "99.1.0")
        self.set_pin("bundler", "99.2.0")
        sync.sync(self.root)
        image = self.read("test/acceptance/clients/ruby.Dockerfile")
        self.assertEqual(3, image.count("rubygems-update-99.1.0.gem"))
        self.assertEqual(3, image.count("bundler-99.2.0.gem"))
        self.assertIn("gem fetch rubygems-update -v 99.1.0", image)
        self.assertIn("gem fetch bundler -v 99.2.0", image)
        self.assertIn("RubyGems 99.1.0 and Bundler 99.2.0", self.read("docs/client-recipes/src/gems.json"))
        self.assertIn("RubyGems 99.1.0 and Bundler 99.2.0", self.read("docs/testing/ci-client-images.md"))
        self.assertEqual([], sync.sync(self.root, check=True))

    def test_terraform_consumers_follow_the_canonical_version_and_platform_checksum(self):
        self.set_pin("terraform", "99.7.0")
        self.set_pin("terraform-linux-amd64-sha256", "e" * 64)
        sync.sync(self.root)
        for path in [".github/workflows/quality-gate.yml", ".github/workflows/platform-scenarios.yml"]:
            workflow = self.read(path)
            self.assertIn("TF_VERSION: '99.7.0'", workflow)
            self.assertIn("TF_SHA256: '" + "e" * 64 + "'", workflow)
        self.assertEqual([], sync.sync(self.root, check=True))

    def test_model_install_instructions_follow_the_client_pin(self):
        self.set_pin("frogml", "99.3.0")
        sync.sync(self.root)
        self.assertIn("python -m pip install frogml==99.3.0", self.read("docs/operations/machinelearning.md"))
        self.assertEqual([], sync.sync(self.root, check=True))

    def test_cross_architecture_and_abi_references_follow_pins(self):
        self.set_pin("idea", "2099.1")
        self.set_pin("p2", "99.42")
        self.set_pin("lua", "5.99.0")
        sync.sync(self.root)
        idea = self.read("test/acceptance/clients/jetbrains.Dockerfile")
        self.assertIn("archive=idea-2099.1.tar.gz", idea)
        self.assertIn("archive=idea-2099.1-aarch64.tar.gz", idea)
        eclipse = self.read("test/acceptance/clients/p2.Dockerfile")
        self.assertIn("eclipse-platform-99.42-linux-gtk-x86_64.tar.gz", eclipse)
        self.assertIn("eclipse-platform-99.42-linux-gtk-aarch64.tar.gz", eclipse)
        self.assertIn("drops4/R-99.42-", eclipse)
        self.assertIn("--lua-version=5.99 ", self.read("test/acceptance/clients/luarocks.Dockerfile"))
        self.assertEqual([], sync.sync(self.root, check=True))

    def test_recipe_prerequisites_follow_client_pins(self):
        self.set_pin("sbt", "99.1.0")
        self.set_pin("gradle", "99.2.0")
        self.set_pin("idea", "2099.2")
        sync.sync(self.root)
        sbt = json.loads(self.read("docs/client-recipes/src/sbt.json"))
        self.assertIn("sbt.version=99.1.0", " ".join(sbt["prerequisites"]))
        for name, key in [("gradle", "gradle"), ("jetbrainsplugins", "idea")]:
            recipe = json.loads(self.read("docs/client-recipes/src/" + name + ".json"))
            self.assertNotIn(self.values[key], " ".join(recipe["prerequisites"]))
        self.assertEqual([], sync.sync(self.root, check=True))

    def test_native_fixture_launchers_and_version_assertions_follow_pins(self):
        self.set_pin("sbt", "99.1.0")
        self.set_pin("hf-hub", "99.2.0")
        self.set_pin("hf-xet", "99.3.0")
        sync.sync(self.root)
        self.assertIn("sbt.version=99.1.0", self.read("test/acceptance/sbt_steps_test.go"))
        native = self.read("test/server/artifacts/huggingface/huggingface_xet_native_test.go")
        self.assertIn('assert version("huggingface_hub") == "99.2.0"', native)
        self.assertIn('assert version("hf_xet") == "99.3.0"', native)
        self.assertIn("native Hub 99.2.0 / hf_xet 99.3.0", native)
        self.assertEqual([], sync.sync(self.root, check=True))

    def test_missing_and_duplicate_canonical_pins_fail_closed(self):
        path = self.root / "go.mod"
        original = path.read_text()
        for value in [re.sub(self.catalog["pins"]["go"]["pattern"], "// absent directive", original, flags=re.MULTILINE), original + "\ngo 1.28\n"]:
            path.write_text(value)
            with self.assertRaisesRegex(ValueError, "exactly one canonical pin"):
                sync.sync(self.root)

    def test_new_images_and_recipes_cannot_silently_miss_the_inventory(self):
        for name in ["test/acceptance/clients/unknown.Dockerfile", "docs/client-recipes/src/unknown.json"]:
            path = self.root / name
            path.write_text("{}")
            with self.assertRaisesRegex(ValueError, "every .*(Dockerfile|recipe)"):
                sync.sync(self.root)
            path.unlink()

    def test_symlinks_and_traversal_are_rejected(self):
        for name in ["../outside", "/tmp/outside", ".", "", "docs/../outside"]:
            with self.assertRaises(ValueError):
                sync.safe_path(self.root, name)
        (self.root / "link").symlink_to(self.root / "go.mod")
        with self.assertRaises(ValueError):
            sync.safe_path(self.root, "link")


if __name__ == "__main__":
    unittest.main()
