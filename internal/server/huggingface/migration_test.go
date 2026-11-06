package huggingface_test

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/http"
	"net/http/httptest"
	"os"
	"os/exec"
	"path/filepath"
	"slices"
	"strings"
	"sync/atomic"
	"testing"
	"time"

	hfformat "github.com/weaveplatform/artifactweave/internal/format/huggingface"
	"github.com/weaveplatform/artifactweave/internal/source"
	"github.com/weaveplatform/artifactweave/migration/artifacts/api/repo"
	"github.com/weaveplatform/artifactweave/migration/artifacts/core/format"
	"github.com/weaveplatform/artifactweave/migration/platform/blob"
	"github.com/weaveplatform/artifactweave/pkg/store"
	"github.com/weaveplatform/artifactweave/pkg/types"
)

const migrationEndpoint = "api/artifactweave/migrations/legacy-layout"

func migrationBody(kind, timestamp, revision, ref string, dry bool) []byte {
	b, _ := json.Marshal(map[string]any{
		"layout": "jfrog-7.77-timestamp-v1", "kind": kind,
		"owner": "acme", "name": "demo", "source_ref": "main", "timestamp": timestamp,
		"revision": revision, "ref": ref, "dry_run": dry,
	})
	return b
}

func (f *fixture) legacyItem(path string, content []byte, props types.Properties) types.Item {
	f.t.Helper()
	upload, err := f.items.Blobs.BeginUpload(f.t.Context())
	if err != nil {
		f.t.Fatal(err)
	}
	if _, err = upload.Append(f.t.Context(), bytes.NewReader(content)); err != nil {
		f.t.Fatal(err)
	}
	info, err := upload.Commit(f.t.Context(), blob.Digests{})
	if err != nil {
		f.t.Fatal(err)
	}
	it, err := f.db.Items().Put(f.t.Context(), types.Item{
		RepoKey: "hf", Name: path,
		Size: info.Size, Created: time.Unix(1700000000, 0),
		Actual: types.Checksums{
			SHA256: info.Digests.SHA256, SHA1: info.Digests.SHA1,
			SHA512: info.Digests.SHA512, MD5: info.Digests.MD5,
		},
	})
	if err != nil {
		f.t.Fatal(err)
	}
	if err = f.db.Items().
		SetProperties(f.t.Context(), it.ID, props, time.Unix(1700000000, 0)); err != nil {
		f.t.Fatal(err)
	}
	return it
}

func (f *fixture) legacyRevision(kind, timestamp, revision string, large bool) map[string][]byte {
	f.t.Helper()
	metadata := "model"
	if kind == "datasets" {
		metadata = "dataset"
	}
	prefix := kind + "/acme/demo/main/" + timestamp + "/"
	f.legacyItem(prefix+".jfrog_huggingface_"+metadata+"_info.json", []byte(`{}`),
		types.Properties{"huggingfaceml.generated.revision.sha1": {revision}})
	files := map[string][]byte{
		"config.json":        []byte(`{"model_type":"bert"}`),
		"sub/tokenizer.json": []byte(`{"version":"` + timestamp + `"}`),
	}
	if large {
		files["weights.bin"] = bytes.Repeat([]byte("migration-weights"), 100000)
	}
	for name, content := range files {
		f.legacyItem(prefix+name, content, types.Properties{"fixture": {"preserved"}})
	}
	return files
}

func (f *fixture) migrate(body []byte, want int) map[string]any {
	f.t.Helper()
	r := f.request(http.MethodPost, migrationEndpoint, "application/json", body)
	b := readResponse(f.t, r)
	if r.StatusCode != want {
		f.t.Fatalf("migration: %d, want %d: %s", r.StatusCode, want, b)
	}
	var status map[string]any
	if err := json.Unmarshal(b, &status); err != nil {
		f.t.Fatal(err)
	}
	return status
}

