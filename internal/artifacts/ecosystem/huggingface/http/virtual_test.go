package huggingfacehttp_test

import (
	"bytes"
	"crypto/sha1"
	"crypto/sha256"
	"encoding/base64"
	"encoding/hex"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync/atomic"
	"testing"

	"github.com/go-chi/chi/v5"

	"github.com/weaveplatform/artifactweave/internal/artifacts/api/repo"
	"github.com/weaveplatform/artifactweave/internal/artifacts/core/remote/source"
	"github.com/weaveplatform/artifactweave/internal/kernel"
	"github.com/weaveplatform/artifactweave/internal/platform/httpkit/apierror"
)

func TestUnitVirtualKeepsOneRevisionMember(t *testing.T) {
	f := newFixture(t, repo.Virtual, func(f *fixture) *source.Service {
		for _, key := range []string{"empty", "first", "second"} {
			c := *repo.Defaults(&repo.Config{Key: key, Class: repo.Local, PackageType: repo.HuggingFaceML})
			if e := f.db.Repos().Create(t.Context(), c); e != nil {
				t.Fatal(e)
			}
		}
		c, e := f.db.Repos().Get(t.Context(), "hf")
		if e != nil {
			t.Fatal(e)
		}
		c.Repositories = []string{"removed", "empty", "first", "second"}
		if e := f.db.Repos().Update(t.Context(), c); e != nil {
			t.Fatal(e)
		}
		return nil
	})
	commit := func(key, file string) {
		t.Helper()
		body := `{"key":"header","value":{"summary":"publish"}}` + "\n" + `{"key":"file","value":{"path":"` + file + `","content":"` + base64.StdEncoding.EncodeToString(
			[]byte(file),
		) + `","encoding":"base64"}}` + "\n"
		req, e := http.NewRequest(
			"POST",
			f.server.URL+"/artifactory/api/huggingfaceml/"+key+"/api/models/acme/demo/commit/"+sha,
			bytes.NewBufferString(body),
		)
		if e != nil {
			t.Fatal(e)
		}
		req.Header.Set("Content-Type", "application/x-ndjson")
		r, e := f.server.Client().Do(req)
		if e != nil {
			t.Fatal(e)
		}
		b := readResponse(t, r)
		if r.StatusCode != 200 {
			t.Fatalf("%s commit %d %s", key, r.StatusCode, b)
		}
	}
	commit("first", "one.txt")
	commit("second", "two.txt")
	r := f.request("GET", "api/models/acme/demo/revision/"+sha, "", nil)
	b := readResponse(t, r)
	if r.StatusCode != 200 || !bytes.Contains(b, []byte("one.txt")) ||
		bytes.Contains(b, []byte("two.txt")) {
		t.Fatalf("virtual info %d %s", r.StatusCode, b)
	}
	r = f.request("GET", "acme/demo/resolve/"+sha+"/one.txt", "", nil)
	b = readResponse(t, r)
	if r.StatusCode != 200 || string(b) != "one.txt" {
		t.Fatalf("first member %d %s", r.StatusCode, b)
	}
	r = f.request("GET", "acme/demo/resolve/"+sha+"/two.txt", "", nil)
	_, _ = io.Copy(io.Discard, r.Body)
	_ = r.Body.Close()
	if r.StatusCode != 404 {
		t.Fatalf("mixed second member: %d", r.StatusCode)
	}
}

func TestUnitVirtualRejectsAbsentAndWrongFormatMembers(t *testing.T) {
	for _, tc := range []struct {
		name            string
		members         []string
		createWrongType bool
		expected        int
	}{
		{"empty virtual", nil, false, 404},
		{"removed member", []string{"removed"}, false, 404},
		// A member of another type is passed over (decision 0008, R11),
		// where it answered 400 "invalid Hugging Face identity".
		{"wrong package type", []string{"wrong"}, true, 404},
	} {
		t.Run(tc.name, func(t *testing.T) {
			f := newFixture(t, repo.Virtual, func(f *fixture) *source.Service {
				if tc.createWrongType {
					wrong := *repo.Defaults(&repo.Config{Key: "wrong", Class: repo.Local, PackageType: repo.NPM})
					if err := f.db.Repos().Create(t.Context(), wrong); err != nil {
						t.Fatal(err)
					}
				}
				virtual, err := f.db.Repos().Get(t.Context(), "hf")
				if err != nil {
					t.Fatal(err)
				}
				virtual.Repositories = tc.members
				if err := f.db.Repos().Update(t.Context(), virtual); err != nil {
					t.Fatal(err)
				}
				return nil
			})
			r := f.request("GET", "api/models/acme/demo/revision/main", "", nil)
			b := readResponse(t, r)
			if r.StatusCode != tc.expected {
				t.Fatalf("virtual member policy: %d %s", r.StatusCode, b)
			}
		})
	}
}

