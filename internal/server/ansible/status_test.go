package ansible_test

import (
	"archive/tar"
	"bytes"
	"compress/gzip"
	"encoding/json"
	"errors"
	"io"
	"net/http"
	"strings"
	"testing"

	ansiblefmt "github.com/weaveplatform/artifactweave/internal/format/ansible"
	"github.com/weaveplatform/artifactweave/internal/service/item"
	"github.com/weaveplatform/artifactweave/pkg/repo"
	"github.com/weaveplatform/artifactweave/pkg/types"
)

func TestAccGalaxyRouteStatusAndLifecycle(t *testing.T) {
	f := newFixture(t)
	requireStatus(t, f.publish(f.data, ""), 201)
	for _, tc := range []struct {
		method, path, user string
		want               int
	}{
		{"HEAD", "/", "", 200},
		{"GET", "/unknown", "", 404},
		{"GET", "/api/v3/collections/weave/missing/", "", 404},
		{"GET", "/api/v3/collections/invalid-name/sample/", "", 400},
		{"GET", "/api/v3/collections/invalid-name/sample/versions/", "", 400},
		{"GET", "/api/v3/unknown", "", 404},
		{"GET", "/api/v3/collections/weave/sample/versions/garbage/", "", 400},
		{"GET", "/api/v3/collections/weave/sample/versions/2.0.0/", "", 404},
		{"GET", "/api/v3/collections/weave/sample/versions/?offset=-1", "", 400},
		{"GET", "/api/v3/collections/weave/sample/versions/?limit=0", "", 400},
		{"GET", "/api/v3/imports/collections/absent/", "", 404},
		{"GET", "/api/v3/imports/collections/" + strings.Repeat("a", 64) + "/", "", 404},
		{"GET", "/api/v3/collections/weave/sample/", "outsider", 403},
		{"GET", "/api/v3/collections/weave/sample/versions/1.2.3/", "denied", 401},
		{"GET", "/api/v3/plugin/ansible/content/published/collections/artifacts/invalid.zip", "", 400},
		{"GET", "/api/v3/plugin/ansible/content/published/collections/artifacts/invalid.tar.gz", "", 400},
		{"GET", "/api/v3/plugin/ansible/content/published/collections/artifacts/weave-sample-2.0.0.tar.gz", "", 404},
		{"GET", "/api/v3/plugin/ansible/content/published/collections/artifacts/weave-sample-1.2.3.tar.gz", "denied", 401},
		{"GET", "/api/v3/collections/weave/sample/;=bad", "", 400},
	} {
		t.Run(
			tc.method+tc.path+tc.user,
			func(t *testing.T) { requireStatus(t, f.do(tc.method, prefix+tc.path, nil, "", tc.user), tc.want) },
		)
	}
	requireStatus(
		t,
		f.do("POST", prefix+"/api/v3/artifacts/collections/", []byte("bad"), "text/plain", ""),
		415,
	)
	p := ansiblefmt.Path("weave", "sample", "1.2.3")
	it, err := f.db.Items().Get(t.Context(), f.repo.Key, false, p)
	if err != nil {
		t.Fatal(err)
	}
	props, err := f.db.Items().Properties(t.Context(), it.ID)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := f.items.Delete(
		t.Context(),
		item.Target{Repo: f.repo, Path: p},
		"admin",
	); err != nil {
		t.Fatal(err)
	}
	requireStatus(
		t,
		f.do("GET", prefix+"/api/v3/collections/weave/sample/versions/1.2.3/", nil, "", ""),
		404,
	)
	if _, err := f.items.Restore(
		t.Context(),
		item.Target{Repo: f.repo, Path: p},
		types.TrashRecord{Item: it, Properties: props},
		"admin",
	); err != nil {
		t.Fatal(err)
	}
	if err := f.items.Reindex(t.Context(), f.repo); err != nil {
		t.Fatal(err)
	}
	requireStatus(
		t,
		f.do("GET", prefix+"/api/v3/collections/weave/sample/versions/1.2.3/", nil, "", ""),
		200,
	)
}

