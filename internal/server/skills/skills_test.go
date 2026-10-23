package skills_test

import (
	"archive/zip"
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"io"
	"mime/multipart"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"github.com/go-chi/chi/v5"

	"github.com/weaveplatform/artifactweave/internal/auth"
	"github.com/weaveplatform/artifactweave/internal/authz"
	"github.com/weaveplatform/artifactweave/internal/format"
	skillsfmt "github.com/weaveplatform/artifactweave/internal/format/skills"
	"github.com/weaveplatform/artifactweave/internal/server/apierror"
	"github.com/weaveplatform/artifactweave/internal/server/pkgroute"
	"github.com/weaveplatform/artifactweave/internal/server/skills"
	"github.com/weaveplatform/artifactweave/internal/service/item"
	"github.com/weaveplatform/artifactweave/pkg/blob/fs"
	"github.com/weaveplatform/artifactweave/pkg/repo"
	"github.com/weaveplatform/artifactweave/pkg/store/inmem"
	"github.com/weaveplatform/artifactweave/pkg/types"
)

type fixture struct {
	t     *testing.T
	h     http.Handler
	db    *inmem.Store
	items *item.Service
	repo  repo.Config
}

func newFixture(t *testing.T) *fixture {
	t.Helper()
	db := inmem.New()
	blobs, err := fs.New(t.TempDir())
	if err != nil {
		t.Fatal(err)
	}
	c := *repo.Defaults(&repo.Config{Key: "skills-local", Class: repo.Local, PackageType: repo.Skills})
	if err := db.Repos().Create(t.Context(), c); err != nil {
		t.Fatal(err)
	}
	if err := db.Permissions().Create(t.Context(), types.Permission{
		Name: "skills-writer",
		Resources: map[types.ResourceType]types.Resource{types.ResourceArtifact: {
			Actions: types.Grants{Users: map[string]types.Actions{
				"writer": types.ActionWrite,
				"reader": types.ActionRead,
			}},
			Targets: map[string]types.Target{c.Key: {Include: []string{"**"}}},
		}},
	}); err != nil {
		t.Fatal(err)
	}
	items := &item.Service{Store: db, Blobs: blobs}
	router := chi.NewRouter()
	router.Use(func(next http.Handler) http.Handler {
		return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
			name := r.Header.Get("X-User")
			p := auth.Principal{Name: name, Admin: name == "admin"}
			if name == "" {
				p = auth.Anonymous
			}
			p, err := auth.Authorize(r.Context(), authz.Sources{
				Permissions: db.Permissions(), Groups: db.Groups(),
			}, p)
			if err != nil {
				t.Fatal(err)
			}
			next.ServeHTTP(w, r.WithContext(auth.WithPrincipal(r.Context(), p)))
		})
	})
	pkgroute.Mount(router, pkgroute.Options{
		Store: db, Items: func() *item.Service { return items },
		ItemError: func(_ context.Context, _ string, err error) error {
			var rejected *format.Rejection
			var overwrite *item.OverwriteDeniedError
			switch {
			case errors.As(err, &rejected):
				return apierror.New(rejected.Status, rejected.Message)
			case errors.As(err, &overwrite):
				return apierror.New(http.StatusForbidden, overwrite.Error())
			default:
				return err
			}
		},
	}, skills.Protocol())
	return &fixture{t: t, h: router, db: db, items: items, repo: c}
}

func publishBody(t *testing.T, payload string, files map[string]string) ([]byte, string) {
	t.Helper()
	var body bytes.Buffer
	mw := multipart.NewWriter(&body)
	w, err := mw.CreateFormField("payload")
	if err != nil {
		t.Fatal(err)
	}
	_, _ = io.WriteString(w, payload)
	for name, content := range files {
		w, err := mw.CreateFormFile("files[]", name)
		if err != nil {
			t.Fatal(err)
		}
		_, _ = io.WriteString(w, content)
	}
	if err := mw.Close(); err != nil {
		t.Fatal(err)
	}
	return body.Bytes(), mw.FormDataContentType()
}

func (f *fixture) publish(as, payload string, files map[string]string) *httptest.ResponseRecorder {
	f.t.Helper()
	data, contentType := publishBody(f.t, payload, files)
	r := httptest.NewRequest(http.MethodPost,
		"/artifactory/api/skills/skills-local/api/v1/skills", bytes.NewReader(data))
	r.Header.Set("Content-Type", contentType)
	r.Header.Set("X-User", as)
	rec := httptest.NewRecorder()
	f.h.ServeHTTP(rec, r)
	return rec
}

