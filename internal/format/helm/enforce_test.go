package helm_test

import (
	"errors"
	"net/http"
	"strings"
	"testing"

	"github.com/weaveplatform/artifactweave/internal/format"
	"github.com/weaveplatform/artifactweave/internal/format/helm"
	"github.com/weaveplatform/artifactweave/pkg/repo"
	"github.com/weaveplatform/artifactweave/pkg/types"
)

// enforcing is a repository with both Enforce Layout fields set as given.
func (w *world) enforcing(key string, names, unique bool) repo.Config {
	w.t.Helper()
	return w.repo(key, func(c *repo.Config) {
		c.ForceMetadataNameVersion, c.ForceNonDuplicateChart = &names, &unique
	})
}

// refusedWith checks that err is a 403 Rejection with message.
func refusedWith(t *testing.T, err error, message string) {
	t.Helper()
	var r *format.Rejection
	if !errors.As(err, &r) || r.Status != http.StatusForbidden || r.Message != message {
		t.Fatalf("got %v, want 403 %q", err, message)
	}
}

const policy = "This action is prevented due to the Enforce Layout Policy, "

// Enforce Chart Name and Version refuses each kind of chart 7.x's
// documentation shows with its message, and the first check a chart fails
// answers.
func TestEnforceNameAndVersion(t *testing.T) {
	w := newWorld(t)
	c := w.enforcing("helm-names", true, false)
	for _, k := range []struct {
		path string
		body []byte
		want string
	}{
		{
			"chart-1.0.0.tgz", []byte("not a chart"),
			policy + "the metadata of the package chart-1.0.0 could not be read or is malformed.",
		},
		{
			"chart-1.0.0.tar.gz", tgz(file{name: "chart/Chart.yaml", body: "name: chart\n"}),
			policy + "the metadata of the package chart-1.0.0 could not be read or is malformed.",
		},
		{
			"chart-1.0.0.0.tgz", chartTgz("chart", "1.0.0.0"),
			policy + "the specified package version chart-1.0.0.0 does not comply with the " +
				"Semantic Versioning format.",
		},
		// Not SemVer comes before the file name, which also differs here.
		{
			"chart-1.0.tgz", chartTgz("chart", "v1.0.0"),
			policy + "the specified package version chart-v1.0.0 does not comply with the " +
				"Semantic Versioning format.",
		},
		{
			"chart-1.0.0.tgz", chartTgz("chart", "2.0.0"),
			policy + "the package file chart-1.0.0 does not match the package name/version " +
				"chart:2.0.0 in the metadata file.",
		},
		{
			"dir/other-1.0.0.tgz", chartTgz("chart", "1.0.0"),
			policy + "the package file other-1.0.0 does not match the package name/version " +
				"chart:1.0.0 in the metadata file.",
		},
	} {
		_, err := w.deployTo(c, k.path, k.body)
		refusedWith(t, err, k.want)
	}
	// A chart named as Chart.yaml names it, in any folder and with either
	// extension, and every other file, pass.
	for p, body := range map[string][]byte{
		"a/b/chart-1.0.0-rc.1+build.tgz": chartTgz("chart", "1.0.0-rc.1+build"),
		"chart-2.0.0.tar.gz":             chartTgz("chart", "2.0.0"),
		"chart-1.0.0.tgz.prov":           []byte("signed"),
		"notes.txt":                      []byte("text"),
	} {
		if _, err := w.deployTo(c, p, body); err != nil {
			t.Fatalf("%s: %v", p, err)
		}
	}
	if got := w.index(c)["chart"]; len(got) != 2 {
		t.Fatal(got)
	}
}

