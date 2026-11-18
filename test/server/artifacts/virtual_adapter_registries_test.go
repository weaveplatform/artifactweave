package artifacts_test

import (
	"archive/tar"
	"bytes"
	"compress/gzip"
	"encoding/json"
	"fmt"
	"io"
	"slices"
	"strings"

	"github.com/weaveplatform/artifactweave/internal/artifacts/api/repo"
	"github.com/weaveplatform/artifactweave/internal/artifacts/core/model"
	ansiblefmt "github.com/weaveplatform/artifactweave/internal/artifacts/ecosystem/ansible"
	bowerfmt "github.com/weaveplatform/artifactweave/internal/artifacts/ecosystem/bower"
	cheffmt "github.com/weaveplatform/artifactweave/internal/artifacts/ecosystem/chef"
	podfmt "github.com/weaveplatform/artifactweave/internal/artifacts/ecosystem/cocoapods"
	pubfmt "github.com/weaveplatform/artifactweave/internal/artifacts/ecosystem/pub"
	puppetfmt "github.com/weaveplatform/artifactweave/internal/artifacts/ecosystem/puppet"
)

func adapterRegistryRows() []adapterConformance {
	return append([]adapterConformance{
		{repo.Pub, adapterPub},
		{repo.Ansible, adapterAnsible},
		{repo.Chef, adapterChef},
		{repo.Puppet, adapterPuppet},
		{repo.Bower, adapterBower},
		{repo.CocoaPods, adapterPods},
	}, adapterSpecialRows()...)
}

func adapterVersions(key string) []string {
	unique := map[string]string{"local": "2.0.0", "federated": "3.0.0", "origin": "4.0.0"}
	return []string{"1.0.0", unique[key]}
}

func (g *adapterGraph) version(key, name, version string, metadata any) {
	g.t.Helper()
	st := g.w.Runtime.Store.Packages()
	p, err := st.Ensure(g.t.Context(), model.Package{RepoKey: key, Type: g.typ, Name: name})
	if err != nil {
		g.t.Fatal(err)
	}
	body, err := json.Marshal(metadata)
	if err != nil {
		g.t.Fatal(err)
	}
	if _, err := st.PutVersion(g.t.Context(), model.PackageVersion{PackageID: p.ID, Version: version, Metadata: body}); err != nil {
		g.t.Fatal(err)
	}
}

func (g *adapterGraph) decode(route string, out any) {
	g.t.Helper()
	if err := json.Unmarshal(g.get(route), out); err != nil {
		g.t.Fatal(err)
	}
}

func (g *adapterGraph) releaseOwners(observe func(string) []string) {
	g.t.Helper()
	want := func(key string, expected ...string) {
		g.t.Helper()
		got := observe(key)
		slices.Sort(got)
		slices.Sort(expected)
		if !slices.Equal(got, expected) {
			g.t.Fatalf("%s native releases = %v; want %v", key, got, expected)
		}
	}
	for _, key := range []string{"local", "federated", "origin"} {
		vs := adapterVersions(key)
		want(key, vs[0]+"="+key, vs[1]+"="+key)
	}
	want("outer", "1.0.0=local", "2.0.0=local", "3.0.0=federated", "4.0.0=origin")
	g.graph([]string{"origin", "inner", "local", "origin", "outer"}, []string{"federated", "outer", "local", "inner", "federated"})
	want("outer", "1.0.0=federated", "2.0.0=local", "3.0.0=federated", "4.0.0=origin")
	g.graph([]string{"origin", "inner", "origin", "outer"}, []string{"outer", "inner"})
	want("outer", "1.0.0=origin", "4.0.0=origin")
}