func TestPublishIndexesCanonicalBundleAndRebuilds(t *testing.T) {
	f := newFixture(t)
	manifest := "---\nname: Example\ndescription: Runs checks\nauthor: Ada\n" +
		"metadata:\n  skills:\n    tags: [ci, agent]\n---\n# Example\n"
	rec := f.publish("writer",
		`{"slug":"example","version":"1.2.3","displayName":"Example Skill","summary":"Useful"}`,
		map[string]string{"SKILL.md": manifest, "src/run.js": "console.log('ok')\n"})
	if rec.Code != http.StatusCreated {
		t.Fatalf("publish: %d %s", rec.Code, rec.Body)
	}
	p := skillsfmt.Path("example", "1.2.3")
	it, err := f.db.Items().Get(t.Context(), f.repo.Key, false, p)
	if err != nil {
		t.Fatal(err)
	}
	if it.Actual.SHA256 == "" {
		t.Fatal("bundle has no fingerprint")
	}
	props, err := f.db.Items().Properties(t.Context(), it.ID)
	if err != nil {
		t.Fatal(err)
	}
	for key, want := range map[string]string{
		"skill.name": "Example", "skill.version": "1.2.3",
		"skill.author": "Ada", "skill.displayName": "Example Skill",
		"skill.fingerprint": it.Actual.SHA256,
	} {
		if len(props[key]) != 1 || props[key][0] != want {
			t.Errorf("%s = %v, want %q", key, props[key], want)
		}
	}
	pkg, err := f.db.Packages().Find(t.Context(), f.repo.Key, "example")
	if err != nil {
		t.Fatal(err)
	}
	v, err := f.db.Packages().Version(t.Context(), pkg.ID, "1.2.3")
	if err != nil {
		t.Fatal(err)
	}
	var meta skillsfmt.Metadata
	if err := json.Unmarshal(v.Metadata, &meta); err != nil ||
		meta.Fingerprint != it.Actual.SHA256 || meta.Summary != "Useful" {
		t.Fatalf("package metadata: %+v %v", meta, err)
	}
	rc, info, err := f.items.Blobs.Open(t.Context(), it.Actual.SHA256)
	if err != nil {
		t.Fatal(err)
	}
	defer rc.Close()
	data, err := io.ReadAll(rc)
	if err != nil {
		t.Fatal(err)
	}
	zr, err := zip.NewReader(bytes.NewReader(data), info.Size)
	if err != nil || len(zr.File) != 2 || zr.File[0].Name != "SKILL.md" ||
		zr.File[1].Name != "src/run.js" {
		t.Fatalf("noncanonical ZIP: %v %+v", err, zr)
	}
	if err := f.db.Packages().Delete(t.Context(), pkg.ID); err != nil {
		t.Fatal(err)
	}
	if err := (skillsfmt.Handler{}).Reindex(t.Context(), f.items.Env(), f.repo); err != nil {
		t.Fatal(err)
	}
	if _, err := f.db.Packages().Find(t.Context(), f.repo.Key, "example"); err != nil {
		t.Fatalf("reindex did not restore package: %v", err)
	}
}

func TestPublishDenialsAndMalformedInputs(t *testing.T) {
	f := newFixture(t)
	good := `{"slug":"example","version":"1.2.3"}`
	for _, tc := range []struct {
		name, as, payload string
		files             map[string]string
		want              int
	}{
		{"anonymous", "", good, map[string]string{"SKILL.md": "# safe"}, http.StatusUnauthorized},
		{"reader", "reader", good, map[string]string{"SKILL.md": "# safe"}, http.StatusForbidden},
		{
			"invalid slug", "writer", `{"slug":"../bad","version":"1.2.3"}`,
			map[string]string{"SKILL.md": "# safe"},
			http.StatusBadRequest,
		},
		{
			"invalid version", "writer", `{"slug":"example","version":"../1"}`,
			map[string]string{"SKILL.md": "# safe"},
			http.StatusBadRequest,
		},
		{"no files", "writer", good, nil, http.StatusBadRequest},
		{
			"bad YAML", "writer", good,
			map[string]string{"SKILL.md": "---\nname: [\n---\n"},
			http.StatusConflict,
		},
	} {
		t.Run(tc.name, func(t *testing.T) {
			rec := f.publish(tc.as, tc.payload, tc.files)
			if rec.Code != tc.want {
				t.Fatalf("status %d, want %d: %s", rec.Code, tc.want, rec.Body)
			}
		})
	}
	rec := f.publish("writer", good, map[string]string{"SKILL.md": "# safe"})
	if rec.Code != http.StatusCreated {
		t.Fatalf("first publish: %d %s", rec.Code, rec.Body)
	}
	rec = f.publish("writer", good, map[string]string{"SKILL.md": "# different"})
	if rec.Code != http.StatusForbidden {
		t.Fatalf("overwrite without delete grant: %d %s", rec.Code, rec.Body)
	}
	if strings.Contains(rec.Body.String(), "different") {
		t.Fatal("denial exposed uploaded content")
	}
}

