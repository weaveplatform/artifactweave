package acceptance

import (
	"archive/tar"
	"bytes"
	"compress/gzip"
	"crypto/sha256"
	"fmt"
	"os"
	"path"
	"path/filepath"
	"strings"

	"github.com/cucumber/godog"
)

func (w *world) registerPyPI(sc *godog.ScenarioContext) {
	sc.Step(`^a Python sdist "([^"]*)" at "([^"]*)" in "([^"]*)"$`, w.writeSdist)
	sc.Step(
		`^a Python wheel "([^"]*)" at "([^"]*)" in "([^"]*)" with module "([^"]*)"$`,
		w.writeWheelModule,
	)
	sc.Step(
		`^a requirements file "([^"]*)" for "([^"]*)" matching "([^"]*)"$`,
		w.pythonRequirements,
	)
	sc.Step(`^the Python installation verifier is available$`, w.pythonVerifier)
	sc.Step(`^a copy of "([^"]*)" at "([^"]*)"$`, w.copyScenarioFile)
}

// The sdist builds with an in-tree PEP 517 backend and an empty build-requires
// list. pip still performs a real isolated source build, without fetching a
// backend or its dependencies from a public package index.
func (w *world) writeSdist(name, version, dir string) error {
	folded := strings.ToLower(wheelNameSeparators.ReplaceAllString(name, "_"))
	root := folded + "-" + version
	backend, err := os.ReadFile("testdata/python/build_backend.py")
	if err != nil {
		return err
	}
	files := []archiveFile{
		{"PKG-INFO", "Metadata-Version: 2.4\nName: " + name + "\nVersion: " + version +
			"\nSummary: An offline source distribution\nRequires-Python: >=3.8\n\n"},
		{folded + "/__init__.py", "GREETING = \"built from the source distribution\"\n"},
		{
			"pyproject.toml",
			"[build-system]\nrequires = []\nbuild-backend = \"build_backend\"\nbackend-path = [\".\"]\n",
		},
		{"build_backend.py", string(backend)},
	}
	var buf bytes.Buffer
	gz := gzip.NewWriter(&buf)
	tw := tar.NewWriter(gz)
	for _, f := range files {
		if err := tw.WriteHeader(&tar.Header{
			Name: path.Join(root, f.name), Mode: 0o644, Size: int64(len(f.content)),
		}); err != nil {
			return err
		}
		if _, err := tw.Write([]byte(f.content)); err != nil {
			return err
		}
	}
	if err := tw.Close(); err != nil {
		return err
	}
	if err := gz.Close(); err != nil {
		return err
	}
	return w.writeFile(path.Join(dir, root+".tar.gz"), &godog.DocString{Content: buf.String()})
}

func (w *world) pythonRequirements(name, requirement, distribution string) error {
	dir, err := w.filesDir()
	if err != nil {
		return err
	}
	b, err := os.ReadFile(filepath.Join(dir, distribution))
	if err != nil {
		return err
	}
	return w.writeFile(
		name,
		&godog.DocString{
			Content: fmt.Sprintf("%s --hash=sha256:%x\n", requirement, sha256.Sum256(b)),
		},
	)
}

func (w *world) pythonVerifier() error {
	b, err := os.ReadFile("testdata/python/check_install.py")
	if err != nil {
		return err
	}
	return w.writeFile("check_install.py", &godog.DocString{Content: string(b)})
}

func (w *world) copyScenarioFile(from, to string) error {
	dir, err := w.filesDir()
	if err != nil {
		return err
	}
	b, err := os.ReadFile(filepath.Join(dir, from))
	if err != nil {
		return err
	}
	return w.writeFile(to, &godog.DocString{Content: string(b)})
}
