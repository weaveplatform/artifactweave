package luarocks_test

import (
	"archive/zip"
	"bytes"
	"fmt"
	"testing"

	"github.com/weaveplatform/artifactweave/internal/format/formattest"
	"github.com/weaveplatform/artifactweave/internal/format/luarocks"
	"github.com/weaveplatform/artifactweave/pkg/repo"
)

func contractRock(t *testing.T, name, version string, n int) []byte {
	t.Helper()
	var b bytes.Buffer
	z := zip.NewWriter(&b)
	f, err := z.Create(name + "-" + version + ".rockspec")
	if err != nil {
		t.Fatal(err)
	}
	_, err = fmt.Fprintf(
		f,
		"package = %q\nversion = %q\ndescription = { summary = %q }\n",
		name,
		version,
		fmt.Sprintf("deploy %d", n),
	)
	if err != nil {
		t.Fatal(err)
	}
	if err := z.Close(); err != nil {
		t.Fatal(err)
	}
	return b.Bytes()
}

func TestUnitFormatContract(t *testing.T) {
	formattest.Run(t, formattest.Format{
		Type:          repo.LuaRocks,
		RepoWork:      []string{luarocks.KindManifest},
		NoRemoteCache: true,
		Version: func(p, v string, n int) formattest.Version {
			version := v + "-1"
			stem := p + "-" + version
			rockspec := stem + ".rockspec"
			return formattest.Version{
				Package: p, Version: version,
				Files: []formattest.File{
					{
						Path: rockspec,
						Body: []byte(
							fmt.Sprintf(
								"package = %q\nversion = %q\ndescription = { summary = %q }\n",
								p,
								version,
								fmt.Sprintf("deploy %d", n),
							),
						),
					},
					{
						Path:      rockspec + ".asc",
						Body:      []byte(fmt.Sprintf("signature %d", n)),
						SidecarOf: rockspec,
					},
					{Path: stem + ".src.rock", Body: contractRock(t, p, version, n)},
				},
				Linked: []string{rockspec, stem + ".src.rock"},
			}
		},
	})
}

func TestUnitPublicDottedPackageName(t *testing.T) {
	id, ok := luarocks.Parse("15puzzle.nvim-1.4.0-1.src.rock")
	if !ok || id.Name != "15puzzle.nvim" || id.Version != "1.4.0-1" || id.Arch != "src" {
		t.Fatalf("public package identity: %+v valid=%t", id, ok)
	}
}