func TestAccLegacyMigrationPreservesRevisionsBytesPropertiesAndPolicy(t *testing.T) {
	for _, class := range []repo.Class{repo.Local, repo.Federated} {
		for _, kind := range []string{"models", "datasets"} {
			t.Run(string(class)+"/"+kind, func(t *testing.T) {
				f := newFixture(t, class)
				if err := f.db.Permissions().
					Create(t.Context(), types.Permission{Name: "readers", Resources: map[types.ResourceType]types.Resource{
						types.ResourceArtifact: {
							Actions: types.Grants{
								Users: map[string]types.Actions{"reader": types.ActionRead},
							},
							Targets: map[string]types.Target{"hf": {Include: []string{"**"}}},
						},
					}}); err != nil {
					t.Fatal(err)
				}
				for n, revision := range []string{sha, strings.Repeat("a", 40)} {
					timestamp := fmt.Sprint(1700000000 + n)
					files := f.legacyRevision(kind, timestamp, revision, true)
					ref := ""
					if n == 0 {
						ref = "main"
					}
					plan := f.migrate(migrationBody(kind, timestamp, revision, ref, true), 200)
					if plan["state"] != "ready" || plan["published_files"] != float64(0) {
						t.Fatalf("plan: %v", plan)
					}
					if _, err := f.db.Packages().
						Find(t.Context(), "hf", kind+"/acme/demo"); n == 0 &&
						!errors.Is(err, store.ErrNotFound) {
						t.Fatalf("dry run indexed content: %v", err)
					}
					for attempt := 0; attempt < 2; attempt++ {
						status := f.migrate(
							migrationBody(kind, timestamp, revision, ref, false),
							200,
						)
						if status["state"] != "complete" ||
							status["published_files"] != float64(len(files)) ||
							status["verified_files"] != float64(len(files)) {
							t.Fatalf("status: %v", status)
						}
					}
					pkg, err := f.db.Packages().Find(t.Context(), "hf", kind+"/acme/demo")
					if err != nil {
						t.Fatal(err)
					}
					version, err := f.db.Packages().Version(t.Context(), pkg.ID, revision)
					if err != nil || !bytes.Contains(version.Metadata, []byte(revision)) {
						t.Fatalf("version metadata: %v %s", err, version.Metadata)
					}
					for name, want := range files {
						path := kind + "--acme--demo/snapshots/" + revision + "/" + name
						dest, err := f.db.Items().Get(t.Context(), "hf", false, path)
						if err != nil {
							t.Fatal(err)
						}
						source, err := f.db.Items().
							Get(t.Context(), "hf", false, kind+"/acme/demo/main/"+timestamp+"/"+name)
						if err != nil || source.Actual != dest.Actual || source.Size != dest.Size {
							t.Fatalf("source changed: %v", err)
						}
						props, err := f.db.Items().Properties(t.Context(), dest.ID)
						if err != nil || !props.Has("fixture", "preserved") {
							t.Fatalf("properties: %v %v", props, err)
						}
						resolve := "acme/demo/resolve/" + revision + "/" + name
						if kind == "datasets" {
							resolve = "datasets/" + resolve
						}
						for _, user := range []string{"reader", "intruder"} {
							r := f.requestAs("GET", resolve, "", nil, user)
							b := readResponse(t, r)
							if user == "reader" && (r.StatusCode != 200 || !bytes.Equal(b, want)) ||
								user == "intruder" && r.StatusCode != 404 {
								t.Fatalf("%s resolve: %d", user, r.StatusCode)
							}
						}
					}
				}
			})
		}
	}
}

