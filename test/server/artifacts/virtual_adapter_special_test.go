package artifacts_test

import (
	"bytes"
	"crypto/sha1" //nolint:gosec // Git blob identity, required by the Hub protocol.
	"crypto/sha256"
	"encoding/base64"
	"encoding/json"
	"encoding/xml"
	"fmt"
	"net/http"
	"strings"

	"github.com/weaveplatform/artifactweave/internal/artifacts/adapters/upstream/fakeupstream"
	"github.com/weaveplatform/artifactweave/internal/artifacts/api/repo"
	"github.com/weaveplatform/artifactweave/internal/artifacts/core/model"
	nixfmt "github.com/weaveplatform/artifactweave/internal/artifacts/ecosystem/nix"
)

func adapterSpecialRows() []adapterConformance {
	return []adapterConformance{
		{repo.Composer, adapterComposer},
		{repo.Conan, adapterConan},
		{repo.BazelModules, adapterBazel},
		{repo.P2, adapterP2},
		{repo.Nix, adapterNix},
		{repo.GitLFS, adapterGitLFS},
		{repo.HuggingFaceML, adapterHub},
	}
}

func adapterComposer(g *adapterGraph) {
	for _, key := range []string{"local", "federated", "origin"} {
		var releases []map[string]any
		for _, v := range adapterVersions(key) {
			doc := map[string]any{"name": "acme/demo", "version": v, "description": key}
			if key == "origin" {
				doc["dist"] = map[string]any{"type": "zip", "url": g.w.Upstream.URL + "/demo-" + v + ".zip"}
				releases = append(releases, doc)
			} else {
				raw, _ := json.Marshal(doc)
				g.store(key, "acme/demo/demo-"+v+".zip", adapterZip(g.t, map[string]string{"composer.json": string(raw)}))
			}
		}
		if key == "origin" {
			g.upstream("packages.json", []byte(`{"metadata-url":"/p2/%package%.json"}`))
			body, _ := json.Marshal(map[string]any{"packages": map[string]any{"acme/demo": releases}})
			g.upstream("p2/acme/demo.json", body)
		}
	}
	g.releaseOwners(func(key string) []string {
		var doc struct {
			Packages map[string][]struct {
				Version     string `json:"version"`
				Description string `json:"description"`
			} `json:"packages"`
		}
		g.decode("/artifactory/api/composer/"+key+"/p2/acme/demo.json", &doc)
		var out []string
		for _, r := range doc.Packages["acme/demo"] {
			out = append(out, r.Version+"="+r.Description)
		}
		return out
	})
}

func adapterConan(g *adapterGraph) {
	const p = "_/demo/1.0/_/r1/export/conanmanifest.txt"
	const remote = "v2/conans/demo/1.0/_/_/revisions/r1/files/conanmanifest.txt"
	bodies := map[string][]byte{}
	for i, key := range []string{"local", "federated", "origin"} {
		bodies[key] = []byte(fmt.Sprintf("%d\nconanfile.py: %s\n", i+1, strings.Repeat("a", 32)))
	}
	g.upstream(remote, bodies["origin"])
	g.fileOwners(p, func(key string) string { return "/artifactory/api/conan/" + key + "/" + remote }, bodies)
}

func adapterBazel(g *adapterGraph) {
	const p = "modules/demo/1.0.0/MODULE.bazel"
	g.upstream("modules/demo/metadata.json", []byte(`{"versions":["1.0.0"],"yanked_versions":{}}`))
	bodies := map[string][]byte{}
	for _, key := range []string{"local", "federated", "origin"} {
		bodies[key] = []byte("module(name = \"demo\", version = \"1.0.0\")\n# " + key + "\n")
		if key == "origin" {
			g.upstream(p, bodies[key])
			g.upstream(
				"modules/demo/1.0.0/source.json",
				[]byte(`{"url":"`+g.w.Upstream.URL+`/source.tar.gz","integrity":"sha256-`+base64.StdEncoding.EncodeToString(make([]byte, 32))+`","strip_prefix":"demo"}`),
			)
		} else {
			g.store(key, "modules/demo/1.0.0/source.tar.gz", adapterTar(g, map[string]string{"demo/README": "fixture"}))
		}
	}
	g.fileOwners(p, func(key string) string { return "/artifactory/api/bazelmodules/" + key + "/" + p }, bodies)
}

