package artifacts_test

import (
	"crypto/sha256"
	"encoding/base64"
	"encoding/json"
	"fmt"
	"slices"
	"strings"
	"time"

	"github.com/weaveplatform/artifactweave/internal/artifacts/api/repo"
	alpinefmt "github.com/weaveplatform/artifactweave/internal/artifacts/ecosystem/alpine"
	condafmt "github.com/weaveplatform/artifactweave/internal/artifacts/ecosystem/conda"
	cranfmt "github.com/weaveplatform/artifactweave/internal/artifacts/ecosystem/cran"
	debianfmt "github.com/weaveplatform/artifactweave/internal/artifacts/ecosystem/debian"
	opkgfmt "github.com/weaveplatform/artifactweave/internal/artifacts/ecosystem/opkg"
	rpmfmt "github.com/weaveplatform/artifactweave/internal/artifacts/ecosystem/rpm"
)

func adapterMetadataRows() []adapterConformance {
	return append([]adapterConformance{
		{repo.Alpine, adapterAlpine},
		{repo.RPM, adapterRPM},
		{repo.Opkg, adapterOpkg},
		{repo.Debian, adapterDebian},
		{repo.Cran, adapterCRAN},
		{repo.Conda, adapterConda},
	}, adapterRegistryRows()...)
}

// indexOwners verifies both union and whole-record ownership. Every source has
// one unique package and its own copy of shared; descriptions identify whose
// duplicate survived. Changing hosted order must change only shared's owner.
func (g *adapterGraph) indexOwners(observe func(string) []string) {
	g.t.Helper()
	want := func(key string, expected ...string) {
		g.t.Helper()
		got := observe(key)
		slices.Sort(got)
		slices.Sort(expected)
		if !slices.Equal(got, expected) {
			g.t.Fatalf("%s native index = %v; want %v", key, got, expected)
		}
	}
	for _, key := range []string{"local", "federated", "origin"} {
		want(key, key+"="+key, "shared="+key)
	}
	want("outer", "local=local", "federated=federated", "origin=origin", "shared=local")
	g.graph([]string{"origin", "inner", "local", "origin", "outer"},
		[]string{"federated", "outer", "local", "inner", "federated"})
	want("outer", "local=local", "federated=federated", "origin=origin", "shared=federated")
	g.graph([]string{"origin", "inner", "origin", "outer"}, []string{"outer", "inner"})
	want("outer", "origin=origin", "shared=origin")
}

func (g *adapterGraph) indexFiles(key string, files map[string][]byte) {
	g.t.Helper()
	for p, body := range files {
		if key == "origin" {
			g.upstream(p, body)
		} else {
			g.store(key, p, body)
		}
	}
}

func adapterAlpine(g *adapterGraph) {
	const index = "v3.20/main/x86_64/APKINDEX.tar.gz"
	for _, key := range []string{"local", "federated", "origin"} {
		var packages []alpinefmt.Package
		for _, name := range []string{"shared", key} {
			packages = append(
				packages,
				alpinefmt.Package{
					Name:         name,
					Version:      "1.0-r0",
					Architecture: "x86_64",
					Description:  key,
					ArchiveSize:  10,
					ControlSHA1:  base64.StdEncoding.EncodeToString(make([]byte, 20)),
				},
			)
		}
		body, err := alpinefmt.EncodeIndex(packages, "qualification", "", nil)
		if err != nil {
			g.t.Fatal(err)
		}
		g.indexFiles(key, map[string][]byte{index: body})
	}
	g.indexOwners(func(key string) []string {
		ps, err := alpinefmt.DecodeIndex(g.t.Context(), g.get("/artifactory/"+key+"/"+index), index)
		if err != nil {
			g.t.Fatal(err)
		}
		var out []string
		for _, p := range ps {
			for _, line := range strings.Split(p.IndexRaw, "\n") {
				if owner, ok := strings.CutPrefix(line, "T:"); ok {
					out = append(out, p.Name+"="+owner)
				}
			}
		}
		return out
	})
}

func adapterOpkg(g *adapterGraph) {
	const index = "feed/Packages"
	for _, key := range []string{"local", "federated", "origin"} {
		var ps []opkgfmt.Package
		for _, name := range []string{"shared", key} {
			ps = append(
				ps,
				opkgfmt.Package{
					Name:         name,
					Version:      "1.0",
					Architecture: "all",
					Description:  key,
					Path:         "feed/" + name + "_1.0_all.ipk",
					Size:         10,
					SHA256:       strings.Repeat("a", 64),
				},
			)
		}
		files, err := opkgfmt.EncodeIndex(ps, "feed")
		if err != nil {
			g.t.Fatal(err)
		}
		g.indexFiles(key, map[string][]byte{index: files["Packages"]})
	}
	g.indexOwners(func(key string) []string {
		ps, err := opkgfmt.DecodeIndex(g.get("/artifactory/"+key+"/"+index), index)
		if err != nil {
			g.t.Fatal(err)
		}
		var out []string
		for _, p := range ps {
			out = append(out, p.Name+"="+p.Description)
		}
		return out
	})
}

