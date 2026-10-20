package pypi_test

import (
	"bytes"
	"errors"
	"fmt"
	"net/http"
	"testing"

	"github.com/weaveplatform/artifactweave/internal/format"
	"github.com/weaveplatform/artifactweave/internal/format/pypi"
	"github.com/weaveplatform/artifactweave/internal/service/item"
	"github.com/weaveplatform/artifactweave/pkg/repo"
)

// The messages 7.x answers (pypi-repositories), without the stray `” +`
// JFrog's documented layout text carries.
const (
	namingRefused = "Action prevented due to artifact file name normalization requirements - " +
		"artifact name %s does not conform to normalized requirements"
	layoutRefused = "Action prevented due to Enforce Layout policy - package file name/version " +
		"%s does not match the package internal name/version %s/%s in the metadata file"
	layoutNoMetadata = "Action prevented due to Enforce Layout policy - " +
		"could not find metadata file to compare"
)

// refusal is the format's refusal err carries, or nil.
func refusal(err error) *format.Rejection {
	var r *format.Rejection
	if errors.As(err, &r) {
		return r
	}
	return nil
}

func wantRefused(t *testing.T, what string, err error, status int, message string) {
	t.Helper()
	r := refusal(err)
	if r == nil || r.Status != status || r.Message != message {
		t.Errorf("%s: %v, want %d %q", what, err, status, message)
	}
}

// sdistNamed is an sdist stored under file whose metadata names name.
func sdistNamed(name string) []byte { return sdist(name, "1.0.0", meta(name, "1.0.0")) }

// Enforce layout compares the file name with the metadata literally, as
// JFrog's table does: each row of it, an sdist and a wheel, and content
// without metadata. It is off by default, as in 7.x.
func TestEnforceLayout(t *testing.T) {
	off := newWorld(t, pypi.Settings{})
	if _, err := off.tryDeploy(
		off.c,
		"jfrog_pypi-1.0.0.tar.gz",
		sdistNamed("jfrog-pypi"),
		nil,
	); err != nil {
		t.Fatalf("off by default: %v", err)
	}
	w := newWorld(t, pypi.Settings{EnforceLayout: true})
	for _, c := range []struct {
		file, name string
		ok         bool
	}{
		{"jfrog-pypi-1.0.0.tar.gz", "jfrog-pypi", true},
		{"jfrog-pypi-1.0.0.tar.gz", "jfrog.pypi", false},
		{"jfrog_pypi-1.0.0.tar.gz", "jfrog_pypi", true},
		{"jfrog_pypi-1.0.0.tar.gz", "jfrog-pypi", false},
		{"jfrog.pypi-1.0.0.tar.gz", "jfrog.pypi", true},
		{"jfrog.pypi-1.0.0.zip", "jfrog.pypi", true},
		{"jfrog.pypi-1.0.0-extra.tar.gz", "jfrog.pypi", false},
	} {
		_, err := w.tryDeploy(w.c, "f/"+c.file, sdistNamed(c.name), nil)
		if c.ok && err != nil {
			t.Errorf("%s named %s: %v", c.file, c.name, err)
		}
		if !c.ok {
			wantRefused(t, c.file+" named "+c.name, err, http.StatusForbidden,
				fmt.Sprintf(layoutRefused, c.file, c.name, "1.0.0"))
		}
	}
	m := meta("Jfrog_PyPI", "1.0")
	if _, err := w.tryDeploy(w.c, "Jfrog_PyPI-1.0-1-py3-none-any.whl",
		wheel("Jfrog_PyPI", "1.0", m), nil); err != nil {
		t.Errorf("a wheel with a build tag: %v", err)
	}
	_, err := w.tryDeploy(
		w.c,
		"jfrog_pypi-1.0-py3-none-any.whl",
		wheel("Jfrog_PyPI", "1.0", m),
		nil,
	)
	wantRefused(t, "a wheel of another case", err, http.StatusForbidden,
		fmt.Sprintf(layoutRefused, "jfrog_pypi-1.0-py3-none-any.whl", "Jfrog_PyPI", "1.0"))
	_, err = w.tryDeploy(w.c, "x-1.0.tar.gz", []byte("no archive"), nil)
	wantRefused(t, "no metadata", err, http.StatusForbidden, layoutNoMetadata)
	for p, body := range map[string][]byte{
		"README.txt":    []byte("not a distribution"),
		"x-1.0.whl.asc": []byte("a signature"),
	} {
		if _, err := w.tryDeploy(w.c, p, body, nil); err != nil {
			t.Errorf("%s: %v", p, err)
		}
	}
}

