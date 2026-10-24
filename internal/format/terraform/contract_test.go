package terraform_test

import (
	"archive/zip"
	"bytes"
	"crypto/sha256"
	"fmt"
	"testing"

	"github.com/weaveplatform/artifactweave/internal/format/formattest"
	"github.com/weaveplatform/artifactweave/pkg/repo"
)

// zipOf is a zip of files, in name order, the same bytes every time.
func zipOf(t testing.TB, files ...[2]string) []byte {
	t.Helper()
	var b bytes.Buffer
	z := zip.NewWriter(&b)
	for _, f := range files {
		w, err := z.Create(f[0])
		if err != nil {
			t.Fatal(err)
		}
		if _, err := w.Write([]byte(f[1])); err != nil {
			t.Fatal(err)
		}
	}
	if err := z.Close(); err != nil {
		t.Fatal(err)
	}
	return b.Bytes()
}

// typed is a local repository of the given terraformType.
func typed(tt repo.TerraformType) func(*repo.Config) {
	return func(c *repo.Config) { c.TerraformType = tt }
}

// A module is a package <ns>/<name>/<system> and each version links its
// zip. Version 1.0 is stored under both spellings, 1.0.0.zip and
// v1.0.0.zip, and the first holds the version, so every scenario also
// checks which spelling the index follows as files come and go; version
// 2.0 is stored as jf terraform publish names it, with a "v".
func TestUnitModuleContract(t *testing.T) {
	formattest.Run(t, formattest.Format{
		Type:  repo.Terraform,
		Local: typed(repo.TerraformModule),
		// A remote repository has no terraformType, so it indexes nothing
		// until P9 serves Terraform remotes.
		NoRemoteCache: true,
		Version: func(p, v string, n int) formattest.Version {
			pkg := "example/" + p + "/aws"
			body := func(name string) []byte {
				return zipOf(
					t,
					[2]string{"main.tf", fmt.Sprintf("# %s %s %s #%d\n", pkg, v, name, n)},
				)
			}
			if v == "2.0" {
				return formattest.Version{
					Package: pkg, Version: "2.0.0",
					Files: []formattest.File{{Path: pkg + "/v2.0.0.zip", Body: body("v2.0.0")}},
				}
			}
			return formattest.Version{
				Package: pkg, Version: "1.0.0",
				Files: []formattest.File{
					{Path: pkg + "/v1.0.0.zip", Body: body("v1.0.0")},
					{Path: pkg + "/1.0.0.zip", Body: body("1.0.0")},
				},
				Linked: []string{pkg + "/1.0.0.zip"},
			}
		},
	})
}

// A Terraform provider package is a package <ns>/<type>, in lower case,
// and each version links its archives, its SHA256SUMS, the signature and
// the manifest, which are ordinary version files (decision 0040). beta's
// namespace folder is stored as "Example", and version 2.0 sits below a
// folder of its own, as JFrog's deploy example puts one; HashiCorp's
// key-specific signature is stored and linked to nothing. The archives are
// zips, so each version's metadata carries their h1: hashes, which a
// reindex from empty recomputes.
func TestUnitProviderPackageContract(t *testing.T) {
	formattest.Run(t, formattest.Format{
		Type:          repo.Terraform,
		Local:         typed(repo.TerraformProvider),
		NoRemoteCache: true,
		Version:       providerRelease(t),
	})
}

// providerRelease is the contract's fixture of Terraform provider package
// releases.
func providerRelease(t *testing.T) func(p, v string, n int) formattest.Version {
	return func(p, v string, n int) formattest.Version {
		ns := map[string]string{"alpha": "example", "beta": "Example"}[p]
		ver := map[string]string{"1.0": "1.0.0", "2.0": "2.0.0"}[v]
		dir := ns + "/" + p + "/" + ver + "/"
		if v == "2.0" {
			dir = "mirror/" + dir
		}
		name := func(suffix string) string { return "terraform-provider-" + p + "_" + ver + "_" + suffix }
		archive := func(platform string) []byte {
			return zipOf(t,
				[2]string{"LICENSE", "MPL-2.0\n"},
				[2]string{
					"terraform-provider-" + p + "_v" + ver,
					fmt.Sprintf("binary %s %s %s #%d\n", p, ver, platform, n),
				},
			)
		}
		linux, darwin := archive("linux_amd64"), archive("darwin_arm64")
		sums := fmt.Appendf(nil, "%x  %s\n%x  %s\n",
			sha256.Sum256(darwin), name("darwin_arm64.zip"),
			sha256.Sum256(linux), name("linux_amd64.zip"))
		files := []formattest.File{
			{Path: dir + name("linux_amd64.zip"), Body: linux},
			{Path: dir + name("darwin_arm64.zip"), Body: darwin},
			{Path: dir + name("SHA256SUMS"), Body: sums},
			{Path: dir + name("SHA256SUMS.sig"), Body: append([]byte("signature of "), sums...)},
			{
				Path: dir + name("manifest.json"),
				Body: []byte(`{"version":1,"metadata":{"protocol_versions":["5.0"]}}`),
			},
		}
		linked := make([]string, 0, len(files))
		for _, f := range files {
			linked = append(linked, f.Path)
		}
		files = append(files, formattest.File{
			Path: dir + name("SHA256SUMS.72D7468F.sig"), Body: []byte("key-specific signature"),
		})
		return formattest.Version{
			Package: "example/" + p, Version: ver, Files: files, Linked: linked,
		}
	}
}