// commitFile publishes file, whose content is its name, to the model
// acme/demo in the repository key as the administrator, at the revision
// sha.
func commitFile(t *testing.T, f *fixture, key, file string) {
	t.Helper()
	body := `{"key":"header","value":{"summary":"publish"}}` + "\n" +
		`{"key":"file","value":{"path":"` + file + `","content":"` +
		base64.StdEncoding.EncodeToString([]byte(file)) + `","encoding":"base64"}}` + "\n"
	req, e := http.NewRequest(http.MethodPost,
		f.server.URL+"/artifactory/api/huggingfaceml/"+key+"/api/models/acme/demo/commit/"+sha,
		bytes.NewBufferString(body))
	if e != nil {
		t.Fatal(e)
	}
	req.Header.Set("Content-Type", "application/x-ndjson")
	r, e := f.server.Client().Do(req)
	if e != nil {
		t.Fatal(e)
	}
	if b := readResponse(t, r); r.StatusCode != http.StatusOK {
		t.Fatalf("%s commit %d %s", key, r.StatusCode, b)
	}
}

func createHub(t *testing.T, f *fixture, c repo.Config) {
	t.Helper()
	c.PackageType = repo.HuggingFaceML
	if e := f.db.Repos().Create(t.Context(), *repo.Defaults(&c)); e != nil {
		t.Fatal(e)
	}
}

func patchHub(t *testing.T, f *fixture, key string, change func(*repo.Config)) {
	t.Helper()
	c, e := f.db.Repos().Get(t.Context(), key)
	if e != nil {
		t.Fatal(e)
	}
	change(&c)
	if e := f.db.Repos().Update(t.Context(), c); e != nil {
		t.Fatal(e)
	}
}

// hubAnswer is one answer through the virtual repository hf.
type hubAnswer struct {
	status int
	header http.Header
	body   string
}

func hubGet(t *testing.T, f *fixture, path, user string) hubAnswer {
	t.Helper()
	r := f.requestAs(http.MethodGet, path, "", nil, user)
	b := readResponse(t, r)
	return hubAnswer{status: r.StatusCode, header: r.Header, body: string(b)}
}

func wantHub(t *testing.T, got hubAnswer, status int, contains string) {
	t.Helper()
	if got.status != status || !strings.Contains(got.body, contains) {
		t.Fatalf("answer: %d %s, want %d %q", got.status, got.body, status, contains)
	}
}

// hubUpstream is a Hub serving revision sha of the model acme/demo with the
// one file config.json, or 502 for every request while offline is set.
func hubUpstream(t *testing.T, content []byte, offline *atomic.Bool) *httptest.Server {
	t.Helper()
	git := sha1.New() //nolint:gosec // a Git blob ID, which the Hub answers.
	_, _ = fmt.Fprintf(git, "blob %d\x00", len(content))
	_, _ = git.Write(content)
	blobID := hex.EncodeToString(git.Sum(nil))
	r := chi.NewRouter()
	r.Use(func(next http.Handler) http.Handler {
		return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
			if offline.Load() {
				http.Error(w, "upstream offline", http.StatusBadGateway)
				return
			}
			next.ServeHTTP(w, r)
		})
	})
	r.Get("/api/models/acme/demo/revision/{revision}", func(w http.ResponseWriter, _ *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		_ = json.NewEncoder(w).Encode(map[string]any{
			"id": "acme/demo", "sha": sha, "siblings": []map[string]any{
				{"rfilename": "config.json", "size": len(content), "blobId": blobID},
			},
		})
	})
	r.Get("/acme/demo/resolve/{revision}/config.json", func(w http.ResponseWriter, _ *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		_, _ = w.Write(content)
	})
	s := httptest.NewServer(r)
	t.Cleanup(s.Close)
	return s
}

// A virtual repository resolves through a nested virtual member and skips a
// cycle back to itself, a self-membership and a repeated member without an
// error (decision 0008, R2), where each answered 400 "invalid Hugging Face
// identity". A member without the model passes over (R3), and a nested
// virtual repository whose own patterns reject the model's folder is
// dropped from the walk.
func TestUnitVirtualFlattensNestedMembersSkippingCyclesAndDuplicates(t *testing.T) {
	f := newFixture(t, repo.Virtual, func(f *fixture) *source.Service {
		createHub(t, f, repo.Config{Key: "empty", Class: repo.Local})
		createHub(t, f, repo.Config{Key: "first", Class: repo.Local})
		createHub(t, f, repo.Config{
			Key: "inner", Class: repo.Virtual,
			Repositories: []string{"hf", "inner", "empty", "first", "first"},
		})
		patchHub(t, f, "hf", func(c *repo.Config) {
			c.Repositories = []string{"inner", "hf", "inner", "first"}
		})
		return nil
	})
	commitFile(t, f, "first", "one.txt")
	wantHub(t, hubGet(t, f, "api/models/acme/demo/revision/main", ""), http.StatusOK, `"one.txt"`)
	wantHub(t, hubGet(t, f, "acme/demo/resolve/main/one.txt", ""), http.StatusOK, "one.txt")
	excluded := "models--acme--demo/**"
	patchHub(t, f, "inner", func(c *repo.Config) { c.ExcludesPattern = &excluded })
	patchHub(t, f, "hf", func(c *repo.Config) { c.Repositories = []string{"inner"} })
	wantHub(t, hubGet(t, f, "api/models/acme/demo/revision/main", ""), http.StatusNotFound,
		"Hugging Face revision or file not found")
}