func TestAccLegacyMigrationRejectsUnsafeMappingsAndContent(t *testing.T) {
	for _, tc := range []struct {
		name   string
		mutate func(*testing.T, *fixture, map[string]any)
	}{
		{"unknown layout", func(_ *testing.T, _ *fixture, body map[string]any) { body["layout"] = "beta" }},
		{"ownerless", func(_ *testing.T, _ *fixture, body map[string]any) { body["owner"] = "" }},
		{"invalid revision", func(_ *testing.T, _ *fixture, body map[string]any) { body["revision"] = "main" }},
		{"invalid timestamp", func(_ *testing.T, _ *fixture, body map[string]any) { body["timestamp"] = "../escape" }},
		{"invalid ref", func(_ *testing.T, _ *fixture, body map[string]any) { body["ref"] = sha }},
		{"revision differs", func(_ *testing.T, _ *fixture, body map[string]any) { body["revision"] = strings.Repeat("f", 40) }},
		{"missing metadata", func(t *testing.T, f *fixture, _ map[string]any) {
			_, err := f.db.Items().Delete(t.Context(), "hf", false, "models/acme/demo/main/1700000000/.jfrog_huggingface_model_info.json")
			if err != nil {
				t.Fatal(err)
			}
		}},
		{"corrupt size", func(t *testing.T, f *fixture, _ map[string]any) {
			it, _ := f.db.Items().Get(t.Context(), "hf", false, "models/acme/demo/main/1700000000/config.json")
			it.Size++
			if _, err := f.db.Items().Put(t.Context(), it); err != nil {
				t.Fatal(err)
			}
		}},
		{"corrupt sha1", func(t *testing.T, f *fixture, _ map[string]any) {
			it, _ := f.db.Items().Get(t.Context(), "hf", false, "models/acme/demo/main/1700000000/config.json")
			it.Actual.SHA1 = strings.Repeat("f", 40)
			if _, err := f.db.Items().Put(t.Context(), it); err != nil {
				t.Fatal(err)
			}
		}},
		{"unsupported metadata", func(_ *testing.T, f *fixture, _ map[string]any) {
			f.legacyItem("models/acme/demo/main/1700000000/.jfrog_unknown", []byte("x"), nil)
		}},
		{"byte bound", func(t *testing.T, f *fixture, _ map[string]any) {
			it, _ := f.db.Items().Get(t.Context(), "hf", false, "models/acme/demo/main/1700000000/config.json")
			it.Size = 17 << 30
			if _, err := f.db.Items().Put(t.Context(), it); err != nil {
				t.Fatal(err)
			}
		}},
		{"partial destination", func(_ *testing.T, f *fixture, _ map[string]any) {
			f.legacyItem("models--acme--demo/snapshots/"+sha+"/config.json", []byte(`{}`), nil)
		}},
		{"conflicting ref", func(_ *testing.T, f *fixture, _ map[string]any) {
			f.legacyItem("models--acme--demo/refs/main", []byte(strings.Repeat("f", 40)), nil)
		}},
	} {
		t.Run(tc.name, func(t *testing.T) {
			f := newFixture(t, repo.Local)
			f.legacyRevision("models", "1700000000", sha, false)
			var body map[string]any
			if err := json.Unmarshal(
				migrationBody("models", "1700000000", sha, "main", false),
				&body,
			); err != nil {
				t.Fatal(err)
			}
			tc.mutate(t, f, body)
			data, _ := json.Marshal(body)
			r := f.request("POST", migrationEndpoint, "application/json", data)
			response := readResponse(t, r)
			if r.StatusCode != 400 && r.StatusCode != 409 {
				t.Fatalf("unsafe migration: %d %s", r.StatusCode, response)
			}
			if _, err := f.db.Packages().
				Find(t.Context(), "hf", "models/acme/demo"); !errors.Is(
				err,
				store.ErrNotFound,
			) {
				t.Fatalf("failed migration indexed revision: %v", err)
			}
		})
	}
}

type interruptedMigration struct {
	hfformat.Handler
	fail string
}

func (h interruptedMigration) GuardWrite(
	_ context.Context,
	_ format.Env,
	_ repo.Config,
	w format.Write,
) error {
	if strings.HasSuffix(w.Path, h.fail) {
		return fmt.Errorf("migration interruption: %w", context.Canceled)
	}
	return nil
}