func adapterPub(g *adapterGraph) {
	for _, key := range []string{"local", "federated", "origin"} {
		var versions []map[string]any
		for _, v := range adapterVersions(key) {
			spec := map[string]any{"name": "demo", "version": v, "description": key}
			if key == "origin" {
				versions = append(versions, map[string]any{"version": v, "pubspec": spec, "archive_url": g.w.Upstream.URL + "/packages/demo/versions/" + v + ".tar.gz"})
			} else {
				p := pubfmt.Path("demo", v)
				it := g.store(key, p, []byte(key+v))
				g.version(key, "demo", v, pubfmt.Metadata{Name: "demo", Version: v, Pubspec: spec, Path: p, SHA256: it.Actual.SHA256})
			}
		}
		if key == "origin" {
			body, _ := json.Marshal(map[string]any{"name": "demo", "versions": versions})
			g.upstream("api/packages/demo", body)
		}
	}
	g.releaseOwners(func(key string) []string {
		var doc struct {
			Versions []struct {
				Version string `json:"version"`
				Pubspec struct {
					Description string `json:"description"`
				} `json:"pubspec"`
			} `json:"versions"`
		}
		g.decode("/artifactory/api/pub/"+key+"/api/packages/demo", &doc)
		var out []string
		for _, v := range doc.Versions {
			out = append(out, v.Version+"="+v.Pubspec.Description)
		}
		return out
	})
}

func adapterAnsible(g *adapterGraph) {
	for _, key := range []string{"local", "federated", "origin"} {
		var versions []map[string]string
		for _, v := range adapterVersions(key) {
			if key == "origin" {
				versions = append(versions, map[string]string{"version": v})
				g.upstream(
					"api/v3/collections/acme/demo/versions/"+v+"/",
					[]byte(
						fmt.Sprintf(
							`{"namespace":{"name":"acme"},"collection":{"name":"demo"},"version":"%s","metadata":{"description":"origin","dependencies":{}},"artifact":{"filename":"acme-demo-%s.tar.gz","sha256":"%s","size":10},"download_url":"%s/archive-%s.tar.gz"}`,
							v,
							v,
							strings.Repeat("a", 64),
							g.w.Upstream.URL,
							v,
						),
					),
				)
			} else {
				it := g.store(key, ansiblefmt.Path("acme", "demo", v), []byte(key+v))
				g.version(
					key,
					"acme.demo",
					v,
					ansiblefmt.Metadata{Namespace: "acme", Name: "demo", Version: v, Description: key, Dependencies: map[string]string{}, SHA256: it.Actual.SHA256},
				)
			}
		}
		if key == "origin" {
			body, _ := json.Marshal(map[string]any{"data": versions, "links": map[string]any{"next": nil}})
			g.upstream("api/v3/collections/acme/demo/versions/?limit=100&offset=0", body)
		}
	}
	g.releaseOwners(func(key string) []string {
		base := "/artifactory/api/ansible/" + key + "/api/v3/collections/acme/demo/versions/"
		var list struct {
			Data []struct {
				Version string `json:"version"`
			} `json:"data"`
		}
		g.decode(base, &list)
		var out []string
		for _, v := range list.Data {
			var doc struct {
				Metadata struct {
					Description string `json:"description"`
				} `json:"metadata"`
			}
			g.decode(base+v.Version+"/", &doc)
			out = append(out, v.Version+"="+doc.Metadata.Description)
		}
		return out
	})
}

