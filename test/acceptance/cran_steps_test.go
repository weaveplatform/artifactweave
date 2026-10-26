package acceptance

import (
	"compress/gzip"
	"fmt"
	"net/url"
	"os"
	"path/filepath"
	"runtime"
	"strings"

	"github.com/cucumber/godog"

	"github.com/weaveplatform/artifactweave/internal/fakeupstream"
)

func (w *world) registerCRAN(sc *godog.ScenarioContext) {
	sc.Step(`^the published assertthat CRAN source is under "([^"]*)"$`, w.cranFixture)
	sc.Step(`^the published assertthat CRAN upstream is available$`, w.cranUpstream)
	sc.Step(`^the CRAN upstream corrupts its index$`, w.cranCorruptIndex)
	sc.Step(`^the CRAN upstream redirects its source archive$`, w.cranRedirectArchive)
	sc.Step(`^I install assertthat from CRAN repository "([^"]*)" with R$`, w.runCRANInstall)
	sc.Step(`^I clear the R library$`, w.clearRLibrary)
}

func (w *world) cranRedirectArchive() error {
	body, err := os.ReadFile(cranTestdata("assertthat_0.2.1.tar.gz"))
	if err != nil {
		return err
	}
	w.remote.Set("/src/contrib/assertthat_0.2.1.tar.gz",
		fakeupstream.Resource{Redirect: w.remote.URL + "/mirrors/assertthat_0.2.1.tar.gz"})
	w.remote.Set("/mirrors/assertthat_0.2.1.tar.gz", fakeupstream.Resource{Body: body})
	return nil
}

func cranTestdata(name string) string {
	return filepath.Join("..", "..", "internal", "format", "cran", "testdata", name)
}

func (w *world) cranFixture(folder string) error {
	data, err := os.ReadFile(cranTestdata("assertthat_0.2.1.tar.gz"))
	if err != nil {
		return err
	}
	dir, err := w.filesDir()
	if err != nil {
		return err
	}
	p := filepath.Join(dir, folder, "assertthat_0.2.1.tar.gz")
	if err := os.MkdirAll(filepath.Dir(p), 0o750); err != nil {
		return err
	}
	return os.WriteFile(p, data, 0o600)
}

func (w *world) cranUpstream() error {
	index, err := os.ReadFile(cranTestdata("assertthat-PACKAGES"))
	if err != nil {
		return err
	}
	body, err := os.ReadFile(cranTestdata("assertthat_0.2.1.tar.gz"))
	if err != nil {
		return err
	}
	w.remote = fakeupstream.New(w.t, fakeupstream.Options{})
	w.closers = append(w.closers, w.remote.Close)
	w.remote.Set("/src/contrib/PACKAGES", fakeupstream.Resource{Body: index})
	var compressed strings.Builder
	zw := gzip.NewWriter(&compressed)
	if _, err := zw.Write(index); err != nil {
		return err
	}
	if err := zw.Close(); err != nil {
		return err
	}
	w.remote.Set(
		"/src/contrib/PACKAGES.gz",
		fakeupstream.Resource{Body: []byte(compressed.String())},
	)
	w.remote.Set("/src/contrib/assertthat_0.2.1.tar.gz", fakeupstream.Resource{Body: body})
	if w.vars == nil {
		w.vars = map[string]string{}
	}
	w.vars["upstream"] = w.remote.URL
	return nil
}

func (w *world) cranCorruptIndex() error {
	w.remote.Set(
		"/src/contrib/PACKAGES",
		fakeupstream.Resource{Body: []byte("Package: ../secret\nVersion: 1.0\n\n")},
	)
	w.remote.Set("/src/contrib/PACKAGES.gz", fakeupstream.Resource{Body: []byte("invalid gzip")})
	return nil
}

func (w *world) runCRANInstall(key string) error {
	dir, err := w.filesDir()
	if err != nil {
		return err
	}
	if err := os.MkdirAll(filepath.Join(dir, "r-lib"), 0o750); err != nil {
		return err
	}
	base := w.url
	if runtime.GOOS == "darwin" && os.Getenv("ARTIFACTWEAVE_R_DESKTOP") == "1" {
		base = strings.Replace(base, "127.0.0.1", "host.docker.internal", 1)
	}
	u, err := url.Parse(base)
	if err != nil {
		return err
	}
	u.User = url.UserPassword("admin", testAdminPassword)
	repository := u.String() + "/artifactory/" + url.PathEscape(key)
	script := fmt.Sprintf(
		`options(repos=c(CRAN=%q), timeout=45); install.packages("assertthat", lib="/work/r-lib", type="source", quiet=TRUE); stopifnot(as.character(packageVersion("assertthat", lib.loc="/work/r-lib")) == "0.2.1")`,
		repository,
	)
	return w.runContainer(w.rImage, nil, []string{"Rscript", "-e", script})
}

func (w *world) clearRLibrary() error {
	dir, err := w.filesDir()
	if err != nil {
		return err
	}
	if err := os.RemoveAll(filepath.Join(dir, "r-lib")); err != nil {
		return err
	}
	return os.MkdirAll(filepath.Join(dir, "r-lib"), 0o750)
}
