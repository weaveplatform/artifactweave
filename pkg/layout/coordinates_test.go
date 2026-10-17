package layout_test

import (
	"testing"

	"github.com/weaveplatform/artifactweave/pkg/layout"
)

func TestRead(t *testing.T) {
	l := mustLookup(t, "maven-2-default")
	tests := []struct {
		path          string
		ok            bool
		want          layout.Coordinates
		version, file string
		integration   bool
	}{
		{
			path: "org/acme/lib/1.0/lib-1.0-sources.jar", ok: true,
			want: layout.Coordinates{
				Org: "org.acme", Module: "lib", BaseRev: "1.0", Classifier: "sources", Ext: "jar",
			},
			version: "1.0", file: "1.0",
		},
		{
			// The descriptor pattern spells .pom, so the extension comes from
			// the name.
			path: "org/acme/lib/1.0/lib-1.0.pom", ok: true,
			want:    layout.Coordinates{Org: "org.acme", Module: "lib", BaseRev: "1.0", Ext: "pom"},
			version: "1.0", file: "1.0",
		},
		{
			path: "org/acme/lib/1.0-SNAPSHOT/lib-1.0-20260101.120000-1.jar", ok: true,
			want: layout.Coordinates{
				Org: "org.acme", Module: "lib", BaseRev: "1.0", FolderItegRev: "SNAPSHOT",
				FileItegRev: "20260101.120000-1", Ext: "jar",
			},
			version: "1.0-SNAPSHOT", file: "1.0-20260101.120000-1", integration: true,
		},
		{path: "lib.jar"},
	}
	for _, tt := range tests {
		got, ok := l.Read(tt.path)
		if ok != tt.ok || got != tt.want {
			t.Errorf("Read(%s) = %+v, %v; want %+v, %v", tt.path, got, ok, tt.want, tt.ok)
			continue
		}
		if got.Version() != tt.version || got.FileVersion() != tt.file ||
			got.Integration() != tt.integration {
			t.Errorf("%s: version %q, file version %q, integration %v", tt.path,
				got.Version(), got.FileVersion(), got.Integration())
		}
	}
	// A folder integration revision alone names the file's version too.
	c := layout.Coordinates{BaseRev: "2.0", FolderItegRev: "SNAPSHOT"}
	if c.FileVersion() != "2.0-SNAPSHOT" {
		t.Errorf("file version %q", c.FileVersion())
	}
}

// A request path with a version token reads as Artifactory reads it: under
// the plain layout gradle-default's lazy base revision takes in
// -[INTEGRATION]; with the tokens it is the integration revision.
func TestVersionTokens(t *testing.T) {
	tests := []struct {
		layout, path string
		plain, tok   layout.Coordinates
	}{
		{
			layout: "gradle-default",
			path:   "org.acme/lib/1.0-[INTEGRATION]/lib-1.0-[INTEGRATION].jar",
			plain: layout.Coordinates{
				Org:     "org.acme",
				Module:  "lib",
				BaseRev: "1.0-[INTEGRATION]",
				Ext:     "jar",
			},
			tok: layout.Coordinates{
				Org: "org.acme", Module: "lib", BaseRev: "1.0",
				FolderItegRev: "[INTEGRATION]", FileItegRev: "[INTEGRATION]", Ext: "jar",
			},
		},
		{
			layout: "maven-2-default",
			path:   "org/acme/lib/[RELEASE]/lib-[RELEASE]-sources.jar",
			plain: layout.Coordinates{
				Org:        "org.acme",
				Module:     "lib",
				BaseRev:    "[RELEASE]",
				Classifier: "sources",
				Ext:        "jar",
			},
			tok: layout.Coordinates{
				Org:        "org.acme",
				Module:     "lib",
				BaseRev:    "[RELEASE]",
				Classifier: "sources",
				Ext:        "jar",
			},
		},
		{
			// An empty integration revision expression is Artifactory's
			// permissive one, with the tokens beside it.
			layout: "empty-revisions",
			path:   "acme/lib/1.0-[INTEGRATION]/lib-1.0-[INTEGRATION].jar",
			plain: layout.Coordinates{
				Org:           "acme",
				Module:        "lib",
				BaseRev:       "1.0",
				FolderItegRev: "[INTEGRATION]",
				FileItegRev:   "[INTEGRATION]",
				Ext:           "jar",
			},
			tok: layout.Coordinates{
				Org:           "acme",
				Module:        "lib",
				BaseRev:       "1.0",
				FolderItegRev: "[INTEGRATION]",
				FileItegRev:   "[INTEGRATION]",
				Ext:           "jar",
			},
		},
	}
	for _, tc := range tests {
		t.Run(tc.layout, func(t *testing.T) {
			var l *layout.Layout
			if tc.layout == "empty-revisions" {
				var err error
				l, err = layout.Compile(layout.Definition{
					Name:                "empty-revisions",
					ArtifactPathPattern: "[org]/[module]/[baseRev](-[folderItegRev])/[module]-[baseRev](-[fileItegRev]).[ext]",
				})
				if err != nil {
					t.Fatal(err)
				}
			} else {
				l = mustLookup(t, tc.layout)
			}
			if got, ok := l.Read(tc.path); !ok || got != tc.plain {
				t.Errorf("plain read %+v, %v; want %+v", got, ok, tc.plain)
			}
			tl, err := l.VersionTokens()
			if err != nil {
				t.Fatal(err)
			}
			if got, ok := tl.Read(tc.path); !ok || got != tc.tok {
				t.Errorf("token read %+v, %v; want %+v", got, ok, tc.tok)
			}
			if tl.Definition() != l.Definition() {
				t.Errorf("definition %+v, want the layout's own", tl.Definition())
			}
		})
	}
}