func adapterChef(g *adapterGraph) {
	for _, key := range []string{"local", "federated", "origin"} {
		var versions []string
		for _, v := range adapterVersions(key) {
			if key == "origin" {
				url := g.w.Upstream.URL + "/api/v1/cookbooks/demo/versions/" + strings.ReplaceAll(v, ".", "_")
				versions = append(versions, url)
				g.upstream(
					strings.TrimPrefix(url, g.w.Upstream.URL),
					[]byte(
						fmt.Sprintf(
							`{"version":"%s","file":"%s/demo-%s.tgz","cookbook":"%s/api/v1/cookbooks/demo","tarball_file_size":10,"sha256":"%s","license":"origin","dependencies":{},"platforms":{}}`,
							v,
							g.w.Upstream.URL,
							v,
							g.w.Upstream.URL,
							strings.Repeat("a", 64),
						),
					),
				)
			} else {
				p := cheffmt.Path("demo", v)
				it := g.store(key, p, []byte(key+v))
				g.version(
					key,
					"demo",
					v,
					cheffmt.Metadata{
						Name:     "demo",
						Version:  v,
						Path:     p,
						SHA256:   it.Actual.SHA256,
						Size:     it.Size,
						Manifest: cheffmt.Manifest{Name: "demo", Version: v, License: key, Dependencies: map[string]string{}, Platforms: map[string]string{}},
					},
				)
			}
		}
		if key == "origin" {
			body, _ := json.Marshal(map[string]any{"name": "demo", "versions": versions, "latest_version": versions[1]})
			g.upstream("api/v1/cookbooks/demo", body)
		}
	}
	g.releaseOwners(func(key string) []string {
		base := "/artifactory/api/chef/" + key + "/api/v1/cookbooks/demo"
		var list struct {
			Versions []string `json:"versions"`
		}
		g.decode(base, &list)
		var out []string
		for _, route := range list.Versions {
			var doc struct {
				Version string `json:"version"`
				License string `json:"license"`
			}
			g.decode(strings.TrimPrefix(route, "http://example.com"), &doc)
			out = append(out, doc.Version+"="+doc.License)
		}
		return out
	})
}

func adapterPuppet(g *adapterGraph) {
	for _, key := range []string{"local", "federated", "origin"} {
		var releases []map[string]any
		for _, v := range adapterVersions(key) {
			manifest := puppetfmt.Manifest{Name: "acme-demo", Version: v, Summary: key, Dependencies: []puppetfmt.Dependency{}}
			if key == "origin" {
				releases = append(
					releases,
					map[string]any{
						"version":     v,
						"file_uri":    g.w.Upstream.URL + "/v3/files/acme-demo-" + v + ".tar.gz",
						"file_sha256": strings.Repeat("a", 64),
						"file_size":   10,
						"metadata":    manifest,
					},
				)
			} else {
				p := puppetfmt.Path("acme-demo", v)
				it := g.store(key, p, []byte(key+v))
				raw, _ := json.Marshal(manifest)
				g.version(
					key,
					"acme-demo",
					v,
					puppetfmt.Metadata{Name: "acme-demo", Version: v, Manifest: manifest, Document: raw, Path: p, SHA256: it.Actual.SHA256, Size: it.Size},
				)
			}
		}
		if key == "origin" {
			body, _ := json.Marshal(map[string]any{"results": releases, "pagination": map[string]any{"current": "/v3/releases", "next": nil, "total": 2, "limit": 20, "offset": 0}})
			g.upstream("v3/releases?module=acme-demo&limit=20&offset=0&exclude_fields=reference%20tasks%20plans%20pdk%20validation_score%20malware_scan%20deleted_for", body)
		}
	}
	g.releaseOwners(func(key string) []string {
		var doc struct {
			Results []struct {
				Version  string `json:"version"`
				Metadata struct {
					Summary string `json:"summary"`
				} `json:"metadata"`
			} `json:"results"`
		}
		g.decode("/artifactory/api/puppet/"+key+"/v3/releases?module=acme-demo", &doc)
		var out []string
		for _, r := range doc.Results {
			out = append(out, r.Version+"="+r.Metadata.Summary)
		}
		return out
	})
}