func TestAccLegacyMigrationRollsBackInterruptedPublication(t *testing.T) {
	f := newFixture(t, repo.Local)
	f.legacyRevision("models", "1700000000", sha, false)
	f.items.Formats = func(repo.PackageType) format.Handler { return interruptedMigration{fail: "sub/tokenizer.json"} }
	f.migrate(migrationBody("models", "1700000000", sha, "main", false), 500)
	items, _, err := f.db.Items().
		Descendants(t.Context(), "hf", false, "models--acme--demo", store.Page{})
	if err != nil || len(items) != 0 {
		t.Fatalf("interrupted publication leaked items: %d %v", len(items), err)
	}
	if _, err = f.db.Packages().
		Find(t.Context(), "hf", "models/acme/demo"); !errors.Is(
		err,
		store.ErrNotFound,
	) {
		t.Fatalf("interrupted index: %v", err)
	}
	f.items.Formats = func(repo.PackageType) format.Handler { return hfformat.Handler{} }
	f.migrate(migrationBody("models", "1700000000", sha, "main", false), 200)
}

func TestAccLegacyMigrationRefusesPermissionChanges(t *testing.T) {
	for _, patterns := range []types.Target{
		{Include: []string{"models/acme/demo/**"}},
		{Include: []string{"models--acme--demo/**"}},
		{Include: []string{"models--acme--demo/**"}, Exclude: []string{"", " "}},
		{Include: []string{" models--acme--demo/** "}},
		{Include: []string{"**"}, Exclude: []string{"models/acme/demo/**"}},
		{Include: []string{"**"}, Exclude: []string{"models--acme--demo/**"}},
	} {
		t.Run(fmt.Sprint(patterns), func(t *testing.T) {
			f := newFixture(t, repo.Local)
			f.legacyRevision("models", "1700000000", sha, false)
			if err := f.db.Permissions().
				Create(t.Context(), types.Permission{Name: "scoped", Resources: map[types.ResourceType]types.Resource{
					types.ResourceArtifact: {
						Actions: types.Grants{
							Groups: map[string]types.Actions{"readers": types.ActionRead},
						},
						Targets: map[string]types.Target{types.TargetAnyLocal: patterns},
					},
				}}); err != nil {
				t.Fatal(err)
			}
			f.migrate(migrationBody("models", "1700000000", sha, "main", false), 409)
		})
	}
}

func TestAccLegacyMigrationRefusesPointerOnlyExports(t *testing.T) {
	f := newFixture(t, repo.Local)
	f.legacyRevision("models", "1700000000", sha, false)
	pointer := []byte(
		"version https://git-lfs.github.com/spec/v1\noid sha256:" + strings.Repeat(
			"f",
			64,
		) + "\nsize 1048577\n",
	)
	f.legacyItem("models/acme/demo/main/1700000000/weights.bin", pointer, nil)
	for _, dry := range []bool{true, false} {
		f.migrate(migrationBody("models", "1700000000", sha, "main", dry), 409)
	}
	items, _, err := f.db.Items().
		Descendants(t.Context(), "hf", false, "models--acme--demo", store.Page{})
	if err != nil || len(items) != 0 {
		t.Fatalf("pointer-only migration published destination: %d %v", len(items), err)
	}
}

type mutationDuringMigration struct {
	blob.Store
	mutate func(context.Context) error
}

func (b *mutationDuringMigration) Open(
	ctx context.Context,
	digest string,
) (io.ReadSeekCloser, blob.Info, error) {
	if b.mutate != nil {
		mutate := b.mutate
		b.mutate = nil
		if err := mutate(ctx); err != nil {
			return nil, blob.Info{}, err
		}
	}
	return b.Store.Open(ctx, digest)
}