func TestAccGalaxyVirtualOrderedReadableMembers(t *testing.T) {
	f := newFixture(t)
	for _, key := range []string{"first", "second"} {
		c := *repo.Defaults(&repo.Config{Key: key, Class: repo.Local, PackageType: repo.Ansible})
		if err := f.db.Repos().Create(t.Context(), c); err != nil {
			t.Fatal(err)
		}
		if _, err := f.items.Deploy(
			t.Context(),
			item.Deploy{
				Target: item.Target{Repo: c, Path: ansiblefmt.Path("weave", "sample", "1.2.3")},
				Body:   bytes.NewReader(f.data),
				By:     "admin",
			},
		); err != nil {
			t.Fatal(err)
		}
		p, err := f.db.Packages().Find(t.Context(), key, "weave.sample")
		if err != nil {
			t.Fatal(err)
		}
		vs, err := f.db.Packages().Versions(t.Context(), p.ID)
		if err != nil {
			t.Fatal(err)
		}
		var m ansiblefmt.Metadata
		if err := json.Unmarshal(vs[0].Metadata, &m); err != nil {
			t.Fatal(err)
		}
		m.Description = key
		vs[0].Metadata, _ = json.Marshal(m)
		if _, err := f.db.Packages().PutVersion(t.Context(), vs[0]); err != nil {
			t.Fatal(err)
		}
	}
	virtual := *repo.Defaults(&repo.Config{Key: "virtual", Class: repo.Virtual, PackageType: repo.Ansible, Repositories: []string{"missing", "first", "second"}})
	if err := f.db.Repos().Create(t.Context(), virtual); err != nil {
		t.Fatal(err)
	}
	permission := types.Permission{
		Name: "reader",
		Resources: map[types.ResourceType]types.Resource{
			types.ResourceArtifact: {
				Actions: types.Grants{Users: map[string]types.Actions{"reader": types.ActionRead}},
				Targets: map[string]types.Target{
					"virtual": {Include: []string{"**"}},
					"second":  {Include: []string{"**"}},
				},
			},
		},
	}
	if err := f.db.Permissions().Create(t.Context(), permission); err != nil {
		t.Fatal(err)
	}
	for _, tc := range []struct{ user, winner string }{{"", "first"}, {"reader", "second"}} {
		t.Run(tc.winner, func(t *testing.T) {
			endpoint := "/artifactory/api/ansible/virtual/api/v3/collections/weave/sample/versions/1.2.3/"
			w := f.do("GET", endpoint, nil, "", tc.user)
			requireStatus(t, w, 200)
			if !strings.Contains(w.Body.String(), `"description":"`+tc.winner+`"`) {
				t.Fatalf("wrong member: %s", w.Body)
			}
			requireStatus(
				t,
				f.do(
					"GET",
					"/artifactory/api/ansible/virtual/api/v3/plugin/ansible/content/published/collections/artifacts/weave-sample-1.2.3.tar.gz",
					nil,
					"",
					tc.user,
				),
				200,
			)
		})
	}
	virtual.Repositories = []string{"virtual"}
	if err := f.db.Repos().Update(t.Context(), virtual); err != nil {
		t.Fatal(err)
	}
	requireStatus(
		t,
		f.do(
			"GET",
			"/artifactory/api/ansible/virtual/api/v3/collections/weave/sample/versions/",
			nil,
			"",
			"",
		),
		400,
	)
	requireStatus(
		t,
		f.do("POST", "/artifactory/api/ansible/virtual/api/v3/artifacts/collections/", nil, "", ""),
		http.StatusMethodNotAllowed,
	)
}

func TestAccGalaxyPaginationAndPathPermissions(t *testing.T) {
	f := newFixture(t)
	for _, v := range []string{"1.3.0", "2.0.0", "1.2.3"} {
		data := versionArchive(t, f.data, v)
		if _, err := f.items.Deploy(
			t.Context(),
			item.Deploy{
				Target: item.Target{Repo: f.repo, Path: ansiblefmt.Path("weave", "sample", v)},
				Body:   bytes.NewReader(data),
				By:     "admin",
			},
		); err != nil {
			t.Fatal(err)
		}
	}
	w := f.do("GET", prefix+"/api/v3/collections/weave/sample/", nil, "", "")
	requireStatus(t, w, 200)
	w = f.do("GET", prefix+"/api/v3/collections/weave/sample/versions/?limit=1", nil, "", "")
	requireStatus(t, w, 200)
	var doc struct {
		Links struct {
			Next string `json:"next"`
		} `json:"links"`
	}
	if err := json.Unmarshal(w.Body.Bytes(), &doc); err != nil {
		t.Fatal(err)
	}
	if doc.Links.Next == "" || !strings.Contains(w.Body.String(), "2.0.0") {
		t.Fatalf("first page: %s", w.Body)
	}
	w = f.do("GET", doc.Links.Next, nil, "", "")
	requireStatus(t, w, 200)
	if !strings.Contains(w.Body.String(), "1.3.0") {
		t.Fatalf("next page: %s", w.Body)
	}
	permission := types.Permission{
		Name: "one-version",
		Resources: map[types.ResourceType]types.Resource{
			types.ResourceArtifact: {
				Actions: types.Grants{Users: map[string]types.Actions{"reader": types.ActionRead}},
				Targets: map[string]types.Target{
					f.repo.Key: {Include: []string{"collections/weave/sample/1.2.3/**"}},
				},
			},
		},
	}
	if err := f.db.Permissions().Create(t.Context(), permission); err != nil {
		t.Fatal(err)
	}
	w = f.do("GET", prefix+"/api/v3/collections/weave/sample/versions/", nil, "", "reader")
	requireStatus(t, w, 200)
	if strings.Contains(w.Body.String(), "2.0.0") || !strings.Contains(w.Body.String(), "1.2.3") {
		t.Fatalf("permission-filtered versions: %s", w.Body)
	}
}

func versionArchive(t *testing.T, data []byte, version string) []byte {
	t.Helper()
	gz, err := gzip.NewReader(bytes.NewReader(data))
	if err != nil {
		t.Fatal(err)
	}
	defer gz.Close()
	var result bytes.Buffer
	writer := gzip.NewWriter(&result)
	tw := tar.NewWriter(writer)
	tr := tar.NewReader(gz)
	for {
		hdr, err := tr.Next()
		if errors.Is(err, io.EOF) {
			break
		}
		if err != nil {
			t.Fatal(err)
		}
		body, err := io.ReadAll(tr)
		if err != nil {
			t.Fatal(err)
		}
		if hdr.Name == "MANIFEST.json" {
			var m map[string]json.RawMessage
			if err := json.Unmarshal(body, &m); err != nil {
				t.Fatal(err)
			}
			var info map[string]any
			if err := json.Unmarshal(m["collection_info"], &info); err != nil {
				t.Fatal(err)
			}
			info["version"] = version
			m["collection_info"], _ = json.Marshal(info)
			body, _ = json.Marshal(m)
			hdr.Size = int64(len(body))
		}
		if err := tw.WriteHeader(hdr); err != nil {
			t.Fatal(err)
		}
		if _, err := tw.Write(body); err != nil {
			t.Fatal(err)
		}
	}
	if err := tw.Close(); err != nil {
		t.Fatal(err)
	}
	if err := writer.Close(); err != nil {
		t.Fatal(err)
	}
	return result.Bytes()
}
