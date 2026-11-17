package helm_test

import (
	"bytes"
	"crypto/sha256"
	"fmt"
	"net/http"
	"path"
	"strings"
	"testing"

	"github.com/ProtonMail/go-crypto/openpgp"
	"github.com/ProtonMail/go-crypto/openpgp/clearsign"
	"github.com/ProtonMail/go-crypto/openpgp/packet"
	"gopkg.in/yaml.v3"

	"github.com/weaveplatform/artifactweave/internal/access/accesstest"
	"github.com/weaveplatform/artifactweave/internal/access/api"
	"github.com/weaveplatform/artifactweave/internal/app/config"
	"github.com/weaveplatform/artifactweave/internal/artifacts/adapters/upstream/fakeupstream"
	"github.com/weaveplatform/artifactweave/internal/artifacts/api/repo"
	"github.com/weaveplatform/artifactweave/internal/artifacts/ecosystem"
	helmfmt "github.com/weaveplatform/artifactweave/internal/artifacts/ecosystem/helm"
	"github.com/weaveplatform/artifactweave/internal/kernel"
	"github.com/weaveplatform/artifactweave/internal/platform/settings"
	platsqltest "github.com/weaveplatform/artifactweave/internal/platform/sql/sqltest"
	"github.com/weaveplatform/artifactweave/test/server/support/harness"
)

func virtualHelmWorld(t *testing.T, relative bool, mutate ...func(*config.Config)) *harness.RemoteWorld {
	t.Helper()
	w := harness.NewRemoteWorld(t, mutate...)
	w.Repo.PackageType = repo.Helm
	w.Update()
	for _, c := range []repo.Config{
		{Key: "first", Class: repo.Local, PackageType: repo.Helm},
		{Key: "second", Class: repo.Federated, PackageType: repo.Helm},
		{Key: "inner", Class: repo.Virtual, PackageType: repo.Helm, Repositories: []string{"first", "remote"}},
		{Key: "virtual", Class: repo.Virtual, PackageType: repo.Helm, Repositories: []string{"inner", "second", "remote"}},
	} {
		if err := w.Runtime.Store.Repos().Create(t.Context(), *repo.Defaults(&c)); err != nil {
			t.Fatal(err)
		}
	}
	if err := w.Runtime.Store.Permissions().
		Create(t.Context(), accesstest.Permission{Name: "local-only", Resources: map[api.ResourceType]accesstest.Resource{api.ResourceArtifact: {Actions: accesstest.Grants{Users: map[string]api.Actions{"denied": api.ActionRead}}, Targets: map[string]accesstest.Target{"first": {}}}}}); err != nil {
		t.Fatal(err)
	}
	w.H = harness.Router(harness.Config{
		Store: w.Runtime.Store, Blobs: w.Runtime.Blobs, Auth: w.Runtime.Auth, Tokens: w.Runtime.Tokens, Keys: w.Runtime.Keys, Work: w.Runtime.Work,
		RemoteSources: w.Runtime.Artifacts.RemoteSources(), RemoteHTTP: w.Runtime.Artifacts.RemoteHTTP(), VirtualCache: w.Runtime.Artifacts.VirtualCache(),
		FormatSettings: ecosystem.Settings{Helm: helmfmt.Settings{RelativeURLs: &relative}},
	})
	return w
}

func virtualChart(t *testing.T, w *harness.RemoteWorld, key, p, name, version string) []byte {
	t.Helper()
	body := helmChart(name, version)
	r := w.Request("PUT", "/artifactory/"+key+"/"+p, "admin", string(body))
	if r.Code != 201 {
		t.Fatal(r.Code, r.Body.String())
	}
	return body
}

func upstreamChart(t *testing.T, w *harness.RemoteWorld, p, name, version string) (helmfmt.ChartVersion, []byte) {
	t.Helper()
	body := helmChart(name, version)
	w.Upstream.Set("/"+p, fakeupstream.Resource{Body: body, Header: http.Header{"Content-Type": {"application/octet-stream"}}})
	return helmfmt.ChartVersion{Chart: helmfmt.Chart{Name: name, Version: version, APIVersion: "v2"}, URLs: []string{p}, Digest: fmt.Sprintf("%x", sha256.Sum256(body))}, body
}

func upstreamHelmIndex(t *testing.T, w *harness.RemoteWorld, entries ...helmfmt.ChartVersion) {
	t.Helper()
	index := helmfmt.Index{APIVersion: "v1", Generated: "2026-11-17T12:00:00Z", Entries: map[string][]helmfmt.ChartVersion{}}
	for _, e := range entries {
		index.Entries[e.Name] = append(index.Entries[e.Name], e)
	}
	body, err := helmfmt.EncodeIndex(index)
	if err != nil {
		t.Fatal(err)
	}
	w.Upstream.Set(
		"/index.yaml",
		fakeupstream.Resource{Body: body, Header: http.Header{"Content-Type": {"application/x-yaml"}, "Etag": {fmt.Sprintf(`"%x"`, sha256.Sum256(body))}}},
	)
}

