package acceptance

import (
	"bytes"
	"crypto/sha256"
	"encoding/hex"
	"fmt"
	"net/url"
	"os"
	"path/filepath"
	"runtime"
	"strings"

	"github.com/cucumber/godog"

	"github.com/weaveplatform/artifactweave/internal/fakeupstream"
	condafmt "github.com/weaveplatform/artifactweave/internal/format/conda"
)

const condaFixtureName = "artifactweave-conda-fixture-1.0.0-0.tar.bz2"

func (w *world) registerConda(sc *godog.ScenarioContext) {
	sc.Step(`^a controlled Conda package under "([^"]*)"$`, w.copyCondaPackage)
	sc.Step(`^a current Conda package under "([^"]*)"$`, w.copyModernCondaPackage)
	sc.Step(`^the Conda channel is "([^"]*)"$`, w.setCondaChannel)
	sc.Step(`^I run micromamba "([^"]*)"$`, w.runMicromamba)
	sc.Step(`^a Conda upstream serves the controlled package$`, w.condaUpstream)
	sc.Step(`^the Conda upstream redirects the package$`, w.condaRedirect)
	sc.Step(`^the Conda upstream corrupts the package$`, w.condaCorrupt)
	sc.Step(`^the Conda upstream adds a modern package$`, w.condaUpstreamAddsModern)
	sc.Step(`^I clear the Conda client cache$`, w.clearCondaClient)
}

func (w *world) copyModernCondaPackage(folder string) error {
	if folder != "conda" {
		return fmt.Errorf("unexpected Conda fixture folder %q", folder)
	}
	const name = "ca-certificates-2026.7.22-hbd8a1cb_0.conda"
	body, err := os.ReadFile(filepath.Join("..", "..", "internal", "format", "conda",
		"testdata", name))
	if err != nil {
		return err
	}
	dir, err := w.filesDir()
	if err != nil {
		return err
	}
	if err := os.MkdirAll(filepath.Join(dir, folder), 0o755); err != nil {
		return err
	}
	return os.WriteFile(filepath.Join(dir, folder, name), body, 0o644)
}

func condaFixture() ([]byte, error) {
	return os.ReadFile(filepath.Join("..", "..", "internal", "format", "conda", "testdata",
		condaFixtureName))
}

func (w *world) copyCondaPackage(folder string) error {
	if folder != "conda" {
		return fmt.Errorf("unexpected Conda fixture folder %q", folder)
	}
	body, err := condaFixture()
	if err != nil {
		return err
	}
	dir, err := w.filesDir()
	if err != nil {
		return err
	}
	if err := os.MkdirAll(filepath.Join(dir, folder), 0o755); err != nil {
		return err
	}
	return os.WriteFile(filepath.Join(dir, folder, condaFixtureName), body, 0o644)
}

func (w *world) setCondaChannel(key string) error {
	base := w.url
	if runtime.GOOS == "darwin" && os.Getenv("ARTIFACTWEAVE_CONDA_DESKTOP") == "1" {
		base = strings.Replace(base, "127.0.0.1", "host.docker.internal", 1)
	}
	u, err := url.Parse(base)
	if err != nil {
		return err
	}
	u.User = url.UserPassword("admin", testAdminPassword)
	if w.vars == nil {
		w.vars = map[string]string{}
	}
	w.vars["conda_channel"] = u.String() + "/artifactory/api/conda/" + key
	return nil
}

func (w *world) runMicromamba(args string) error {
	// Each scenario keeps its package cache and environment under /work. No
	// default channels or runner configuration may satisfy this install.
	command := append([]string{"--no-rc", "--root-prefix", "/work/conda-root"},
		strings.Fields(w.expand(args))...)
	return w.runContainer(w.condaImage, []string{
		"--platform", "linux/amd64", "--entrypoint", "micromamba",
		"-e", "MAMBA_ROOT_PREFIX=/work/conda-root",
		"-e", "CONDA_PKGS_DIRS=/work/conda-root/pkgs",
	}, command)
}

func (w *world) clearCondaClient() error {
	dir, err := w.filesDir()
	if err != nil {
		return err
	}
	return os.RemoveAll(filepath.Join(dir, "conda-root"))
}

func (w *world) condaUpstream() error {
	body, err := condaFixture()
	if err != nil {
		return err
	}
	id, _ := condafmt.ParsePackagePath("noarch/" + condaFixtureName)
	rec, err := condafmt.ReadPackage(w.t.Context(), bytes.NewReader(body), int64(len(body)), id)
	if err != nil {
		return err
	}
	digest := sha256.Sum256(body)
	rec.SHA256 = hex.EncodeToString(digest[:])
	rec.Size = int64(len(body))
	index := condafmt.EmptyRepodata("noarch")
	index.Packages[condaFixtureName] = rec
	encoded, err := condafmt.EncodeRepodata(index, "", "noarch")
	if err != nil {
		return err
	}
	w.remote = fakeupstream.New(w.t, fakeupstream.Options{})
	w.closers = append(w.closers, w.remote.Close)
	w.remote.Set("/noarch/repodata.json", fakeupstream.Resource{Body: encoded["repodata.json"]})
	w.remote.Set("/noarch/"+condaFixtureName, fakeupstream.Resource{Body: body})
	if w.vars == nil {
		w.vars = map[string]string{}
	}
	w.vars["upstream"] = w.remote.URL
	return nil
}

func (w *world) condaRedirect() error {
	body, err := condaFixture()
	if err != nil {
		return err
	}
	w.remote.Set("/redirected/"+condaFixtureName, fakeupstream.Resource{Body: body})
	w.remote.Set("/noarch/"+condaFixtureName, fakeupstream.Resource{
		Redirect: w.remote.URL + "/redirected/" + condaFixtureName,
	})
	return nil
}

func (w *world) condaCorrupt() error {
	body, err := condaFixture()
	if err != nil {
		return err
	}
	body = append([]byte(nil), body...)
	body[len(body)-1] ^= 0xff
	w.remote.Set("/noarch/"+condaFixtureName, fakeupstream.Resource{Body: body})
	return nil
}

func (w *world) condaUpstreamAddsModern() error {
	const modern = "ca-certificates-2026.7.22-hbd8a1cb_0.conda"
	legacy, err := condaFixture()
	if err != nil {
		return err
	}
	modernBody, err := os.ReadFile(filepath.Join("..", "..", "internal", "format",
		"conda", "testdata", modern))
	if err != nil {
		return err
	}
	index := condafmt.EmptyRepodata("noarch")
	for _, pkg := range []struct {
		name string
		body []byte
	}{
		{condaFixtureName, legacy},
		{modern, modernBody},
	} {
		id, _ := condafmt.ParsePackagePath("noarch/" + pkg.name)
		rec, err := condafmt.ReadPackage(w.t.Context(), bytes.NewReader(pkg.body),
			int64(len(pkg.body)), id)
		if err != nil {
			return err
		}
		digest := sha256.Sum256(pkg.body)
		rec.SHA256 = hex.EncodeToString(digest[:])
		rec.Size = int64(len(pkg.body))
		if id.Extension == ".conda" {
			index.PackagesConda[pkg.name] = rec
		} else {
			index.Packages[pkg.name] = rec
		}
		w.remote.Set("/noarch/"+pkg.name, fakeupstream.Resource{Body: pkg.body})
	}
	encoded, err := condafmt.EncodeRepodata(index, "", "noarch")
	if err != nil {
		return err
	}
	w.remote.Set("/noarch/repodata.json", fakeupstream.Resource{Body: encoded["repodata.json"]})
	return nil
}