// Naming normalisation refuses a "-" in the file name's name segment, as
// JFrog's table does, reading the segment by the metadata's version, or
// by the file name's shape when the content has none.
func TestEnforceNamingNormalization(t *testing.T) {
	w := newWorld(t, pypi.Settings{EnforceNamingNormalization: true})
	jw := wheel("jfrog-pypi", "1.0.0", meta("jfrog-pypi", "1.0.0"))
	for _, c := range []struct {
		file string
		body []byte
		ok   bool
	}{
		{"jfrog_pypi-1.0.0.tar.gz", sdistNamed("jfrog-pypi"), true},
		{"jfrog.pypi-1.0.0.tar.gz", sdistNamed("jfrog.pypi"), true},
		{"jfrog-pypi-1.0.0.tar.gz", sdistNamed("jfrog-pypi"), false},
		{"jfrog_pypi-1.0.0-1-py3-none-any.whl", jw, true},
		{"jfrog-pypi-1.0.0-py3-none-any.whl", jw, false},
		// The version is read where it ends the stem or a field: 1.0 at
		// the start of 1.0.1 is not it.
		{"x-1.0.1-1.0.tar.gz", sdist("x", "1.0", meta("x", "1.0")), false},
		// The metadata's version is not in the file name: its shape decides.
		{"jfrog-pypi-1.0.tar.gz", sdistNamed("jfrog-pypi"), false},
		{"jfrog_pypi-1.0.tar.gz", sdistNamed("jfrog-pypi"), true},
		// No metadata: a wheel's fields, or an sdist's part before its
		// last "-".
		{"a_b-1.0-py3-none-any.whl", []byte("x"), true},
		{"a_b-1.0-2-py3-none-any.whl", []byte("x"), true},
		{"a_b-1.0-b2-py3-none-any.whl", []byte("x"), false},
		// Six fields whose third begins with a digit read as a build tag,
		// as the shape alone cannot tell them from a "-" in the name.
		{"a-b-1.0-py3-none-any.whl", []byte("x"), true},
		{"a-b-c-1.0-py3-none-any.whl", []byte("x"), false},
		{"a-b-1.0-2-py3-none-any.whl", []byte("x"), false},
		{"a_b-1.0.tar.gz", []byte("x"), true},
		{"ab.tar.gz", []byte("x"), true},
		{"a-b-1.0.tar.gz", []byte("x"), false},
	} {
		_, err := w.tryDeploy(w.c, "f/"+c.file, c.body, nil)
		if c.ok && err != nil {
			t.Errorf("%s: %v", c.file, err)
		}
		if !c.ok {
			wantRefused(t, c.file, err, http.StatusForbidden, fmt.Sprintf(namingRefused, c.file))
		}
	}
}

// With both on, a file must pass both, naming normalisation first: JFrog's
// combined table's jfrog-pypi-1.0.0.tar.gz named jfrog.pypi breaks both.
func TestEnforceBoth(t *testing.T) {
	w := newWorld(t, pypi.Settings{EnforceLayout: true, EnforceNamingNormalization: true})
	_, err := w.tryDeploy(w.c, "jfrog-pypi-1.0.0.tar.gz", sdistNamed("jfrog.pypi"), nil)
	wantRefused(
		t,
		"both broken",
		err,
		http.StatusForbidden,
		fmt.Sprintf(namingRefused, "jfrog-pypi-1.0.0.tar.gz"),
	)
	_, err = w.tryDeploy(w.c, "jfrog_pypi-1.0.0.tar.gz", sdistNamed("jfrog-pypi"), nil)
	wantRefused(t, "layout broken", err, http.StatusForbidden,
		fmt.Sprintf(layoutRefused, "jfrog_pypi-1.0.0.tar.gz", "jfrog-pypi", "1.0.0"))
	if _, err := w.tryDeploy(
		w.c,
		"jfrog_pypi-1.0.0.tar.gz",
		sdistNamed("jfrog_pypi"),
		nil,
	); err != nil {
		t.Errorf("both passed: %v", err)
	}
}

