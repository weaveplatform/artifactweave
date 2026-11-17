package maven_test

import (
	"context"
	"errors"
	"testing"
	"time"

	"github.com/weaveplatform/artifactweave/internal/artifacts/api/repo"
	"github.com/weaveplatform/artifactweave/internal/artifacts/core/format"
	"github.com/weaveplatform/artifactweave/internal/artifacts/core/layout"
	"github.com/weaveplatform/artifactweave/internal/artifacts/ecosystem/maven"
)

func TestUnitMavenVirtualSelectionMode(t *testing.T) {
	w := newWorld(t, nil)
	for _, tc := range []struct {
		name, layout, path string
		selected, snapshot bool
	}{
		{"valid token remains first hit", "gradle-default", "org.acme/app/1.0-[INTEGRATION]/app-1.0-[INTEGRATION].jar", false, false},
		{"plain", repo.LayoutMaven2, "org/acme/app/1.0/app-1.0.jar", false, false},
		{"snapshot", repo.LayoutMaven2, "org/acme/app/1.0-SNAPSHOT/app-1.0-SNAPSHOT.jar", true, true},
		{"invalid snapshot fallback", repo.LayoutMaven1, "org/acme/app/1.0-SNAPSHOT/app-1.0-SNAPSHOT.jar", true, true},
		{"invalid integration fallback", repo.LayoutMaven1, "app/[INTEGRATION]/app.jar", true, true},
		{"valid nonintegration", "simple-default", "org/acme/app/app-1.0-SNAPSHOT.jar", false, false},
		{"release", repo.LayoutMaven2, "org/acme/app/[RELEASE]/app-[RELEASE].jar", true, false},
		{"invalid release", repo.LayoutMaven1, "app/[RELEASE]/app.jar", true, false},
	} {
		t.Run(tc.name, func(t *testing.T) {
			c := w.c
			c.Class = repo.Virtual
			c.RepoLayoutRef = tc.layout
			s, err := w.items.SelectVirtual(t.Context(), c, tc.path)
			if err != nil || (s != nil) != tc.selected {
				t.Fatal(s, err)
			}
			if s != nil && s.DirectRemotes != tc.snapshot {
				t.Fatal(s)
			}
			if s != nil {
				m := repo.Config{Key: "member", HandleSnapshots: new(false), HandleReleases: new(false)}
				skip := s.Skip(m)
				if tc.name == "invalid release" {
					if len(skip) != 0 {
						t.Fatal(skip)
					}
				} else if len(skip) == 0 {
					t.Fatal("policy not skipped")
				}
			}
		})
	}
}

func TestUnitMavenVirtualSnapshotChoices(t *testing.T) {
	w := newWorld(t, nil)
	c := w.c
	c.Class = repo.Virtual
	path := "org/acme/app/1.0-SNAPSHOT/app-1.0-SNAPSHOT.jar"
	for _, unique := range []bool{false, true} {
		asked := path
		if unique {
			asked = "org/acme/app/1.0-SNAPSHOT/app-1.0-20261117.120000-1.jar"
		}
		s, err := w.items.SelectVirtual(t.Context(), c, asked)
		if err != nil {
			t.Fatal(err)
		}
		older := format.VirtualCandidate{Path: "old", Modified: time.Unix(100, 0)}
		newer := format.VirtualCandidate{Path: "new", Modified: time.Unix(200, 0)}
		for _, tc := range []struct {
			name       string
			current    format.VirtualCandidate
			kept       *format.VirtualCandidate
			take, stop bool
		}{
			{"first", older, nil, true, false},
			{"newer", newer, &older, true, false},
			{"older", older, &newer, false, false},
			{"equal", older, &older, false, false},
			{"first exact", format.VirtualCandidate{Exact: true, Modified: older.Modified}, &newer, true, unique},
			{"later nonexact", newer, &format.VirtualCandidate{Exact: true, Modified: older.Modified}, true, false},
		} {
			t.Run(tc.name, func(t *testing.T) {
				choice, err := s.Choose(t.Context(), tc.current, tc.kept)
				if err != nil || choice.Take != tc.take || choice.Stop != tc.stop {
					t.Fatal(choice, err)
				}
				if s.Returning(tc.current) == "" {
					t.Fatal("empty trace")
				}
			})
		}
	}
}

func TestUnitMavenVirtualReleaseChoices(t *testing.T) {
	w := newWorld(t, nil)
	c := w.c
	c.Class = repo.Virtual
	s, err := w.items.SelectVirtual(t.Context(), c, "org/acme/app/[RELEASE]/app-[RELEASE].jar")
	if err != nil {
		t.Fatal(err)
	}
	candidate := func(version string, exact bool) format.VirtualCandidate {
		return format.VirtualCandidate{Repo: w.c, Path: "org/acme/app/" + version + "/app-" + version + ".jar", Exact: exact}
	}
	for _, tc := range []struct {
		name          string
		current, kept format.VirtualCandidate
		take          bool
	}{
		{"numeric", candidate("1.10", false), candidate("1.9", false), true},
		{"older", candidate("1.9", false), candidate("1.10", false), false},
		{"equal", candidate("1.9", false), candidate("1.9", false), false},
		{"first exact", candidate("1.0", true), candidate("2.0", false), true},
		{"newer nonexact", candidate("2.0", false), candidate("1.0", true), true},
	} {
		t.Run(tc.name, func(t *testing.T) {
			choice, err := s.Choose(t.Context(), tc.current, &tc.kept)
			if err != nil || choice.Take != tc.take || choice.Stop {
				t.Fatal(choice, err)
			}
			if s.Returning(tc.current) == "" {
				t.Fatal("empty trace")
			}
		})
	}
}

type selectionLayouts struct{ err error }

func (s selectionLayouts) Layout(_ context.Context, name string) (*layout.Layout, error) {
	if name == "failed" {
		return nil, errSelectionLayout
	}
	if s.err != nil {
		return nil, s.err
	}
	return layout.Lookup(name)
}

var errSelectionLayout = errors.New("layout unavailable")

func TestUnitMavenVirtualSelectionLayoutFailures(t *testing.T) {
	h := maven.Handler{}
	c := repo.Config{Key: "virtual", Class: repo.Virtual, PackageType: repo.Maven, RepoLayoutRef: repo.LayoutMaven2}
	env := format.Env{Layouts: selectionLayouts{err: errSelectionLayout}}
	if _, err := h.SelectVirtual(t.Context(), env, c, "org/acme/app/[RELEASE]/app-[RELEASE].jar"); !errors.Is(err, errSelectionLayout) {
		t.Fatal(err)
	}
	env.Layouts = selectionLayouts{}
	s, err := h.SelectVirtual(t.Context(), env, c, "org/acme/app/[RELEASE]/app-[RELEASE].jar")
	if err != nil {
		t.Fatal(err)
	}
	// A member descriptor can fail while comparing, after mode selection.
	c.RepoLayoutRef = "failed"
	current := format.VirtualCandidate{Repo: c, Path: "org/acme/app/1.0/app-1.0.jar"}
	if _, err = s.Choose(t.Context(), current, &current); err == nil {
		t.Fatal("missing candidate layout accepted")
	}
	valid := current
	valid.Repo.RepoLayoutRef = repo.LayoutMaven2
	if _, err = s.Choose(t.Context(), valid, &current); err == nil {
		t.Fatal("missing kept layout accepted")
	}
}