func readVirtualIndex(t *testing.T, w *harness.RemoteWorld, user string, headers ...string) helmfmt.Index {
	t.Helper()
	r := w.Request("GET", "/artifactory/api/helm/virtual/index.yaml", user, "", headers...)
	if r.Code != 200 {
		t.Fatal(r.Code, r.Body.String())
	}
	var index helmfmt.Index
	if err := yaml.Unmarshal(r.Body.Bytes(), &index); err != nil {
		t.Fatal(err)
	}
	if strings.Contains(r.Body.String(), "local://") {
		t.Fatal("local URL leaked")
	}
	return index
}

func TestAccHelmVirtualIndexURLsPermissionsAndDownloads(t *testing.T) {
	for _, relative := range []bool{true, false} {
		t.Run(fmt.Sprint(relative), func(t *testing.T) {
			w := virtualHelmWorld(t, relative)
			local := virtualChart(t, w, "first", "charts/web-1.0.0.tgz", "web", "1.0.0")
			virtualChart(t, w, "second", "charts/web-2.0.0.tgz", "web", "2.0.0")
			remote, archive := upstreamChart(t, w, "remote-1.0.0.tgz", "remote", "1.0.0")
			duplicate, _ := upstreamChart(t, w, "duplicate.tgz", "web", "1.0.0")
			upstreamHelmIndex(t, w, remote, duplicate)
			index := readVirtualIndex(t, w, "admin", "X-JFrog-Override-Base-Url", "https://proxy.example/prefix")
			if len(index.Entries) != 2 || len(index.Entries["web"]) != 2 || index.Entries["web"][0].Version != "2.0.0" ||
				index.Entries["web"][1].Digest != fmt.Sprintf("%x", sha256.Sum256(local)) {
				t.Fatal(index)
			}
			head := w.Request("HEAD", "/artifactory/api/helm/virtual/index.yaml", "admin", "")
			if head.Code != 200 || head.Body.Len() != 0 || head.Header().Get("Content-Length") == "" || head.Header().Get("ETag") == "" {
				t.Fatal("index HEAD", head.Code, head.Header())
			}
			condition := w.Request("GET", "/artifactory/api/helm/virtual/index.yaml", "admin", "", "If-None-Match", head.Header().Get("ETag"))
			if condition.Code != 304 {
				t.Fatal("index condition", condition.Code)
			}
			want := "remote-1.0.0.tgz"
			if !relative {
				want = "https://proxy.example/prefix/artifactory/virtual/" + want
			}
			if index.Entries["remote"][0].URLs[0] != want {
				t.Fatal(index.Entries)
			}
			localOnly := readVirtualIndex(t, w, "denied")
			remoteOnly := readVirtualIndex(t, w, "reader")
			if len(localOnly.Entries) != 1 || len(localOnly.Entries["web"]) != 1 || len(remoteOnly.Entries) != 2 {
				t.Fatal("permission sets shared", localOnly, remoteOnly)
			}
			key, err := openpgp.NewEntity("fixture", "", "test@example.invalid", &packet.Config{Algorithm: packet.PubKeyAlgoRSA, RSABits: 2048})
			if err != nil {
				t.Fatal(err)
			}
			var signed bytes.Buffer
			signer, err := clearsign.Encode(&signed, key.PrivateKey, nil)
			if err != nil {
				t.Fatal(err)
			}
			if _, err = fmt.Fprintf(signer, "apiVersion: v2\nname: remote\nversion: 1.0.0\n...\nfiles:\n  %s: sha256:%s\n", path.Base(remote.URLs[0]), remote.Digest); err != nil {
				t.Fatal(err)
			}
			if err = signer.Close(); err != nil {
				t.Fatal(err)
			}
			w.Upstream.Set("/remote-1.0.0.tgz.prov", fakeupstream.Resource{Body: signed.Bytes(), Header: http.Header{"Content-Type": {"application/octet-stream"}}})
			for p, want := range map[string][]byte{"charts/web-1.0.0.tgz": local, "remote-1.0.0.tgz": archive, "remote-1.0.0.tgz.prov": signed.Bytes()} {
				r := w.Request("GET", "/artifactory/api/helm/virtual/"+p, "admin", "")
				if r.Code != 200 || !bytes.Equal(r.Body.Bytes(), want) {
					t.Fatal(p, r.Code, r.Body.String())
				}
				head := w.Request("HEAD", "/artifactory/api/helm/virtual/"+p, "admin", "")
				if head.Code != 200 || head.Body.Len() != 0 {
					t.Fatal("HEAD", p, head.Code)
				}
			}
			missing := w.Request("GET", "/artifactory/api/helm/virtual/missing.tgz", "admin", "")
			if missing.Code != 404 {
				t.Fatal(missing.Code, missing.Body.String())
			}
			reindex := w.Request("POST", "/artifactory/api/helm/virtual/reindex", "admin", "")
			if reindex.Code != 400 || !strings.Contains(reindex.Body.String(), "Repository 'virtual' is not a local helm repository") {
				t.Fatal(reindex.Code, reindex.Body.String())
			}
		})
	}
}