func adapterBower(g *adapterGraph) {
	for _, key := range []string{"local", "federated"} {
		for _, v := range adapterVersions(key) {
			g.version(key, "demo", v, bowerfmt.Manifest{Name: "demo", Version: v})
			g.store(key, "demo/"+v+"/bower.json", []byte(fmt.Sprintf(`{"name":"demo","version":"%s"}`, v)))
			g.store(key, "demo/"+v+"/owner.txt", []byte(key))
		}
	}
	var refs strings.Builder
	for _, v := range adapterVersions("origin") {
		fmt.Fprintf(&refs, "%s refs/tags/v%s\n", strings.Repeat("a", 44), v)
		g.upstream(
			"binaries/origin/demo.git/v"+v,
			adapterTar(g, map[string]string{"demo/bower.json": fmt.Sprintf(`{"name":"demo","version":"%s"}`, v), "demo/owner.txt": "origin"}),
		)
	}
	g.upstream("refs/origin/demo", []byte(refs.String()))
	g.releaseOwners(func(key string) []string {
		body := string(g.get("/artifactory/api/bower/" + key + "/refs/" + key + "/demo"))
		var out []string
		for _, v := range []string{"1.0.0", "2.0.0", "3.0.0", "4.0.0"} {
			if strings.Contains(body, "refs/tags/v"+v) {
				archive := g.get("/artifactory/api/bower/" + key + "/binaries/" + key + "/demo.git/v" + v)
				out = append(out, v+"="+adapterTarOwner(g, archive))
			}
		}
		return out
	})
}

func adapterPods(g *adapterGraph) {
	for _, key := range []string{"local", "federated"} {
		name := map[string]string{"local": "Local", "federated": "Federated"}[key]
		for _, name := range []string{"Shared", name} {
			g.version(key, name, "1.0.0", podfmt.Archive{Name: name, Version: "1.0.0", Spec: []byte(fmt.Sprintf(`{"name":"%s","version":"1.0.0","summary":"%s"}`, name, key))})
		}
	}
	g.upstream("all_pods.txt", []byte("Origin\nShared\n"))
	for _, name := range []string{"Origin", "Shared"} {
		g.upstream(podfmt.ShardFile(name), []byte(name+"/1.0.0\n"))
		g.upstream(
			podfmt.SpecPath(name, "1.0.0"),
			[]byte(fmt.Sprintf(`{"name":"%s","version":"1.0.0","summary":"origin","source":{"http":"%s/source.tar.gz"}}`, name, g.w.Upstream.URL)),
		)
	}
	g.indexOwners(func(key string) []string {
		var out []string
		for name := range strings.SplitSeq(strings.TrimSpace(string(g.get("/artifactory/api/pods/"+key+"/all_pods.txt"))), "\n") {
			var spec struct {
				Summary string `json:"summary"`
			}
			g.decode("/artifactory/api/pods/"+key+"/"+podfmt.SpecPath(name, "1.0.0"), &spec)
			out = append(out, strings.ToLower(name)+"="+spec.Summary)
		}
		return out
	})
}

func adapterTar(g *adapterGraph, files map[string]string) []byte {
	g.t.Helper()
	var b bytes.Buffer
	z := gzip.NewWriter(&b)
	tw := tar.NewWriter(z)
	for name, body := range files {
		if err := tw.WriteHeader(&tar.Header{Name: name, Mode: 0o644, Size: int64(len(body))}); err != nil {
			g.t.Fatal(err)
		}
		if _, err := tw.Write([]byte(body)); err != nil {
			g.t.Fatal(err)
		}
	}
	if err := tw.Close(); err != nil {
		g.t.Fatal(err)
	}
	if err := z.Close(); err != nil {
		g.t.Fatal(err)
	}
	return b.Bytes()
}

func adapterTarOwner(g *adapterGraph, body []byte) string {
	g.t.Helper()
	z, err := gzip.NewReader(bytes.NewReader(body))
	if err != nil {
		g.t.Fatal(err)
	}
	defer func() { _ = z.Close() }()
	tr := tar.NewReader(z)
	for {
		h, err := tr.Next()
		if err == io.EOF {
			g.t.Fatal("archive has no owner.txt")
		}
		if err != nil {
			g.t.Fatal(err)
		}
		if strings.HasSuffix(h.Name, "/owner.txt") || h.Name == "owner.txt" {
			data, err := io.ReadAll(tr)
			if err != nil {
				g.t.Fatal(err)
			}
			return string(data)
		}
	}
}