func adapterP2(g *adapterGraph) {
	content := adapterFixture(g.t, "p2", "content.jar")
	g.store("local", "content.jar", content)
	g.store("federated", "content.jar", content)
	g.upstream("content.jar", content)
	for _, key := range []string{"local", "federated", "origin"} {
		g.want("/artifactory/"+key+"/content.jar", content)
	}
	children := func(route string, want ...string) {
		g.t.Helper()
		var doc struct {
			Children struct {
				Child []struct {
					Location string `xml:"location,attr"`
				} `xml:"child"`
			} `xml:"children"`
		}
		if err := xml.Unmarshal(g.get(route), &doc); err != nil {
			g.t.Fatal(err)
		}
		var got []string
		for _, c := range doc.Children.Child {
			got = append(got, c.Location)
		}
		if strings.Join(got, ",") != strings.Join(want, ",") {
			g.t.Fatalf("P2 children %s=%v; want %v", route, got, want)
		}
	}
	// P2 intentionally preserves composite child URLs rather than flattening
	// update sites into a synthetic content.jar (decision 0008, ADP-04).
	children("/artifactory/outer/compositeContent.xml", "local", "inner", "origin")
	children("/artifactory/outer/inner/compositeContent.xml", "federated", "local")
	for _, route := range []string{"/artifactory/outer/local/content.jar", "/artifactory/outer/inner/federated/content.jar", "/artifactory/outer/origin/content.jar"} {
		g.want(route, content)
	}
	g.graph([]string{"origin", "inner", "local", "origin", "outer"}, []string{"federated", "outer", "local", "inner", "federated"})
	children("/artifactory/outer/compositeContent.xml", "inner", "origin")
	children("/artifactory/outer/inner/compositeContent.xml", "federated", "local")
	g.want("/artifactory/outer/inner/local/content.jar", content)
	g.graph([]string{"origin", "inner", "origin", "outer"}, []string{"outer", "inner"})
	children("/artifactory/outer/compositeContent.xml", "origin")
	g.want("/artifactory/outer/origin/content.jar", content)
}

// contentOwners proves serving-member selection for content-addressed formats
// without fabricating different bytes for one hash. Real item download stats
// identify the local/federated holder; the private origin's request count
// proves it is untouched until no hosted member remains.
func (g *adapterGraph) contentOwners(route string, local, federated model.Item, body []byte, originPath string) {
	g.t.Helper()
	for _, key := range []string{"local", "federated"} {
		g.want(strings.Replace(route, "/outer/", "/"+key+"/", 1), body)
	}
	count := func(it model.Item) int64 {
		g.t.Helper()
		s, err := g.w.Runtime.Store.Items().Stats(g.t.Context(), it.ID)
		if err != nil {
			g.t.Fatal(err)
		}
		return s.DownloadCount
	}
	beforeLocal, beforeFed := count(local), count(federated)
	g.want(route, body)
	if count(local) != beforeLocal+1 || count(federated) != beforeFed {
		g.t.Fatal("first read did not select the explicit local holder")
	}
	if n := g.w.Upstream.Count("", ""); n != 0 {
		g.t.Fatalf("local winner contacted origin %d times", n)
	}
	g.graph([]string{"origin", "inner", "local", "origin", "outer"}, []string{"federated", "outer", "local", "inner", "federated"})
	g.want(route, body)
	if count(local) != beforeLocal+1 || count(federated) != beforeFed+1 {
		g.t.Fatal("nested-first read did not select the explicit federated holder")
	}
	if n := g.w.Upstream.Count("", ""); n != 0 {
		g.t.Fatalf("federated winner contacted origin %d times", n)
	}
	g.graph([]string{"origin", "inner", "origin", "outer"}, []string{"outer", "inner"})
	g.want(route, body)
	if n := g.w.Upstream.Count(http.MethodGet, originPath); n != 1 {
		g.t.Fatalf("remote object fetched %d times; want once despite duplicate/cycle", n)
	}
	g.want(strings.Replace(route, "/outer/", "/origin/", 1), body)
}