func TestAccLegacyMigrationRechecksChangesDuringVerification(t *testing.T) {
	for _, change := range []string{"source", "source properties", "source added", "destination", "policy", "repository"} {
		t.Run(change, func(t *testing.T) {
			f := newFixture(t, repo.Local)
			f.legacyRevision("models", "1700000000", sha, false)
			f.items.Blobs = &mutationDuringMigration{
				Store: f.items.Blobs,
				mutate: func(ctx context.Context) error {
					it, err := f.db.Items().
						Get(ctx, "hf", false, "models/acme/demo/main/1700000000/config.json")
					if err != nil {
						return err
					}
					switch change {
					case "source":
						it.Size++
						_, err = f.db.Items().Put(ctx, it)
					case "source properties":
						err = f.db.Items().
							SetProperties(ctx, it.ID, types.Properties{"changed": {"true"}}, time.Now())
					case "source added":
						it.ID, it.Name = 0, "unexpected.txt"
						_, err = f.db.Items().Put(ctx, it)
					case "destination":
						it.ID, it.Path = 0, "models--acme--demo/snapshots/"+sha
						_, err = f.db.Items().Put(ctx, it)
					case "policy":
						err = f.db.Permissions().
							Create(ctx, types.Permission{Name: "late grant", Resources: map[types.ResourceType]types.Resource{
								types.ResourceArtifact: {
									Actions: types.Grants{
										Users: map[string]types.Actions{
											"intruder": types.ActionRead,
										},
									},
									Targets: map[string]types.Target{
										"hf": {Include: []string{"models--acme--demo/**"}},
									},
								},
							}})
					case "repository":
						c, getErr := f.db.Repos().Get(ctx, "hf")
						if getErr != nil {
							return getErr
						}
						blocked := true
						c.BlackedOut = &blocked
						err = f.db.Repos().Update(ctx, c)
					}
					return err
				},
			}
			f.migrate(migrationBody("models", "1700000000", sha, "main", false), 409)
			items, _, err := f.db.Items().
				Descendants(t.Context(), "hf", false, "models--acme--demo", store.Page{})
			if err != nil || len(items) != 0 {
				t.Fatalf("conflict exposed a partial migration: %d %v", len(items), err)
			}
		})
	}
}

func TestAccLegacyMigrationBoundsAuthorityAndRerunConflicts(t *testing.T) {
	t.Run("authority and request", func(t *testing.T) {
		f := newFixture(t, repo.Local)
		for _, tc := range []struct {
			user, body, media, query string
			status                   int
		}{
			{"intruder", `{}`, "application/json", "", 403},
			{"", `{}`, "text/plain", "", 400},
			{"", `{"unknown":true}`, "application/json", "", 400},
			{"", string(migrationBody("models", "1700000000", sha, "", true)), "application/json", "?unknown=true", 400},
		} {
			r := f.requestAs("POST", migrationEndpoint+tc.query, tc.media, []byte(tc.body), tc.user)
			body := readResponse(t, r)
			if r.StatusCode != tc.status {
				t.Fatalf("request: %d %s", r.StatusCode, body)
			}
		}
	})
	t.Run("remote", func(t *testing.T) {
		f := newFixture(t, repo.Virtual)
		f.migrate(migrationBody("models", "1700000000", sha, "", false), 409)
	})
	t.Run("tree bound", func(t *testing.T) {
		f := newFixture(t, repo.Local)
		f.legacyRevision("models", "1700000000", sha, false)
		for n := 0; n < 1025; n++ {
			if _, err := f.db.Items().
				Put(t.Context(), types.Item{RepoKey: "hf", Name: fmt.Sprintf("models/acme/demo/main/1700000000/folder-%04d", n), Folder: true}); err != nil {
				t.Fatal(err)
			}
		}
		f.migrate(migrationBody("models", "1700000000", sha, "", false), 413)
	})
	t.Run("permission bound", func(t *testing.T) {
		f := newFixture(t, repo.Local)
		f.legacyRevision("models", "1700000000", sha, false)
		for n := 0; n < 1025; n++ {
			if err := f.db.Permissions().
				Create(t.Context(), types.Permission{Name: fmt.Sprint(n)}); err != nil {
				t.Fatal(err)
			}
		}
		f.migrate(migrationBody("models", "1700000000", sha, "", false), 413)
	})
	t.Run("advanced ref is retained", func(t *testing.T) {
		f := newFixture(t, repo.Local)
		f.legacyRevision("models", "1700000000", sha, false)
		f.migrate(migrationBody("models", "1700000000", sha, "main", false), 200)
		f.legacyItem("models--acme--demo/refs/main", []byte(strings.Repeat("f", 40)), nil)
		f.migrate(migrationBody("models", "1700000000", sha, "main", false), 200)
		it, err := f.db.Items().Get(t.Context(), "hf", false, "models--acme--demo/refs/main")
		if err != nil {
			t.Fatal(err)
		}
		reader, _, err := f.items.Blobs.Open(t.Context(), it.Actual.SHA256)
		if err != nil {
			t.Fatal(err)
		}
		defer reader.Close()
		data, err := io.ReadAll(reader)
		if err != nil || string(data) != strings.Repeat("f", 40) {
			t.Fatalf("rerun rewound ref: %s %v", data, err)
		}
	})
	for _, conflict := range []string{"bytes", "properties", "extra file"} {
		t.Run(conflict, func(t *testing.T) {
			f := newFixture(t, repo.Local)
			f.legacyRevision("models", "1700000000", sha, false)
			f.migrate(migrationBody("models", "1700000000", sha, "", false), 200)
			path := "models--acme--demo/snapshots/" + sha + "/config.json"
			switch conflict {
			case "bytes":
				f.legacyItem(path, []byte("changed"), nil)
			case "properties":
				it, _ := f.db.Items().Get(t.Context(), "hf", false, path)
				if err := f.db.Items().
					SetProperties(t.Context(), it.ID, nil, time.Now()); err != nil {
					t.Fatal(err)
				}
			case "extra file":
				f.legacyItem("models--acme--demo/snapshots/"+sha+"/extra", []byte("extra"), nil)
			}
			f.migrate(migrationBody("models", "1700000000", sha, "", false), 409)
		})
	}
}

