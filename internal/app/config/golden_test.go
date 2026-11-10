package config

import (
	"flag"
	"os"
	"path/filepath"
	"testing"

	"github.com/google/go-cmp/cmp"
)

var update = flag.Bool("update", false, "rewrite the config validate golden files")

// TestUnitConfigStringGolden pins what config validate prints, byte for
// byte, for the defaults and for testdata/every-key.yaml. String marshals
// Config in field order, so a field added to Config, or one moved within it,
// reorders the summary that operators read and compare. Rewrite the golden
// files with -update only for a deliberate change to the file's shape.
func TestUnitConfigStringGolden(t *testing.T) {
	everyKey, err := Load(LoadOptions{File: filepath.Join("testdata", "every-key.yaml")})
	if err != nil {
		t.Fatal(err)
	}
	for name, cfg := range map[string]Config{
		"default":   Default(),
		"every-key": everyKey,
	} {
		t.Run(name, func(t *testing.T) {
			got := cfg.String()
			path := filepath.Join("testdata", name+".golden.yaml")
			if *update {
				if err := os.WriteFile(path, []byte(got), 0o600); err != nil {
					t.Fatal(err)
				}
			}
			want, err := os.ReadFile(path)
			if err != nil {
				t.Fatal(err)
			}
			if diff := cmp.Diff(string(want), got); diff != "" {
				t.Errorf("%s (-want +got):\n%s", path, diff)
			}
		})
	}
}