// A member whose upstream fails passes over and is logged (decision 0008,
// R3) [I], where the fault ended the walk with its 502: a later remote
// member serves the revision and its file, and a member that merely misses
// is not logged.
func TestUnitVirtualPassesOverAFaultyMemberAndLogsIt(t *testing.T) {
	content := []byte(`{"model_type":"bert"}`)
	var offline, online atomic.Bool
	offline.Store(true)
	down := hubUpstream(t, content, &offline)
	up := hubUpstream(t, content, &online)
	f := newFixture(t, repo.Virtual, func(f *fixture) *source.Service {
		createHub(t, f, repo.Config{Key: "hollow", Class: repo.Local})
		createHub(t, f, repo.Config{Key: "down", Class: repo.Remote, URL: down.URL})
		createHub(t, f, repo.Config{Key: "up", Class: repo.Remote, URL: up.URL})
		patchHub(t, f, "hf", func(c *repo.Config) {
			c.Repositories = []string{"hollow", "down", "up"}
		})
		return remoteService(t, f)
	})
	wantHub(t, hubGet(t, f, "api/models/acme/demo/revision/main", ""), http.StatusOK, `"config.json"`)
	wantHub(t, hubGet(t, f, "acme/demo/resolve/main/config.json", ""), http.StatusOK, string(content))
	logs := f.logs.String()
	if !strings.Contains(logs, "level=WARN") ||
		!strings.Contains(logs, "huggingface: virtual repository member passed over") ||
		!strings.Contains(logs, "virtual=hf member=down ") || strings.Contains(logs, "member=hollow") {
		t.Fatalf("logs: %s", logs)
	}
}

// A Xet download through a nested virtual repository of a model only a
// remote member's upstream holds (decision 0008, R1 and R2): the resolve
// advertises the Xet hash and a refresh route on the virtual repository,
// whose read token names the remote member and serves its reconstruction,
// where the nested member answered 400.
func TestUnitVirtualServesARemoteMembersModelOverXet(t *testing.T) {
	content := bytes.Repeat([]byte("remote Xet through a virtual"), 10000)
	digest := fmt.Sprintf("%x", sha256.Sum256(content))
	r := chi.NewRouter()
	r.Get("/api/models/acme/demo/revision/main", func(w http.ResponseWriter, _ *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		_ = json.NewEncoder(w).Encode(map[string]any{
			"id": "acme/demo", "sha": sha, "siblings": []map[string]any{{
				"rfilename": "weights.bin", "size": len(content),
				"lfs": map[string]any{"sha256": digest, "size": len(content), "pointerSize": 130},
			}},
		})
	})
	r.Get("/acme/demo/resolve/main/weights.bin", func(w http.ResponseWriter, _ *http.Request) {
		_, _ = w.Write(content)
	})
	up := httptest.NewServer(r)
	t.Cleanup(up.Close)
	f := newFixture(t, repo.Virtual, func(f *fixture) *source.Service {
		f.xetEnabled = true
		createHub(t, f, repo.Config{Key: "local", Class: repo.Local})
		createHub(t, f, repo.Config{Key: "up", Class: repo.Remote, URL: up.URL})
		createHub(t, f, repo.Config{Key: "inner", Class: repo.Virtual, Repositories: []string{"up", "hf"}})
		patchHub(t, f, "hf", func(c *repo.Config) { c.Repositories = []string{"local", "inner"} })
		return remoteService(t, f)
	})
	head := f.request(http.MethodHead, "acme/demo/resolve/main/weights.bin", "", nil)
	readResponse(t, head)
	hash, refresh := head.Header.Get("X-Xet-Hash"), head.Header.Get("X-Xet-Refresh-Route")
	if head.StatusCode != http.StatusOK || len(hash) != 64 ||
		!strings.Contains(refresh, "/artifactory/api/huggingfaceml/hf/") {
		t.Fatalf("resolve through the virtual: %d %v", head.StatusCode, head.Header)
	}
	answer, e := f.server.Client().Get(refresh)
	if e != nil {
		t.Fatal(e)
	}
	var token struct {
		CAS   string `json:"casUrl"`
		Token string `json:"accessToken"`
	}
	if b := readResponse(t, answer); answer.StatusCode != http.StatusOK || json.Unmarshal(b, &token) != nil {
		t.Fatalf("read token: %d %s", answer.StatusCode, b)
	}
	req, e := http.NewRequestWithContext(t.Context(), http.MethodGet,
		token.CAS+"/v1/reconstructions/"+hash, nil)
	if e != nil {
		t.Fatal(e)
	}
	req.Header.Set("Authorization", "Bearer "+token.Token)
	reconstruction, e := f.server.Client().Do(req)
	if e != nil {
		t.Fatal(e)
	}
	if b := readResponse(t, reconstruction); reconstruction.StatusCode != http.StatusOK ||
		!strings.Contains(string(b), "fetch_info") {
		t.Fatalf("reconstruction through the virtual: %d %s", reconstruction.StatusCode, b)
	}
}

