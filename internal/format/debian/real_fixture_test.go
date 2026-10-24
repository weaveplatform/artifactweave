package debian

import (
	"os"
	"path/filepath"
	"testing"
)

func TestDpkgBuiltPackagesMatchTheBinaryReader(t *testing.T) {
	for _, name := range []string{"alpha", "beta"} {
		for _, version := range []string{"1.0", "2.0"} {
			for n := range 2 {
				file := filepath.Join(
					"testdata",
					name+"_"+version+"_amd64_"+string(rune('0'+n))+".deb",
				)
				r, err := os.Open(file)
				if err != nil {
					t.Fatal(err)
				}
				info, err := r.Stat()
				if err != nil {
					_ = r.Close()
					t.Fatal(err)
				}
				got, err := ReadBinary(t.Context(), r, info.Size())
				_ = r.Close()
				if err != nil {
					t.Fatalf("dpkg fixture %s: %v", file, err)
				}
				if got.Name != name || got.Version != version || got.Architecture != "amd64" {
					t.Fatalf("dpkg fixture %s: %+v", file, got)
				}
			}
		}
	}
}