func adapterCRAN(g *adapterGraph) {
	const index = "src/contrib/PACKAGES"
	for _, key := range []string{"local", "federated", "origin"} {
		var ps []cranfmt.Package
		for _, name := range []string{"shared", key} {
			ps = append(
				ps,
				cranfmt.Package{Name: name, Version: "1.0", Feed: "src/contrib", Path: "src/contrib/" + name + "_1.0.tar.gz", Fields: map[string]string{"License": key}},
			)
		}
		files, err := cranfmt.EncodeIndex(ps, "src/contrib")
		if err != nil {
			g.t.Fatal(err)
		}
		g.indexFiles(key, map[string][]byte{index: files["PACKAGES"]})
	}
	g.indexOwners(func(key string) []string {
		ps, err := cranfmt.DecodeIndex(g.get("/artifactory/"+key+"/"+index), index)
		if err != nil {
			g.t.Fatal(err)
		}
		var out []string
		for _, p := range ps {
			out = append(out, p.Name+"="+p.Fields["License"])
		}
		return out
	})
}

func adapterConda(g *adapterGraph) {
	const index = "noarch/repodata.json"
	for _, key := range []string{"local", "federated", "origin"} {
		doc := condafmt.EmptyRepodata("noarch")
		for _, name := range []string{"shared", key} {
			doc.Packages[name+"-1.0-0.tar.bz2"] = condafmt.Record{Name: name, Version: "1.0", Build: "0", License: key, Size: 10, SHA256: strings.Repeat("a", 64)}
		}
		body, err := json.Marshal(doc)
		if err != nil {
			g.t.Fatal(err)
		}
		g.indexFiles(key, map[string][]byte{index: body})
	}
	g.indexOwners(func(key string) []string {
		id, _ := condafmt.ParseMetadataPath(index)
		doc, err := condafmt.DecodeRepodata(g.get("/artifactory/api/conda/"+key+"/"+index), id)
		if err != nil {
			g.t.Fatal(err)
		}
		var out []string
		for _, p := range doc.Packages {
			out = append(out, p.Name+"="+p.License)
		}
		return out
	})
}

func adapterRPM(g *adapterGraph) {
	for _, key := range []string{"local", "federated", "origin"} {
		var ps []rpmfmt.Package
		for _, name := range []string{"shared", key} {
			ps = append(
				ps,
				rpmfmt.Package{
					Name:         name,
					Version:      "1.0",
					Release:      "1",
					Architecture: "noarch",
					Description:  key,
					Path:         name + "-1.0-1.noarch.rpm",
					ArchiveSize:  10,
					SHA256:       strings.Repeat("a", 64),
				},
			)
		}
		metadata, err := rpmfmt.EncodeMetadata(ps, "", time.Unix(1, 0))
		if err != nil {
			g.t.Fatal(err)
		}
		g.indexFiles(key, metadata.Files)
	}
	g.indexOwners(func(key string) []string {
		prefix := "/artifactory/" + key + "/"
		p, sum, err := rpmfmt.DecodeRepomd(g.get(prefix+"repodata/repomd.xml"), "")
		if err != nil {
			g.t.Fatal(err)
		}
		ps, err := rpmfmt.DecodePrimary(g.get(prefix+p), "", sum)
		if err != nil {
			g.t.Fatal(err)
		}
		var out []string
		for _, p := range ps {
			out = append(out, p.Name+"="+p.Description)
		}
		return out
	})
}

func adapterDebian(g *adapterGraph) {
	const index = "dists/stable/main/binary-amd64/Packages"
	for _, key := range []string{"local", "federated", "origin"} {
		var body strings.Builder
		for _, name := range []string{"shared", key} {
			fmt.Fprintf(
				&body,
				"Package: %s\nVersion: 1.0\nArchitecture: amd64\nFilename: pool/%s_1.0_amd64.deb\nSize: 10\nSHA256: %s\nDescription: %s\n\n",
				name,
				name,
				strings.Repeat("a", 64),
				key,
			)
		}
		raw := []byte(body.String())
		release := []byte(fmt.Sprintf("Suite: stable\nArchitectures: amd64\nComponents: main\nSHA256:\n %x %d main/binary-amd64/Packages\n", sha256.Sum256(raw), len(raw)))
		g.indexFiles(key, map[string][]byte{index: raw, "dists/stable/Release": release})
	}
	g.indexOwners(func(key string) []string {
		ps, err := debianfmt.DecodePackagesIndex(g.t.Context(), g.get("/artifactory/"+key+"/"+index))
		if err != nil {
			g.t.Fatal(err)
		}
		var out []string
		for _, p := range ps {
			for _, line := range strings.Split(string(p.Raw), "\n") {
				if owner, ok := strings.CutPrefix(line, "Description: "); ok {
					out = append(out, p.Name+"="+owner)
				}
			}
		}
		return out
	})
}