// An authenticated caller who may read the model in no member gets its 404,
// as for a member without it (decision 0008, R3). An anonymous caller gets
// the 401 challenge (R4) [I], where it got the 404, unless the virtual
// repository hides unauthorized resources or no member's policy admits the
// model's folder.
func TestUnitVirtualUnservedAnswersPerCaller(t *testing.T) {
	f := newFixture(t, repo.Virtual, func(f *fixture) *source.Service {
		createHub(t, f, repo.Config{Key: "first", Class: repo.Local})
		patchHub(t, f, "hf", func(c *repo.Config) { c.Repositories = []string{"first"} })
		return nil
	})
	commitFile(t, f, "first", "one.txt")
	routes := []string{"api/models/acme/demo/revision/main", "acme/demo/resolve/main/one.txt"}
	wantNotFound := func(user string) {
		t.Helper()
		for _, route := range routes {
			wantHub(t, hubGet(t, f, route, user), http.StatusNotFound,
				"Hugging Face revision or file not found")
		}
	}
	wantNotFound("intruder")
	for _, route := range routes {
		got := hubGet(t, f, route, kernel.AnonymousUser)
		if got.status != http.StatusUnauthorized ||
			got.header.Get("WWW-Authenticate") != apierror.BasicChallenge {
			t.Fatalf("anonymous %s: %d %v %s", route, got.status, got.header, got.body)
		}
	}
	hide := true
	patchHub(t, f, "hf", func(c *repo.Config) { c.HideUnauthorizedResources = &hide })
	wantNotFound(kernel.AnonymousUser)
	hide = false
	excluded := "models--acme--demo/**"
	patchHub(t, f, "hf", func(c *repo.Config) { c.HideUnauthorizedResources = &hide })
	patchHub(t, f, "first", func(c *repo.Config) { c.ExcludesPattern = &excluded })
	wantNotFound(kernel.AnonymousUser)
}

// The virtual repository's own patterns refuse the model's folder before
// any member is asked, an administrator's too (decision 0008, R5): the
// listing and the file answer 404 "No repository found to serve the
// request", where the listing was served. A member whose own patterns
// reject the folder passes over to the next, where it was chosen and
// refused each file.
func TestUnitVirtualPatternsComeBeforeTheMembers(t *testing.T) {
	f := newFixture(t, repo.Virtual, func(f *fixture) *source.Service {
		createHub(t, f, repo.Config{Key: "first", Class: repo.Local})
		createHub(t, f, repo.Config{Key: "second", Class: repo.Local})
		patchHub(t, f, "hf", func(c *repo.Config) { c.Repositories = []string{"first", "second"} })
		return nil
	})
	commitFile(t, f, "first", "one.txt")
	commitFile(t, f, "second", "two.txt")
	excluded := "models--acme--demo/**"
	patchHub(t, f, "hf", func(c *repo.Config) { c.ExcludesPattern = &excluded })
	for _, route := range []string{
		"api/models/acme/demo/revision/main", "acme/demo/resolve/main/one.txt",
	} {
		wantHub(t, hubGet(t, f, route, ""), http.StatusNotFound,
			"No repository found to serve the request for hf:models--acme--demo")
	}
	patchHub(t, f, "hf", func(c *repo.Config) { c.ExcludesPattern = new("") })
	patchHub(t, f, "first", func(c *repo.Config) { c.ExcludesPattern = &excluded })
	got := hubGet(t, f, "api/models/acme/demo/revision/main", "")
	wantHub(t, got, http.StatusOK, `"two.txt"`)
	if strings.Contains(got.body, "one.txt") {
		t.Fatalf("refusing member chosen: %s", got.body)
	}
	wantHub(t, hubGet(t, f, "acme/demo/resolve/main/two.txt", ""), http.StatusOK, "two.txt")
}