// The settings check uploads to local and federated repositories only: a
// copy or a move is not an upload, and files stored before a setting was
// turned on stay.
func TestEnforceScope(t *testing.T) {
	w := newWorld(t, pypi.Settings{})
	w.deploy("jfrog-pypi-1.0.0.tar.gz", sdistNamed("jfrog.pypi"), nil)
	w.h.Settings = pypi.Settings{EnforceLayout: true, EnforceNamingNormalization: true}
	w.transfer("jfrog-pypi-1.0.0.tar.gz", "copied/jfrog-pypi-1.0.0.tar.gz", false)
	w.transfer("jfrog-pypi-1.0.0.tar.gz", "moved/jfrog-pypi-1.0.0.tar.gz", true)
	fed := w.repo("pypi-fed", repo.Federated)
	_, err := w.tryDeploy(fed, "jfrog-pypi-1.0.0.tar.gz", sdistNamed("jfrog.pypi"), nil)
	wantRefused(
		t,
		"federated",
		err,
		http.StatusForbidden,
		fmt.Sprintf(namingRefused, "jfrog-pypi-1.0.0.tar.gz"),
	)
	remote := w.repo("pypi-remote", repo.Remote)
	if _, err := w.items.Deploy(w.ctx, item.Deploy{
		Target: item.Target{Repo: remote, Cached: true, Path: "packages/jfrog-pypi-1.0.0.tar.gz"},
		Body:   bytes.NewReader(sdistNamed("jfrog.pypi")), By: "tester",
	}); err != nil {
		t.Errorf("a remote's cache: %v", err)
	}
}

// packages/ and simple/ are reserved in every PyPI repository: a deploy
// there answers 400, and so does a copy or move into them, while a remote
// repository's cache is the server's own.
func TestReservedNamespaces(t *testing.T) {
	w := newWorld(t, pypi.Settings{})
	body := sdistNamed("x")
	for _, p := range []string{"packages/x-1.0.0.tar.gz", "simple/x/index.html", "packages", "simple"} {
		_, err := w.tryDeploy(w.c, p, body, nil)
		wantRefused(
			t,
			p,
			err,
			http.StatusBadRequest,
			"The repository 'pypi-local' rejected '"+p+"': packages/ and simple/ are reserved in a "+
				"PyPI repository.",
		)
	}
	for _, p := range []string{"Packages/x-1.0.0.tar.gz", "x/packages/x-1.0.0.tar.gz", "simplex/a"} {
		if _, err := w.tryDeploy(w.c, p, body, nil); err != nil {
			t.Errorf("%s: %v", p, err)
		}
	}
	w.now = w.now.Add(1)
	res, err := w.items.Transfer(w.ctx, item.Transfer{
		From: item.Target{
			Repo: w.c,
			Path: "x/packages",
		},
		To: item.Target{Repo: w.c, Path: "simple"},
		By: "tester",
	})
	if err != nil || res.Failed != 1 {
		t.Errorf("a copy into simple/: %v %+v", err, res)
	}
	remote := w.repo("pypi-remote", repo.Remote)
	if _, err := w.items.Deploy(w.ctx, item.Deploy{
		Target: item.Target{Repo: remote, Cached: true, Path: "packages/x-1.0.0.tar.gz"},
		Body:   bytes.NewReader(body), By: "tester",
	}); err != nil {
		t.Errorf("a remote's cache: %v", err)
	}
}

// The deploy policy of every package type comes first: a blacked-out
// repository's 404 before the reserved namespaces' 400.
func TestGenericPolicyFirst(t *testing.T) {
	w := newWorld(t, pypi.Settings{})
	blackedOut := true
	c := w.c
	c.Key, c.BlackedOut = "pypi-dark", &blackedOut
	if err := w.st.Repos().Create(w.ctx, c); err != nil {
		t.Fatal(err)
	}
	_, err := w.tryDeploy(c, "packages/x.tar.gz", sdistNamed("x"), nil)
	if r := refusal(err); r == nil || r.Status != http.StatusNotFound {
		t.Fatalf("%v", err)
	}
}
