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