func TestAccHelmVirtualIndexInvalidationAndOutage(t *testing.T) {
	w := virtualHelmWorld(t, true)
	virtualChart(t, w, "first", "local-1.0.0.tgz", "local", "1.0.0")
	remote, _ := upstreamChart(t, w, "remote-1.0.0.tgz", "remote", "1.0.0")
	upstreamHelmIndex(t, w, remote)
	for range 2 {
		readVirtualIndex(t, w, "admin")
		readVirtualIndex(t, w, "denied")
		readVirtualIndex(t, w, "reader")
	}
	virtualChart(t, w, "first", "added-1.0.0.tgz", "added", "1.0.0")
	for _, user := range []string{"admin", "denied"} {
		if len(readVirtualIndex(t, w, user).Entries["added"]) != 1 {
			t.Fatal("nested write did not invalidate", user)
		}
	}
	more, _ := upstreamChart(t, w, "more-1.0.0.tgz", "more", "1.0.0")
	upstreamHelmIndex(t, w, remote, more)
	w.Repo.RetrievalCachePeriodSecs = new(0)
	w.Update()
	r := w.Request("GET", "/artifactory/api/helm/remote/index.yaml", "admin", "")
	if r.Code != 200 {
		t.Fatal(r.Code, r.Body.String())
	}
	if len(readVirtualIndex(t, w, "reader").Entries["more"]) != 1 {
		t.Fatal("remote refresh did not invalidate")
	}
	w.Upstream.Close()
	c, err := w.Runtime.Store.Repos().Get(t.Context(), "virtual")
	if err != nil {
		t.Fatal(err)
	}
	c.VirtualRetrievalCachePeriodSecs = new(-1)
	if err = w.Runtime.Store.Repos().Update(t.Context(), c); err != nil {
		t.Fatal(err)
	}
	if len(readVirtualIndex(t, w, "admin").Entries["remote"]) != 1 {
		t.Fatal("cached remote missing during outage")
	}
}

func TestAccHelmVirtualBeforeAfterProbe(t *testing.T) {
	for _, relative := range []bool{true, false} {
		t.Run(fmt.Sprint(relative), func(t *testing.T) {
			w := virtualHelmWorld(t, relative)
			virtualChart(t, w, "first", "local-1.0.0.tgz", "local", "1.0.0")
			r := w.Request("PUT", "/artifactory/first/local-1.0.0.tgz.prov", "admin", "fixture provenance")
			if r.Code != 201 {
				t.Fatal(r.Code, r.Body.String())
			}
			remote, _ := upstreamChart(t, w, "remote-1.0.0.tgz", "remote", "1.0.0")
			upstreamHelmIndex(t, w, remote)
			for _, tc := range []struct {
				method, path, user string
				want               int
			}{
				{"GET", "/artifactory/api/helm/virtual/index.yaml", "admin", 200},
				{"GET", "/artifactory/api/helm/virtual/local-1.0.0.tgz", "admin", 200},
				{"GET", "/artifactory/api/helm/virtual/local-1.0.0.tgz.prov", "admin", 200},
				{"GET", "/artifactory/api/helm/virtual/local-1.0.0.tgz", "reader", 404},
				{"POST", "/artifactory/api/helm/virtual/reindex", "admin", 400},
				{"GET", "/artifactory/api/helm/first/index.yaml", "admin", 200},
			} {
				r := w.Request(tc.method, tc.path, tc.user, "")
				t.Logf("relative=%t %s %s user=%s status=%d", relative, tc.method, tc.path, tc.user, r.Code)
				if r.Code != tc.want {
					t.Errorf("want %d, got %d: %s", tc.want, r.Code, r.Body.String())
				}
			}
		})
	}
}

