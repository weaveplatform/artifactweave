"""The real downloader must repair partial caches and never seed product bytes."""
import hashlib
import importlib.util
import json
from pathlib import Path
import tempfile
import time
import unittest

SCRIPT = Path(__file__).resolve().parents[2] / 'test/acceptance/clients/maven-tools.py'
SPEC = importlib.util.spec_from_file_location('maven_tools', SCRIPT)
TOOLS = importlib.util.module_from_spec(SPEC)
SPEC.loader.exec_module(TOOLS)


class MavenToolsTest(unittest.TestCase):
    def setUp(self):
        self.work = tempfile.TemporaryDirectory()
        self.addCleanup(self.work.cleanup)
        self.root = Path(self.work.name)
        self.cache = self.root / 'cache'
        self.primary = self.root / 'primary'
        self.secondary = self.root / 'secondary'
        self.name = 'org/apache/tool/1/tool-1.jar'
        self.data = b'pinned build tool'
        self.sha = hashlib.sha512(self.data).hexdigest()
        self.entries = {self.name: self.sha}
        for base, data in [(self.primary, b'wrong bytes'), (self.secondary, self.data)]:
            path = base / self.name
            path.parent.mkdir(parents=True)
            path.write_bytes(data)
        self.sources = tuple(base.as_uri() + '/' for base in [self.primary, self.secondary])

    def prepare(self):
        TOOLS.prepare(self.cache, self.entries, sources=self.sources)

    def test_missing_and_corrupt_cache_use_verified_fallback(self):
        self.prepare()
        obj = self.cache / 'objects' / self.sha
        self.assertEqual(self.data, obj.read_bytes())
        obj.write_bytes(b'corrupt restored object')
        self.prepare()
        self.assertEqual(self.data, obj.read_bytes())
        # A complete cache needs no live source.
        (self.secondary / self.name).unlink()
        self.prepare()

    def test_unavailable_or_wrong_sources_fail_without_installing(self):
        (self.secondary / self.name).unlink()
        with self.assertRaisesRegex(RuntimeError, 'cannot prepare pinned Maven tool'):
            self.prepare()
        self.assertFalse((self.cache / 'objects' / self.sha).exists())

    def test_seed_is_private_and_whitelisted_and_requires_complete_cache(self):
        self.prepare()
        extractor = {'version': '1', 'path': self.name, 'sha512': self.sha}
        lock = {'repository': self.entries, 'extractor': extractor}
        (self.cache / 'objects' / 'product.jar').write_bytes(b'forbidden product')
        first, second = self.root / 'first', self.root / 'second'
        TOOLS.seed(self.cache, first, lock, self.root / 'jf-home')
        TOOLS.seed(self.cache, second, lock)
        self.assertEqual([self.name], [p.relative_to(first).as_posix() for p in first.rglob('*.jar')])
        (first / self.name).write_bytes(b'scenario writes')
        self.assertEqual(self.data, (second / self.name).read_bytes())
        self.assertEqual(self.data, (self.cache / 'objects' / self.sha).read_bytes())
        self.assertEqual(self.data, (self.root / 'jf-home/dependencies/maven/1/tool-1.jar').read_bytes())
        with self.assertRaisesRegex(ValueError, 'must be fresh'):
            TOOLS.seed(self.cache, first, lock)
        (self.cache / 'objects' / self.sha).unlink()
        with self.assertRaisesRegex(ValueError, 'missing or corrupt'):
            TOOLS.seed(self.cache, self.root / 'incomplete', lock)
        self.assertFalse((self.root / 'incomplete').exists())

    def test_preparation_deadline_fails_truthfully(self):
        with self.assertRaises(TimeoutError):
            TOOLS.prepare(self.cache, self.entries, budget=0)

    def test_committed_manifest_matches_inputs_and_excludes_product_coordinates(self):
        lock, entries = TOOLS.load_lock()
        self.assertTrue(entries)
        self.assertEqual('2.44.1', lock['extractor']['version'])
        self.assertEqual(TOOLS.jf_version(), lock['extractor']['jf'])

    def test_manifest_refuses_product_entries_missing_plugins_and_jf_drift(self):
        original = json.loads(TOOLS.LOCK.read_text())
        for reason in ['product', 'missing-plugin', 'missing-command-plugin', 'jf-drift']:
            with self.subTest(reason=reason):
                lock = json.loads(json.dumps(original))
                if reason == 'product':
                    lock['repository']['org/acme/smoke/lib/1/lib-1.jar'] = self.sha
                elif reason == 'missing-plugin':
                    del lock['repository']['org/apache/maven/plugins/maven-resources-plugin/3.3.1/maven-resources-plugin-3.3.1.jar']
                elif reason == 'missing-command-plugin':
                    # Only a feature's command line names this version.
                    del lock['repository']['org/apache/maven/plugins/maven-dependency-plugin/3.8.1/maven-dependency-plugin-3.8.1.pom']
                else:
                    lock['extractor']['jf'] = '0.0.0'
                path = self.root / 'manifest.json'
                path.write_text(json.dumps(lock))
                with self.assertRaises(ValueError):
                    TOOLS.load_lock(path)

    def test_required_plugins_come_from_poms_and_commands(self):
        root = self.root / 'repo'
        (root / 'test/acceptance/features').mkdir(parents=True)
        (root / 'test/acceptance/testdata/maven/lib').mkdir(parents=True)
        (root / 'test/acceptance/features/x.feature').write_text(
            'When I run mvn "org.apache.maven.plugins:maven-help-plugin:3.5.1:evaluate"\n')
        (root / 'test/acceptance/testdata/maven/lib/pom.xml').write_text(
            '<project xmlns="http://maven.apache.org/POM/4.0.0"><build><plugins><plugin>'
            '<artifactId>maven-jar-plugin</artifactId><version>3.4.2</version>'
            '</plugin></plugins></build></project>')
        self.assertEqual(
            {('org.apache.maven.plugins', 'maven-help-plugin', '3.5.1'),
             ('org.apache.maven.plugins', 'maven-jar-plugin', '3.4.2')},
            TOOLS.required_plugins(root))
        (root / 'test/acceptance/testdata/maven/lib/pom.xml').write_text(
            '<project><build><plugins><plugin><artifactId>maven-jar-plugin</artifactId>'
            '</plugin></plugins></build></project>')
        with self.assertRaisesRegex(ValueError, 'unpinned plugin'):
            TOOLS.required_plugins(root)

    def test_seed_extractor_alone_for_a_later_jf_home(self):
        self.prepare()
        extractor = {'version': '1', 'path': self.name, 'sha512': self.sha}
        TOOLS.seed(self.cache, None, {'repository': self.entries, 'extractor': extractor}, self.root / 'jf')
        self.assertEqual(self.data, (self.root / 'jf/dependencies/maven/1/tool-1.jar').read_bytes())
        self.assertFalse((self.root / 'repository').exists())

    def test_timeout_terminates_the_downloader_child(self):
        marker = self.root / 'orphan-child-completed'
        fetcher = self.root / 'slow-fetcher.sh'
        fetcher.write_text('sleep 0.4; echo leaked > "' + str(marker) + '"\n')
        with self.assertRaises(TimeoutError):
            TOOLS.prepare(self.cache, self.entries, fetcher=fetcher, budget=0.05)
        time.sleep(0.5)
        self.assertFalse(marker.exists(), 'timed-out preparation left its child running')