func adapterGitLFS(g *adapterGraph) {
	body := []byte("content-addressed adapter conformance\n")
	oid := fmt.Sprintf("%x", sha256.Sum256(body))
	p := "objects/" + oid[:2] + "/" + oid[2:4] + "/" + oid
	local := g.store("local", p, body)
	fed := g.store("federated", p, body)
	remotePath := "/origin/objects/" + oid
	g.w.Upstream.Set(
		"/origin/objects/batch",
		fakeupstream.Resource{
			Method: http.MethodPost,
			Status: http.StatusOK,
			Header: http.Header{"Content-Type": {"application/vnd.git-lfs+json"}},
			Body: []byte(
				fmt.Sprintf(`{"transfer":"basic","objects":[{"oid":"%s","size":%d,"actions":{"download":{"href":"%s%s"}}}]}`, oid, len(body), g.w.Upstream.URL, remotePath),
			),
		},
	)
	g.upstream(remotePath, body)
	g.contentOwners(fmt.Sprintf("/artifactory/api/lfs/outer/objects/%s?size=%d", oid, len(body)), local, fed, body, remotePath)
}

func adapterNix(g *adapterGraph) {
	const hash = "fyli4q2yj6vh01bs0jxqbvj5hir5zvqc"
	meta := adapterFixture(g.t, "nix", "cache/"+hash+".narinfo")
	info, err := nixfmt.Parse(meta, hash+".narinfo")
	if err != nil {
		g.t.Fatal(err)
	}
	body := adapterFixture(g.t, "nix", "cache/"+info.URL)
	g.store("local", nixfmt.InfoItem(hash, false), meta)
	g.store("federated", nixfmt.InfoItem(hash, false), meta)
	local := g.store("local", nixfmt.ArchiveItem(hash, info.URL, false), body)
	fed := g.store("federated", nixfmt.ArchiveItem(hash, info.URL, false), body)
	g.upstream(hash+".narinfo", meta)
	g.upstream(info.URL, body)
	g.contentOwners("/artifactory/api/nix/outer/nar/"+hash+"/"+strings.TrimPrefix(info.URL, "nar/"), local, fed, body, "/"+info.URL)
}

func adapterHub(g *adapterGraph) {
	bodies := map[string][]byte{}
	for _, key := range []string{"local", "federated"} {
		body := []byte(`{"owner":"` + key + `"}`)
		bodies[key] = body
		commit := `{"key":"header","value":{"summary":"adapter conformance"}}` + "\n" + fmt.Sprintf(
			`{"key":"file","value":{"path":"config.json","content":"%s","encoding":"base64"}}`,
			base64.StdEncoding.EncodeToString(body),
		) + "\n"
		r := g.w.Request(http.MethodPost, "/artifactory/api/huggingfaceml/"+key+"/api/models/acme/demo/commit/main", "admin", commit, "Content-Type", "application/x-ndjson")
		if r.Code != http.StatusOK {
			g.t.Fatalf("Hub commit %s=%d %s", key, r.Code, r.Body)
		}
	}
	bodies["origin"] = []byte(`{"owner":"origin"}`)
	const rev = "0123456789abcdef0123456789abcdef01234567"
	git := sha1.New()
	_, _ = fmt.Fprintf(git, "blob %d\x00", len(bodies["origin"]))
	_, _ = git.Write(bodies["origin"])
	g.upstream(
		"api/models/acme/demo/revision/main",
		[]byte(fmt.Sprintf(`{"id":"acme/demo","sha":"%s","siblings":[{"rfilename":"config.json","size":%d,"blobId":"%x"}]}`, rev, len(bodies["origin"]), git.Sum(nil))),
	)
	g.upstream("acme/demo/resolve/"+rev+"/config.json", bodies["origin"])
	g.upstream("acme/demo/resolve/main/config.json", bodies["origin"])
	read := func(key string) string {
		return "/artifactory/api/huggingfaceml/" + key + "/acme/demo/resolve/main/config.json"
	}
	for _, key := range []string{"local", "federated", "origin"} {
		g.want(read(key), bodies[key])
	}
	g.want(read("outer"), bodies["local"])
	g.graph([]string{"origin", "inner", "local", "origin", "outer"}, []string{"federated", "outer", "local", "inner", "federated"})
	g.want(read("outer"), bodies["federated"])
	g.graph([]string{"origin", "inner", "origin", "outer"}, []string{"outer", "inner"})
	g.want(read("outer"), bodies["origin"])
	if bytes.Equal(bodies["local"], bodies["origin"]) {
		g.t.Fatal("Hub fixtures must distinguish owners")
	}
}