func TestAccPinnedClientReadsMigratedLegacyRevisions(t *testing.T) {
	python := os.Getenv("HF_CLIENT_PYTHON")
	if python == "" {
		t.Skip("set HF_CLIENT_PYTHON to the pinned Hugging Face client environment")
	}
	f := newFixture(t, repo.Local, func(f *fixture) *source.Service {
		f.xetEnabled = true
		return nil
	})
	base := f.server.Config.Handler
	var casReads atomic.Int64
	f.server = httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if strings.Contains(r.URL.Path, "/xet/") && strings.Contains(r.URL.Path, "/data/") {
			casReads.Add(1)
		}
		if r.Method == http.MethodGet && strings.Contains(r.URL.Path, "/resolve/") &&
			strings.HasSuffix(r.URL.Path, "/weights.bin") {
			http.Error(
				w,
				"native migration proof requires preserved Xet records",
				http.StatusBadGateway,
			)
			return
		}
		base.ServeHTTP(w, r)
	}))
	t.Cleanup(f.server.Close)
	seedScript := `import os,sys
from huggingface_hub import HfApi
api=HfApi(endpoint=os.environ['HF_ENDPOINT'],token='fixture-token')
with open(sys.argv[1],'wb') as output: output.write(b'migration-weights'*100000)
api.upload_file(repo_id='acme/demo',path_in_repo='weights.bin',path_or_fileobj=sys.argv[1])
`
	seed := exec.CommandContext(
		t.Context(),
		python,
		"-c",
		seedScript,
		filepath.Join(t.TempDir(), "weights.bin"),
	)
	seed.Env = append(
		os.Environ(),
		"HF_ENDPOINT="+f.server.URL+"/artifactory/api/huggingfaceml/hf",
		"HF_HOME="+filepath.Join(
			t.TempDir(),
			"seed-home",
		),
		"HF_XET_CACHE="+filepath.Join(t.TempDir(), "seed-xet"),
		"HF_HUB_DISABLE_XET=0",
		"HF_HUB_DISABLE_TELEMETRY=1",
	)
	if output, err := seed.CombinedOutput(); err != nil {
		t.Fatalf("native Xet seed: %v\n%s", err, output)
	}
	before, _, err := f.db.Items().
		Descendants(t.Context(), "hf", false, "models--acme--demo/.xet", store.Page{})
	if err != nil || len(before) == 0 {
		t.Fatalf("native seed did not publish Xet records: %v", err)
	}
	for n, revision := range []string{sha, strings.Repeat("a", 40)} {
		timestamp := fmt.Sprint(1700000000 + n)
		f.legacyRevision("models", timestamp, revision, true)
		f.migrate(migrationBody("models", timestamp, revision, "", false), 200)
	}
	after, _, err := f.db.Items().
		Descendants(t.Context(), "hf", false, "models--acme--demo/.xet", store.Page{})
	if err != nil || !slices.Equal(before, after) {
		t.Fatalf("migration changed existing Xet references: %v", err)
	}
	head := f.request(http.MethodHead, "acme/demo/resolve/"+sha+"/weights.bin", "", nil)
	readResponse(t, head)
	if head.StatusCode != http.StatusOK || head.Header.Get("X-Xet-Hash") == "" {
		t.Fatalf("migrated file lost Xet metadata: status=%d", head.StatusCode)
	}
	// CI setup owns the client version and checksum policy; this scenario logs
	// the installed versions and verifies migration behavior across upgrades.
	script := `import os,sys
from importlib.metadata import version
import huggingface_hub
from huggingface_hub import HfApi,snapshot_download
from huggingface_hub.errors import HfHubHTTPError
print('huggingface_hub',huggingface_hub.__version__,'hf-xet',version('hf-xet'))
api=HfApi(endpoint=os.environ['HF_ENDPOINT'],token='fixture-token')
for n,revision in enumerate(sys.argv[2:]):
 info=api.model_info('acme/demo',revision=revision)
 assert info.sha == revision
 out=snapshot_download(repo_id='acme/demo',revision=revision,endpoint=os.environ['HF_ENDPOINT'],token='fixture-token',local_dir=os.path.join(sys.argv[1],str(n)),max_workers=1)
 assert open(os.path.join(out,'config.json'),'rb').read()==b'{"model_type":"bert"}'
 assert open(os.path.join(out,'weights.bin'),'rb').read()==b'migration-weights'*100000
 assert open(os.path.join(out,'sub/tokenizer.json'),'rb').read()==('{"version":"'+str(1700000000+n)+'"}').encode()
denied=HfApi(endpoint=os.environ['HF_ENDPOINT'],token='fixture-token',headers={'X-User':'intruder'})
try:
 denied.model_info('acme/demo',revision=sys.argv[2])
except HfHubHTTPError as error:
 assert error.response.status_code == 404
else:
 raise AssertionError('denied native principal read migrated metadata')
`
	cmd := exec.CommandContext(
		t.Context(),
		python,
		"-c",
		script,
		filepath.Join(t.TempDir(), "downloads"),
		sha,
		strings.Repeat("a", 40),
	)
	cmd.Env = append(
		os.Environ(),
		"HF_ENDPOINT="+f.server.URL+"/artifactory/api/huggingfaceml/hf",
		"HF_HOME="+filepath.Join(
			t.TempDir(),
			"home",
		),
		"HF_XET_CACHE="+filepath.Join(t.TempDir(), "read-xet"),
		"HF_HUB_CACHE="+t.TempDir(),
		"HF_HUB_DISABLE_XET=0",
		"HF_HUB_DISABLE_TELEMETRY=1",
	)
	output, err := cmd.CombinedOutput()
	if err != nil {
		t.Fatalf("native post-migration read: %v\n%s", err, output)
	}
	t.Logf("native migration client output:\n%s", output)
	if casReads.Load() == 0 {
		t.Fatal("migrated large-file read did not use the preserved Xet records")
	}
}
