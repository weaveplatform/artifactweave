package debian_test

import (
	"encoding/json"
	"net/http"
	"slices"
	"strings"
	"testing"

	"github.com/weaveplatform/artifactweave/internal/artifacts/api/repo"
	"github.com/weaveplatform/artifactweave/internal/artifacts/core/format"
	"github.com/weaveplatform/artifactweave/internal/artifacts/core/item"
	"github.com/weaveplatform/artifactweave/internal/artifacts/ecosystem/debian"
	"github.com/weaveplatform/artifactweave/internal/platform/testkit/repopath"
	"github.com/weaveplatform/artifactweave/internal/platform/work"
	"github.com/weaveplatform/artifactweave/test/server/support/harness"
)

// TestAccDebianMetadataValidationSetting (#1374): with the server's
// debian.metadata_validation on, 7.x's default ("Use Debian Metadata
// Validation", debian.metadata.validation), a .deb whose control data has
// an empty Maintainer is stored, marked with deb.index.status and left out
// of the Packages index, as before. With it off, the same package is
// indexed with its deb.name and deb.version, its empty field left out of
// its stanza [I]. A .deb that is not an ar archive is stored and marked
// either way, as no index can be read from it [I]. Every deploy answers
// 201 and every stored file is served as deployed.
func TestAccDebianMetadataValidationSetting(t *testing.T) {
	const coords = ";deb.distribution=stable;deb.component=main;deb.architecture=amd64"
	const (
		good    = "pool/main/a/alpha/alpha_1.0_amd64.deb"
		empty   = "pool/main/b/beta/beta_1.0_amd64.deb"
		garbage = "pool/main/g/garbage/garbage_1.0_amd64.deb"
	)
	bodies := map[string][]byte{
		good: repopath.Read(
			t,
			"internal/artifacts/ecosystem/debian/testdata/alpha_1.0_amd64_0.deb",
		),
		empty: debOf(t,
			xzOf(t, tarOf(t, "./control", "Package: beta\nVersion: 1.0\nArchitecture: amd64\n"+
				"Maintainer:\nDescription: beta binary\n")),
			xzOf(t, tarOf(t, "./usr/bin/beta", "beta"))),
		garbage: []byte("not a debian package"),
	}
	for _, tc := range []struct {
		name     string
		settings debian.Settings
		props    map[string]map[string][]string
		indexed  []string
	}{
		{
			name: "on",
			props: map[string]map[string][]string{
				empty: {"deb.index.status": {
					"debian: invalid binary package: malformed or duplicate control field",
				}},
				garbage: {"deb.index.status": {"debian: invalid binary package: ar magic"}},
			},
			indexed: []string{good},
		},
		{
			name:     "off",
			settings: debian.Settings{DisableMetadataValidation: true},
			props: map[string]map[string][]string{
				empty:   {"deb.name": {"beta"}, "deb.version": {"1.0"}},
				garbage: {"deb.index.status": {"debian: invalid binary package: ar magic"}},
			},
			indexed: []string{good, empty},
		},
	} {
		t.Run(tc.name, func(t *testing.T) {
			w := newItemWorld(t, false, func(cfg *harness.Config) {
				cfg.FormatSettings.Debian = tc.settings
			})
			c := repo.Config{Key: "deb-local", Class: repo.Local, PackageType: repo.Debian}
			if err := w.db.Repos().Create(t.Context(), *repo.Defaults(&c)); err != nil {
				t.Fatal(err)
			}
			for _, p := range []string{good, empty, garbage} {
				harness.WantStatus(
					t,
					w.do(
						http.MethodPut,
						"/artifactory/deb-local/"+p+coords,
						"admin",
						string(bodies[p]),
					),
					http.StatusCreated,
				)
				r := w.do(http.MethodGet, "/artifactory/deb-local/"+p, "admin", "")
				if r.Code != http.StatusOK || r.Body.String() != string(bodies[p]) {
					t.Errorf("GET %s answered %d, want 200 and the deployed bytes", p, r.Code)
				}
			}
			for p, want := range tc.props {
				r := w.do(http.MethodGet, "/artifactory/api/storage/deb-local/"+p+"?properties",
					"admin", "")
				var got struct {
					Properties map[string][]string `json:"properties"`
				}
				if r.Code != http.StatusOK || json.Unmarshal(r.Body.Bytes(), &got) != nil {
					t.Fatalf("properties of %s answered %d %s", p, r.Code, r.Body)
				}
				for _, key := range []string{"deb.name", "deb.version", "deb.index.status"} {
					if !slices.Equal(got.Properties[key], want[key]) {
						t.Errorf("%s %s = %q, want %q", p, key, got.Properties[key], want[key])
					}
				}
			}
			h := debian.Handler{Settings: tc.settings}
			items := &item.Service{
				Store: w.db, Blobs: w.blobs,
				Formats: func(repo.PackageType) format.Handler { return h },
			}
			if err := h.Work(t.Context(), items.Env(),
				work.WorkItem{Kind: debian.KindIndex, Key: c.Key}); err != nil {
				t.Fatal(err)
			}
			r := w.do(
				http.MethodGet,
				"/artifactory/deb-local/dists/stable/main/binary-amd64/Packages",
				"admin",
				"",
			)
			if r.Code != http.StatusOK {
				t.Fatalf("Packages answered %d %s", r.Code, r.Body)
			}
			index := r.Body.String()
			var listed []string
			for _, line := range strings.Split(index, "\n") {
				if name, ok := strings.CutPrefix(line, "Filename: "); ok {
					listed = append(listed, name)
				}
			}
			slices.Sort(listed)
			if !slices.Equal(listed, tc.indexed) {
				t.Errorf("Packages lists %q, want %q", listed, tc.indexed)
			}
			if tc.name == "off" && (!strings.Contains(index,
				"Package: beta\nVersion: 1.0\nArchitecture: amd64\nDescription: beta binary\n") ||
				strings.Contains(index, "Maintainer:\n")) {
				t.Errorf("Packages %q does not list beta without its empty Maintainer", index)
			}
		})
	}
}