func TestSkillsIndexFollowsDeleteRestoreAndCopy(t *testing.T) {
	f := newFixture(t)
	for _, version := range []string{"1.0.0", "2.0.0"} {
		rec := f.publish("admin",
			`{"slug":"example","version":"`+version+`"}`,
			map[string]string{"SKILL.md": "---\nname: Example\n---\n"})
		if rec.Code != http.StatusCreated {
			t.Fatalf("publish %s: %d %s", version, rec.Code, rec.Body)
		}
	}
	pkg, err := f.db.Packages().Find(t.Context(), f.repo.Key, "example")
	if err != nil {
		t.Fatal(err)
	}
	versions, err := f.db.Packages().Versions(t.Context(), pkg.ID)
	if err != nil || len(versions) != 2 {
		t.Fatalf("versions before delete: %v %v", versions, err)
	}
	target := skillsfmt.Path("example", "2.0.0")
	if _, err := f.items.Delete(t.Context(),
		item.Target{Repo: f.repo, Path: target}, "admin"); err != nil {
		t.Fatal(err)
	}
	versions, err = f.db.Packages().Versions(t.Context(), pkg.ID)
	if err != nil || len(versions) != 1 || versions[0].Version != "1.0.0" {
		t.Fatalf("versions after delete: %v %v", versions, err)
	}
	trashed, err := f.db.Trash().Find(t.Context(), f.repo.Key, target)
	if err != nil || len(trashed) != 1 {
		t.Fatalf("trash: %v %v", trashed, err)
	}
	if _, err := f.items.Restore(t.Context(),
		item.Target{Repo: f.repo, Path: target}, trashed[0].Record, "admin"); err != nil {
		t.Fatal(err)
	}
	versions, err = f.db.Packages().Versions(t.Context(), pkg.ID)
	if err != nil || len(versions) != 2 {
		t.Fatalf("versions after restore: %v %v", versions, err)
	}
	other := *repo.Defaults(&repo.Config{
		Key: "skills-copy", Class: repo.Local, PackageType: repo.Skills,
	})
	if err := f.db.Repos().Create(t.Context(), other); err != nil {
		t.Fatal(err)
	}
	result, err := f.items.Transfer(t.Context(), item.Transfer{
		From: item.Target{Repo: f.repo, Path: target},
		To:   item.Target{Repo: other, Path: target},
		By:   "admin", FailFast: true,
	})
	if err != nil || result.Files != 1 || result.Failed != 0 {
		t.Fatalf("copy: %+v %v", result, err)
	}
	if _, err := f.db.Packages().Find(t.Context(), other.Key, "example"); err != nil {
		t.Fatalf("copy not indexed: %v", err)
	}
}

func TestDirectSkillBundleAdmissionUsesTheSameFormatRules(t *testing.T) {
	f := newFixture(t)
	rec := f.publish("admin", `{"slug":"example","version":"1.0.0"}`,
		map[string]string{"SKILL.md": "# safe"})
	if rec.Code != http.StatusCreated {
		t.Fatalf("seed: %d %s", rec.Code, rec.Body)
	}
	it, err := f.db.Items().Get(t.Context(), f.repo.Key, false,
		skillsfmt.Path("example", "1.0.0"))
	if err != nil {
		t.Fatal(err)
	}
	rc, _, err := f.items.Blobs.Open(t.Context(), it.Actual.SHA256)
	if err != nil {
		t.Fatal(err)
	}
	data, err := io.ReadAll(rc)
	_ = rc.Close()
	if err != nil {
		t.Fatal(err)
	}
	for _, tc := range []struct {
		path string
		data []byte
	}{
		{"example/1.0.0/wrong.zip", data},
		{skillsfmt.Path("broken", "1.0.0"), []byte("not a ZIP")},
	} {
		_, err := f.items.Deploy(t.Context(), item.Deploy{
			Target: item.Target{Repo: f.repo, Path: tc.path}, Body: bytes.NewReader(tc.data),
		})
		var reject *format.Rejection
		if !errors.As(err, &reject) || reject.Status != http.StatusConflict {
			t.Fatalf("direct deploy %s: %v", tc.path, err)
		}
	}
	if _, err := f.items.Deploy(t.Context(), item.Deploy{
		Target: item.Target{Repo: f.repo, Path: "notes.txt"}, Body: strings.NewReader("notes"),
	}); err != nil {
		t.Fatalf("ordinary item should remain allowed: %v", err)
	}
	if _, err := f.db.Packages().Find(t.Context(), f.repo.Key, "notes"); err == nil {
		t.Fatal("ordinary item appeared in the Skills package index")
	}
}