// Prevent Duplicate Chart Paths refuses a chart whose name and version a
// chart at another path already has; an overwrite at the same path passes,
// as does a move of the chart itself. Without it a duplicate is stored and
// indexed (TestDuplicatesNewestWins).
func TestEnforceNoDuplicates(t *testing.T) {
	w := newWorld(t)
	c := w.enforcing("helm-unique", false, true)
	if _, err := w.deployTo(c, "a/web-1.0.0.tgz", chartTgz("web", "1.0.0")); err != nil {
		t.Fatal(err)
	}
	_, err := w.deployTo(c, "b/whatever.tgz", chartTgz("web", "1.0.0"))
	refusedWith(t, err, policy+"a package with the same name and version web-1.0.0 already exists "+
		"in the repository.")
	for p, body := range map[string][]byte{
		"a/web-1.0.0.tgz": chartTgz("web", "1.0.0", "description: again"),
		"b/web-2.0.0.tgz": chartTgz("web", "2.0.0"),
		"b/junk.tgz":      []byte("no chart"),
	} {
		if _, err := w.deployTo(c, p, body); err != nil {
			t.Fatalf("%s: %v", p, err)
		}
	}
	// A move within the repository is no duplicate of its own source; a
	// copy is.
	if res := w.transfer(c, "a/web-1.0.0.tgz", c, "moved/web-1.0.0.tgz", true); res.Failed > 0 {
		t.Fatal(res.Messages)
	}
	res := w.transfer(c, "moved/web-1.0.0.tgz", c, "copied/web-1.0.0.tgz", false)
	if res.Failed == 0 ||
		!strings.Contains(res.Messages[0].Text, "already exists in the repository") {
		t.Fatal(res.Messages)
	}
}

// A copy or a move into an enforcing repository is refused for each chart
// that breaks it, and reported, as 7.x warns it may skip such charts.
func TestEnforceTransfers(t *testing.T) {
	w := newWorld(t)
	c := w.enforcing("helm-both", true, true)
	w.deploy("x/web-1.0.0.tgz", chartTgz("web", "1.0.0"))
	w.deploy("x/misnamed.tgz", chartTgz("web", "2.0.0"))
	res := w.transfer(w.c, "x", c, "x", false)
	if res.Failed == 0 || res.Files == 0 {
		t.Fatalf("%+v", res)
	}
	found := false
	for _, m := range res.Messages {
		found = found ||
			strings.Contains(m.Text, "does not match the package name/version web:2.0.0")
	}
	if !found {
		t.Fatal(res.Messages)
	}
	if got := w.index(c)["web"]; len(got) != 1 {
		t.Fatal(got)
	}
}

// Admit passes what Enforce Layout does not look at, refuses with the
// policy every package type applies first, and reads the content itself
// when nothing inspected it.
func TestAdmit(t *testing.T) {
	w := newWorld(t)
	c := w.enforcing("helm-admit", true, true)
	h := helm.Handler{}
	it := w.deploy("misnamed.tgz", chartTgz("web", "1.0.0"))
	env := w.items.Env()
	for _, k := range []format.Write{
		{Op: format.OpChecksum, Path: "misnamed.tgz", Checksums: it.Actual},
		{Op: format.OpDeploy, Path: "misnamed.tgz", Cached: true, Checksums: it.Actual},
		{Op: format.OpDeploy, Path: "misnamed.tgz.prov", Checksums: it.Actual},
	} {
		if _, err := h.Admit(w.ctx, env, c, k); err != nil {
			t.Fatalf("%+v: %v", k, err)
		}
	}
	_, err := h.Admit(w.ctx, env, c, format.Write{
		Op: format.OpDeploy, Path: "misnamed.tgz", Checksums: it.Actual,
	})
	refusedWith(t, err, policy+"the package file misnamed does not match the package "+
		"name/version web:1.0.0 in the metadata file.")
	// Neither field set: nothing is read.
	if _, err := h.Admit(
		w.ctx,
		env,
		w.c,
		format.Write{Op: format.OpDeploy, Path: "x.tgz"},
	); err != nil {
		t.Fatal(err)
	}
	off := true
	blackedOut := w.repo("helm-off", func(c *repo.Config) { c.BlackedOut = &off })
	_, err = h.Admit(w.ctx, env, blackedOut, format.Write{Op: format.OpDeploy, Path: "x.tgz"})
	var r *format.Rejection
	if !errors.As(err, &r) || r.Status != http.StatusNotFound {
		t.Fatal(err)
	}
	// Content the store cannot open is a failure, not a refusal.
	_, err = h.Admit(w.ctx, env, c, format.Write{
		Op: format.OpDeploy, Path: "x.tgz", Checksums: types.Checksums{SHA256: "missing"},
	})
	if err == nil || errors.As(err, &r) {
		t.Fatal(err)
	}
}