func TestAccHelmVirtualPostgresInvalidation(t *testing.T) {
	dsn := platsqltest.ScratchDSN(t, platsqltest.PostgresDSN(t))
	w := virtualHelmWorld(t, true, func(cfg *config.Config) { cfg.Database.Driver = "postgres"; cfg.Database.DSN = dsn })
	virtualChart(t, w, "first", "one-1.0.0.tgz", "one", "1.0.0")
	remote, _ := upstreamChart(t, w, "remote-1.0.0.tgz", "remote", "1.0.0")
	upstreamHelmIndex(t, w, remote)
	for range 2 {
		readVirtualIndex(t, w, "admin")
	}
	virtualChart(t, w, "first", "two-1.0.0.tgz", "two", "1.0.0")
	if len(readVirtualIndex(t, w, "admin").Entries["two"]) != 1 {
		t.Fatal("PostgreSQL member write left a cached index")
	}
}

func TestAccHelmVirtualMemberVisibilityAndPatterns(t *testing.T) {
	w := virtualHelmWorld(t, true)
	virtualChart(t, w, "first", "visible/public-1.0.0.tgz", "public", "1.0.0")
	virtualChart(t, w, "first", "private/secret-1.0.0.tgz", "secret", "1.0.0")
	if err := w.Runtime.Store.Users().Create(t.Context(), accesstest.User{Name: "partial", PasswordHash: cheapHash}); err != nil {
		t.Fatal(err)
	}
	if err := w.Runtime.Store.Permissions().
		Create(t.Context(), accesstest.Permission{Name: "partial", Resources: map[api.ResourceType]accesstest.Resource{api.ResourceArtifact: {Actions: accesstest.Grants{Users: map[string]api.Actions{"partial": api.ActionRead}}, Targets: map[string]accesstest.Target{"first": {Include: []string{"visible/**"}}}}}}); err != nil {
		t.Fatal(err)
	}
	index := readVirtualIndex(t, w, "partial")
	if len(index.Entries) != 2 || len(index.Entries["secret"]) != 1 {
		t.Fatal("partial member read filtered chart names", index)
	}
	denied := w.Request("GET", "/artifactory/api/helm/virtual/private/secret-1.0.0.tgz", "partial", "")
	if denied.Code != 404 {
		t.Fatal(denied.Code, denied.Body.String())
	}
	c, err := w.Runtime.Store.Repos().Get(t.Context(), "first")
	if err != nil {
		t.Fatal(err)
	}
	c.ExcludesPattern = new("private/**")
	if err = w.Runtime.Store.Repos().Update(t.Context(), c); err != nil {
		t.Fatal(err)
	}
	if len(readVirtualIndex(t, w, "admin").Entries["secret"]) != 1 {
		t.Fatal("chart pattern filtered index")
	}
	c, err = w.Runtime.Store.Repos().Get(t.Context(), "virtual")
	if err != nil {
		t.Fatal(err)
	}
	c.ExcludesPattern = new("index.yaml")
	if err = w.Runtime.Store.Repos().Update(t.Context(), c); err != nil {
		t.Fatal(err)
	}
	refused := w.Request("GET", "/artifactory/api/helm/virtual/index.yaml", "admin", "")
	if refused.Code != 404 {
		t.Fatal(refused.Code, refused.Body.String())
	}
	c.ExcludesPattern = nil
	c.Repositories = nil
	if err = w.Runtime.Store.Repos().Update(t.Context(), c); err != nil {
		t.Fatal(err)
	}
	missing := w.Request("GET", "/artifactory/api/helm/virtual/index.yaml", "admin", "")
	if missing.Code != 404 {
		t.Fatal(missing.Code, missing.Body.String())
	}
}

func TestAccHelmVirtualAnonymousURLs(t *testing.T) {
	for _, relative := range []bool{true, false} {
		t.Run(fmt.Sprint(relative), func(t *testing.T) {
			w := virtualHelmWorld(t, relative)
			virtualChart(t, w, "first", "public-1.0.0.tgz", "public", "1.0.0")
			if err := w.Runtime.Store.Settings().Put(t.Context(), settings.SettingAnonymousAccess, true); err != nil {
				t.Fatal(err)
			}
			if err := w.Runtime.Store.Permissions().
				Create(t.Context(), accesstest.Permission{Name: "anonymous-helm", Resources: map[api.ResourceType]accesstest.Resource{api.ResourceArtifact: {Actions: accesstest.Grants{Users: map[string]api.Actions{kernel.AnonymousUser: api.ActionRead}}, Targets: map[string]accesstest.Target{"first": {}}}}}); err != nil {
				t.Fatal(err)
			}
			index := readVirtualIndex(t, w, "")
			if len(index.Entries["public"]) != 1 || strings.Contains(index.Entries["public"][0].URLs[0], "local://") {
				t.Fatal(index)
			}
		})
	}
}
