package pkgroute_test

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/http"
	"strings"
	"testing"

	"github.com/go-chi/chi/v5"
	"golang.org/x/mod/semver"

	"github.com/weaveplatform/artifactweave/internal/access/accesstest"
	"github.com/weaveplatform/artifactweave/internal/access/api"
	"github.com/weaveplatform/artifactweave/internal/app/adapters/inmem"
	"github.com/weaveplatform/artifactweave/internal/artifacts/adapters/http/pkgroute"
	"github.com/weaveplatform/artifactweave/internal/artifacts/api/repo"
	"github.com/weaveplatform/artifactweave/internal/artifacts/core/format"
	"github.com/weaveplatform/artifactweave/internal/artifacts/core/port"
	"github.com/weaveplatform/artifactweave/internal/artifacts/core/remote/source"
	"github.com/weaveplatform/artifactweave/internal/artifacts/core/virtual"
	"github.com/weaveplatform/artifactweave/internal/kernel"
	"github.com/weaveplatform/artifactweave/internal/platform/httpkit/apierror"
	"github.com/weaveplatform/artifactweave/internal/platform/httpkit/baseurl"
)

// walkRemote is a remote member's source boundary: it serves the bodies of
// docs by path, misses any other, and records each read as the member's
// key, with "~" after a read from the cache alone.
type walkRemote struct {
	docs  map[string]string
	err   error
	reads []string
}

func (*walkRemote) Item(context.Context, pkgroute.Store, repo.Config, string) (pkgroute.Found, error) {
	return pkgroute.Found{}, source.ErrUnsupported
}

func (s *walkRemote) Read(r *http.Request, c repo.Config, res source.Resource) (source.Result, error) {
	step := c.Key
	if pkgroute.RequestFrom(r).CacheOnly {
		step += "~"
	}
	s.reads = append(s.reads, step)
	if s.err != nil {
		return source.Result{}, s.err
	}
	body, ok := s.docs[res.Path]
	if !ok {
		return source.Result{}, fmt.Errorf("remote %s: %w", res.Path, kernel.ErrNotFound)
	}
	return source.Result{Stream: &source.Stream{
		Status: http.StatusOK, Body: io.NopCloser(strings.NewReader(body)),
		Representation: source.Representation{Size: int64(len(body))},
	}}, nil
}

// row is one version of the merged test document.
type row struct {
	Version string `json:"version"`
	From    string `json:"from"`
	URL     string `json:"url"`
}

// merged is the test format's merged document.
type merged struct {
	Versions []row  `json:"versions"`
	Latest   string `json:"latest"`
}

// versionsMerger merges members' comma-separated version lists as a
// family's virtual.go would, from the contract's helpers alone.
type versionsMerger struct{ base, key string }

func (m versionsMerger) Merge(_ context.Context, parts []virtual.Part[[]string]) (merged, error) {
	lists := make([][]row, len(parts))
	for i, p := range parts {
		for _, v := range p.Doc {
			lists[i] = append(lists[i], row{Version: v, From: p.Member.Key})
		}
	}
	rows := virtual.UnionBy(lists, func(r row) string { return r.Version })
	virtual.SortVersions(rows, func(r row) string { return r.Version }, semver.Compare)
	versions := make([]string, len(rows))
	for i := range rows {
		rows[i].URL = virtual.APIBase(m.base, "testfmt", m.key) + "/files/" + rows[i].Version
		versions[i] = rows[i].Version
	}
	stable := func(v string) bool { return semver.Prerelease(v) == "" }
	return merged{Versions: rows, Latest: virtual.Latest(versions, semver.Compare, stable)}, nil
}

// walkFormat is policyFormat with the virtual walk's routes: /steps/*
// answers the keys of MergeMembers' member requests in order, a remote
// member's with "~" when it reads its cache alone; /collect/* merges the
// version lists the members hold at the path through Collect and
// versionsMerger, and answers 404 when no member holds one.
func walkFormat(sources map[repo.Class]pkgroute.Source) pkgroute.Protocol {
	p := policyFormat(sources)
	routes := p.Routes
	p.Routes = func(r chi.Router, k *pkgroute.Kit) {
		routes(r, k)
		path := func(r *http.Request) string {
			_, rest, _ := strings.Cut(pkgroute.RequestFrom(r).Rest, "/")
			return rest
		}
		fail := func(w http.ResponseWriter, r *http.Request, err error) {
			mark(w, err)
			k.Error(w, r, pkgroute.SourceError(err, false))
		}
		for route, read := range map[string]func(*http.Request, string) ([]*http.Request, error){
			"/steps/*": k.MergeMembers, "/downloads/*": k.DownloadMembers,
		} {
			r.Get(route, func(w http.ResponseWriter, r *http.Request) {
				members, err := read(r, path(r))
				if err != nil {
					fail(w, r, err)
					return
				}
				keys := make([]string, 0, len(members))
				for _, mr := range members {
					q := pkgroute.RequestFrom(mr)
					key := q.Repo.Key
					if q.CacheOnly {
						key += "~"
					}
					keys = append(keys, key)
				}
				_, _ = io.WriteString(w, strings.Join(keys, ","))
			})
		}
		r.Get("/collect/*", func(w http.ResponseWriter, r *http.Request) {
			p := path(r)
			parts, err := pkgroute.Collect(k, r, p, func(mr *http.Request) ([]string, error) {
				return readVersions(k, mr, p)
			})
			if err != nil {
				fail(w, r, err)
				return
			}
			if len(parts) == 0 {
				k.Error(w, r, apierror.New(http.StatusNotFound, noMember))
				return
			}
			doc, err := versionsMerger{
				base: baseurl.From(r.Context()), key: pkgroute.RequestFrom(r).Repo.Key,
			}.Merge(r.Context(), parts)
			if err != nil {
				fail(w, r, err)
				return
			}
			_ = json.NewEncoder(w).Encode(doc)
		})
	}
	return p
}

// readVersions reads the version list at path in mr's repository, as a
// format reads a member's metadata document.
func readVersions(k *pkgroute.Kit, mr *http.Request, path string) ([]string, error) {
	result, err := k.ReadMetadata(mr, pkgroute.Resource(mr, path, path))
	if err != nil {
		return nil, err //nolint:wrapcheck // the kit's own answer
	}
	var body io.ReadCloser
	if result.Stream != nil {
		body = result.Stream.Body
	} else if body, _, err = k.Blobs().Open(mr.Context(), result.Item.Actual.SHA256); err != nil {
		return nil, err //nolint:wrapcheck // a test read
	}
	defer func() { _ = body.Close() }()
	b, err := io.ReadAll(body)
	if err != nil {
		return nil, err //nolint:wrapcheck // a test read
	}
	return strings.Split(string(b), ","), nil
}

// walkFixture is the kit fixture serving walkFormat behind the base URL
// middleware, with "root" an administrator, over the ticket's nesting:
// outer lists inner and local-b, and inner lists remote-a, local-a and
// outer again. local-a, local-b and remote-a each hold idx/versions, and
// local-a and local-b each hold a file only they hold. merger may read
// every member, partial only local-a, and txt only local-a's .txt files.
func walkFixture(t *testing.T, tune func(*pkgroute.Options)) (*kitFixture, *walkRemote) {
	t.Helper()
	remote := &walkRemote{docs: map[string]string{"idx/versions": "v0.9.0,v1.5.0"}}
	sources := map[repo.Class]pkgroute.Source{
		repo.Local: pkgroute.Local{}, repo.Federated: pkgroute.Local{},
		repo.Remote: remote, repo.Virtual: pkgroute.Virtual{},
	}
	f := newKitFixture(t, sources)
	for _, c := range []repo.Config{
		{Key: "local-a", Class: repo.Local},
		{Key: "local-b", Class: repo.Local},
		{Key: "remote-a", Class: repo.Remote, URL: "https://up.example"},
		{Key: "inner", Class: repo.Virtual, Repositories: []string{"remote-a", "local-a", "outer"}},
		{Key: "outer", Class: repo.Virtual, Repositories: []string{"inner", "local-b"}},
	} {
		if err := f.db.Repos().Create(t.Context(), *repo.Defaults(&c)); err != nil {
			t.Fatal(err)
		}
	}
	f.deploy(t, "local-a", "idx/versions", "v1.0.0,v2.0.0-beta.1")
	f.deploy(t, "local-b", "idx/versions", "v1.0.0,v1.5.0")
	f.deploy(t, "local-a", "shared.txt", "a's shared")
	f.deploy(t, "local-b", "shared.txt", "b's shared")
	f.deploy(t, "local-b", "only-b.txt", "b's own")
	f.deploy(t, "local-a", "data.bin", "a's data")
	for user, targets := range map[string]map[string]accesstest.Target{
		"merger": {
			"local-a": {Include: []string{"**"}}, "local-b": {Include: []string{"**"}},
			"remote-a": {Include: []string{"**"}},
		},
		"partial": {"local-a": {Include: []string{"**"}}},
		"txt":     {"local-a": {Include: []string{"**/*.txt"}}},
	} {
		if err := f.db.Permissions().Create(t.Context(), accesstest.Permission{
			Name: user,
			Resources: map[api.ResourceType]accesstest.Resource{api.ResourceArtifact: {
				Actions: accesstest.Grants{Users: map[string]api.Actions{user: api.ActionRead}},
				Targets: targets,
			}},
		}); err != nil {
			t.Fatal(err)
		}
	}
	o := f.o
	if tune != nil {
		tune(&o)
	}
	principalOf := func(r *http.Request) kernel.Principal {
		name := r.Header.Get("X-Test-User")
		return kernel.Principal{Name: name, Admin: name == "root"}
	}
	authorize := accesstest.Over(t, f.db).Middleware(t, true, principalOf)
	r := chi.NewRouter()
	r.Use(baseurl.Middleware(""))
	r.Use(authorize)
	pkgroute.Mount(r, o, walkFormat(sources))
	f.h = r
	return f, remote
}

// wantBody checks that GET path as user answers 200 with body.
func (f *kitFixture) wantBody(t *testing.T, path, user, body string, headers ...string) {
	t.Helper()
	rec := f.do(http.MethodGet, base+path, user, "", headers...)
	if got := strings.TrimSpace(rec.Body.String()); rec.Code != http.StatusOK || got != body {
		t.Fatalf("GET %s as %q: %d %s, want 200 %s", path, user, rec.Code, got, body)
	}
}

// TestAccMergeMembersWalk: MergeMembers walks a virtual repository's
// members as 7.x's package handlers do: nested virtual repositories
// flattened with one visited set, so the cycle back to outer is skipped,
// local members before remote ones (R1, R2); and a member the caller may
// read nothing in is left out, never refused (R3).
func TestAccMergeMembersWalk(t *testing.T) {
	f, _ := walkFixture(t, nil)
	f.wantBody(t, "outer/steps/idx/versions", "merger", "local-a,local-b,remote-a")
	f.wantBody(t, "outer/steps/idx/versions", "root", "local-a,local-b,remote-a")
	f.wantBody(t, "inner/steps/idx/versions", "merger", "local-a,local-b,remote-a")
	f.wantBody(t, "outer/steps/idx/versions", "partial", "local-a")
	f.wantBody(t, "outer/steps/idx/versions", "nobody", "")
	// A repository that is not virtual is its own one step.
	f.wantBody(t, "local-b/steps/idx/versions", "nobody", "local-b")
}

// TestAccMergeMembersPassOver: a member whose patterns reject the
// document, a blacked-out member, a missing one, one of a package type the
// virtual repository's does not take (R11), one of a class the protocol
// has no source for and a nested virtual repository whose patterns reject
// the document are left out; the virtual repository's own patterns
// rejecting it still refuse it. A generic virtual repository takes members
// of every type.
func TestAccMergeMembersPassOver(t *testing.T) {
	f, _ := walkFixture(t, nil)
	f.patch(t, "local-b", func(c *repo.Config) { c.ExcludesPattern = strPtr("idx/**") })
	f.patch(t, "local-a", blackOut)
	f.patch(t, "outer", func(c *repo.Config) {
		c.Repositories = []string{"gone", "img-local", "inner", "local-b", "gen-fed"}
	})
	f.wantBody(t, "outer/steps/idx/versions", "root", "img-local,gen-fed,remote-a")
	f.wantBody(t, "outer/steps/shared.txt", "root", "img-local,local-b,gen-fed,remote-a")
	f.patch(t, "inner", func(c *repo.Config) { c.ExcludesPattern = strPtr("idx/**") })
	f.wantBody(t, "outer/steps/idx/versions", "root", "img-local,gen-fed")
	f.patch(t, "outer", func(c *repo.Config) { c.ExcludesPattern = strPtr("idx/**") })
	f.wantPolicy(t, "outer/steps/idx/versions", "root", http.StatusNotFound,
		"No repository found to serve the request for outer:idx/versions")

	// A protocol with no remote source passes over remote-a, and a Docker
	// virtual repository passes over its generic member.
	p, _ := walkFixture(t, nil)
	c := repo.Config{
		Key: "img-virtual", Class: repo.Virtual, PackageType: repo.Docker,
		Repositories: []string{"local-a", "img-local"},
	}
	if err := p.db.Repos().Create(t.Context(), *repo.Defaults(&c)); err != nil {
		t.Fatal(err)
	}
	r := chi.NewRouter()
	r.Use(accesstest.Over(t, p.db).Middleware(t, true, func(*http.Request) kernel.Principal {
		return kernel.Principal{Name: "root", Admin: true}
	}))
	proto := walkFormat(map[repo.Class]pkgroute.Source{
		repo.Local: pkgroute.Local{}, repo.Virtual: pkgroute.Local{},
	})
	proto.Types = append(proto.Types, repo.Docker)
	pkgroute.Mount(r, p.o, proto)
	p.h = r
	p.wantBody(t, "outer/steps/idx/versions", "root", "local-a,local-b")
	p.wantBody(t, "img-virtual/steps/idx/versions", "root", "img-local")
}

// TestAccMergeMembersCacheOnly: when the walk takes no upstream step
// (Options.NoUpstream, R7) each remote member's request reads its cache
// alone, as Request.CacheOnly asks the source boundary to.
func TestAccMergeMembersCacheOnly(t *testing.T) {
	var asked []string
	f, remote := walkFixture(t, func(o *pkgroute.Options) {
		o.NoUpstream = func(r *http.Request, v repo.Config) bool {
			asked = append(asked, v.Key)
			return r.Header.Get("X-From-Artifactory") != ""
		}
	})
	f.wantBody(t, "outer/steps/idx/versions", "merger", "local-a,local-b,remote-a")
	f.wantBody(t, "outer/steps/idx/versions", "merger", "local-a,local-b,remote-a~",
		"X-From-Artifactory", "true")
	rec := f.do(http.MethodGet, base+"outer/collect/idx/versions", "merger", "",
		"X-From-Artifactory", "true")
	if rec.Code != http.StatusOK || len(remote.reads) != 1 || remote.reads[0] != "remote-a~" {
		t.Fatalf("cache-only collect: %d %s, remote reads %v", rec.Code, rec.Body, remote.reads)
	}
	if strings.Join(asked, ",") != "outer,outer,outer" {
		t.Fatalf("NoUpstream asked for %v", asked)
	}
}

// TestAccCollectMerges: Collect reads the document from each member the
// walk keeps, and a merge built from the contract's helpers keeps the first
// member's copy of a version, sorts the versions, picks the latest release
// over a later pre-release, and points each file at the virtual
// repository's API under the request's base URL, X-JFrog-Override-Base-Url
// included. A member that cannot be read, or holds no document, is left
// out, and with none left the format answers its own not-found.
func TestAccCollectMerges(t *testing.T) {
	f, remote := walkFixture(t, nil)
	get := func(user string, headers ...string) merged {
		t.Helper()
		rec := f.do(http.MethodGet, base+"outer/collect/idx/versions", user, "", headers...)
		var doc merged
		if rec.Code != http.StatusOK || json.Unmarshal(rec.Body.Bytes(), &doc) != nil {
			t.Fatalf("collect as %s: %d %s", user, rec.Code, rec.Body)
		}
		return doc
	}
	spell := func(doc merged) string {
		out := make([]string, len(doc.Versions))
		for i, r := range doc.Versions {
			out[i] = r.Version + "@" + r.From
		}
		return strings.Join(out, " ") + " latest " + doc.Latest
	}
	doc := get("merger")
	if want := "v0.9.0@remote-a v1.0.0@local-a v1.5.0@local-b v2.0.0-beta.1@local-a latest v1.5.0"; spell(doc) != want {
		t.Fatalf("merged %s, want %s", spell(doc), want)
	}
	if want := "http://example.com/artifactory/api/testfmt/outer/files/v1.5.0"; doc.Versions[2].URL != want {
		t.Fatalf("url %s, want %s", doc.Versions[2].URL, want)
	}
	doc = get("merger", baseurl.OverrideHeader, "https://proxy.example/root/")
	if want := "https://proxy.example/root/artifactory/api/testfmt/outer/files/v1.5.0"; doc.Versions[2].URL != want {
		t.Fatalf("overridden url %s, want %s", doc.Versions[2].URL, want)
	}
	doc = get("merger", "X-Artifactory-Override-Base-Url", "https://ignored.example")
	if !strings.HasPrefix(doc.Versions[0].URL, "http://example.com/") {
		t.Fatalf("X-Artifactory-Override-Base-Url honoured (DV-037): %s", doc.Versions[0].URL)
	}
	if want := "v1.0.0@local-a v2.0.0-beta.1@local-a latest v1.0.0"; spell(get("partial")) != want {
		t.Fatalf("partial reader's merge %s, want %s", spell(get("partial")), want)
	}
	// The walk admits txt into local-a, whose .txt files it may read; the
	// document's own read refuses it, and the refusal passes over.
	f.wantPolicy(t, "outer/collect/idx/versions", "txt", http.StatusNotFound, noMember)
	f.wantPolicy(t, "outer/collect/idx/versions", "nobody", http.StatusNotFound, noMember)
	f.wantPolicy(t, "outer/collect/idx/missing", "merger", http.StatusNotFound, noMember)
	// A remote member whose upstream fails is passed over [I].
	remote.err = &source.HTTPError{Status: http.StatusBadGateway}
	if want := "v1.0.0@local-a v1.5.0@local-b v2.0.0-beta.1@local-a latest v1.5.0"; spell(get("merger")) != want {
		t.Fatalf("merge past a failing upstream %s, want %s", spell(get("merger")), want)
	}
	// A repository that is not virtual is its own one part.
	f.wantBody(t, "local-b/collect/idx/versions", "merger",
		`{"versions":[{"version":"v1.0.0","from":"local-b","url":"http://example.com/artifactory/api/testfmt/local-b/files/v1.0.0"},`+
			`{"version":"v1.5.0","from":"local-b","url":"http://example.com/artifactory/api/testfmt/local-b/files/v1.5.0"}],"latest":"v1.5.0"}`)
	f.wantPolicy(t, "local-b/collect/idx/versions", "partial", http.StatusForbidden, "")
	f.patch(t, "outer", func(c *repo.Config) { c.ExcludesPattern = strPtr("idx/**") })
	f.wantPolicy(t, "outer/collect/idx/versions", "merger", http.StatusNotFound,
		"No repository found to serve the request for outer:idx/versions")
}

// TestAccCollectFault: a member read that fails for another reason than
// one PassedOver reports fails the merge, so it is never answered short.
func TestAccCollectFault(t *testing.T) {
	f, remote := walkFixture(t, nil)
	remote.err = &source.HTTPError{Status: http.StatusUnauthorized}
	f.wantPolicy(t, "outer/collect/idx/versions", "merger", http.StatusUnauthorized, "Unauthorized")
	remote.err = errors.New("boom")
	f.wantPolicy(t, "outer/collect/idx/versions", "merger", http.StatusInternalServerError, "")
	if len(f.faults) == 0 || !strings.Contains(f.faults[len(f.faults)-1].Error(),
		"testfmt: merge outer: member remote-a") {
		t.Fatalf("faults %v", f.faults)
	}
}

// TestAccVirtualSource: Virtual serves a virtual repository's file from
// the first member in the walk that holds it and that the caller may read,
// passes over the others, the remote member's source with no single-item
// lookup included, and answers "Could not find resource" when none does,
// never 403. The virtual repository's own patterns refuse first.
func TestAccVirtualSource(t *testing.T) {
	f, _ := walkFixture(t, nil)
	f.wantServed(t, "outer/files/shared.txt", "merger", "local-a/shared.txt")
	f.wantServed(t, "outer/files/only-b.txt", "merger", "local-b/only-b.txt")
	f.wantServed(t, "outer/files/shared.txt", "partial", "local-a/shared.txt")
	f.wantServed(t, "outer/files/shared.txt", "txt", "local-a/shared.txt")
	const missing = "Could not find resource"
	f.wantPolicy(t, "outer/files/only-b.txt", "partial", http.StatusNotFound, missing)
	f.wantPolicy(t, "outer/files/data.bin", "txt", http.StatusNotFound, missing)
	f.wantPolicy(t, "outer/files/shared.txt", "nobody", http.StatusNotFound, missing)
	f.wantPolicy(t, "outer/files/absent.txt", "root", http.StatusNotFound, missing)
	f.patch(t, "local-a", func(c *repo.Config) { c.ExcludesPattern = strPtr("shared.txt") })
	f.wantServed(t, "outer/files/shared.txt", "merger", "local-b/shared.txt")
	f.patch(t, "outer", func(c *repo.Config) { c.ExcludesPattern = strPtr("*.txt") })
	f.wantPolicy(t, "outer/files/shared.txt", "merger", http.StatusNotFound,
		"No repository found to serve the request for outer:shared.txt")
}

// faultSource fails every lookup.
type faultSource struct{}

func (faultSource) Item(context.Context, pkgroute.Store, repo.Config, string) (pkgroute.Found, error) {
	return pkgroute.Found{}, fmt.Errorf("lookup: %w", format.ErrStorageFault)
}

// TestAccVirtualWalkFaults: a member's lookup fault, and a member the
// store fails to read, end the walk as the fault rather than shrinking its
// answer; a request no access check has evaluated reads no member; and a
// Virtual no kit has bound supports no lookup.
func TestAccVirtualWalkFaults(t *testing.T) {
	sources := map[repo.Class]pkgroute.Source{
		repo.Local: pkgroute.Local{}, repo.Federated: faultSource{}, repo.Virtual: pkgroute.Virtual{},
	}
	f := newKitFixture(t, sources)
	f.patch(t, "gen-virtual", func(c *repo.Config) { c.Repositories = []string{federated} })
	f.wantPolicy(t, "gen-virtual/files/a/b.txt", "reader", http.StatusInternalServerError, "")
	if len(f.faults) != 1 || !errors.Is(f.faults[0], format.ErrStorageFault) {
		t.Fatalf("faults %v", f.faults)
	}
	f.patch(t, "gen-virtual", func(c *repo.Config) { c.Repositories = []string{local} })
	f.wantPolicy(t, "gen-virtual/files/a/b.txt", "", http.StatusNotFound, "Could not find resource")
	c, err := f.db.Repos().Get(t.Context(), "gen-virtual")
	if err != nil {
		t.Fatal(err)
	}
	// Mount binds a copy: the protocol's own map keeps the unbound value.
	if _, err := sources[repo.Virtual].Item(t.Context(), f.db, c, "a/b.txt"); !errors.Is(
		err, source.ErrUnsupported,
	) {
		t.Fatalf("unbound Virtual: %v", err)
	}

	w, _ := walkFixture(t, nil)
	w.patch(t, "inner", func(c *repo.Config) { c.Repositories = []string{"broken", "local-a"} })
	o := w.o
	o.Store = memberFailRepos{w.db}
	r := chi.NewRouter()
	r.Use(baseurl.Middleware(""))
	r.Use(accesstest.Over(t, w.db).Middleware(t, true, func(*http.Request) kernel.Principal {
		return kernel.Principal{Name: "root", Admin: true}
	}))
	pkgroute.Mount(r, o, walkFormat(map[repo.Class]pkgroute.Source{
		repo.Local: pkgroute.Local{}, repo.Virtual: pkgroute.Virtual{},
	}))
	w.h = r
	for _, route := range []string{"steps", "collect", "files"} {
		faults := len(w.faults)
		w.wantPolicy(t, "outer/"+route+"/idx/versions", "root", http.StatusInternalServerError, "")
		if len(w.faults) != faults+1 || !strings.Contains(w.faults[faults].Error(), "member broken") {
			t.Fatalf("%s: faults %v", route, w.faults[faults:])
		}
	}
}

// memberFailRepos fails to read the repository "broken" alone, so a
// virtual repository resolves and its walk meets the failure.
type memberFailRepos struct{ *inmem.Store }

func (s memberFailRepos) Repos() port.RepoStore { return memberFailRepoStore{s.Store.Repos()} }

type memberFailRepoStore struct{ port.RepoStore }

func (s memberFailRepoStore) Get(ctx context.Context, key string) (repo.Config, error) {
	if key == "broken" {
		return repo.Config{}, errors.New("member broken: store down")
	}
	return s.RepoStore.Get(ctx, key) //nolint:wrapcheck // the store's own answer
}

// TestAccMemberRequestRefusesDirectCallers: MemberRequest, for a protocol
// that walks a virtual repository's own list, still refuses a nested
// virtual member and one of a class the protocol has no source for, which
// MergeMembers' walk flattens or passes over.
func TestAccMemberRequestRefusesDirectCallers(t *testing.T) {
	f := policyFixture(t, nil)
	for members, message := range map[string]string{
		"gen-remote":  "unsupported virtual repository member",
		"gen-virtual": "invalid virtual repository member",
	} {
		f.patch(t, "gen-virtual", func(c *repo.Config) { c.Repositories = []string{members} })
		f.wantPolicy(t, "gen-virtual/first/a/b.txt", "root", http.StatusBadRequest, message)
		f.wantBody(t, "gen-virtual/merge/a/b.txt", "root", "")
	}
}

func TestUnitPassedOver(t *testing.T) {
	refused := format.Rejectf(http.StatusNotFound, "rejected")
	for _, tc := range []struct {
		name string
		err  error
		want bool
	}{
		{"nil", nil, false},
		{"missing", fmt.Errorf("get: %w", kernel.ErrNotFound), true},
		{"policy refusal", fmt.Errorf("read: %w", refused), true},
		{"boundary refusal", pkgroute.SourceError(refused, false), true},
		{"member read denied", source.ErrDenied, true},
		{"forbidden", fmt.Errorf("read: %w", kernel.ErrForbidden), true},
		{"unauthenticated", kernel.ErrUnauthenticated, true},
		{"unreachable", source.ErrUnavailable, true},
		{"timed out", context.DeadlineExceeded, true},
		{"upstream 502", &source.HTTPError{Status: http.StatusBadGateway}, true},
		{"upstream 401", &source.HTTPError{Status: http.StatusUnauthorized}, false},
		{"upstream 429", &source.HTTPError{Status: http.StatusTooManyRequests}, false},
		{"answered miss", pkgroute.SourceError(kernel.ErrNotFound, false), true},
		{"answered unreachable", pkgroute.SourceError(source.ErrUnavailable, false), true},
		{"answered timeout", pkgroute.SourceError(context.DeadlineExceeded, false), true},
		{"answered upstream 503", pkgroute.SourceError(
			&source.HTTPError{Status: http.StatusServiceUnavailable}, false), true},
		{"answered upstream 403", pkgroute.SourceError(
			&source.HTTPError{Status: http.StatusForbidden}, false), false},
		{"answered superseded", pkgroute.SourceError(source.ErrSuperseded, false), false},
		{"bad member", apierror.New(http.StatusBadRequest, "invalid virtual repository member"), false},
		{"storage fault", fmt.Errorf("%w: %w", format.ErrStorageFault, context.DeadlineExceeded), false},
		{"other", errors.New("boom"), false},
	} {
		if got := pkgroute.PassedOver(tc.err); got != tc.want {
			t.Errorf("%s: PassedOver(%v) = %v, want %v", tc.name, tc.err, got, tc.want)
		}
	}
}

// DownloadMembers leaves a remote's policy to its source while retaining
// hosted and nested virtual patterns, authority and the cache-only binding.
func TestAccDownloadMembersPolicyAndCacheOnly(t *testing.T) {
	f, _ := walkFixture(t, func(o *pkgroute.Options) {
		o.NoUpstream = func(r *http.Request, _ repo.Config) bool {
			return r.Header.Get("X-From-Artifactory") != ""
		}
	})
	f.wantBody(t, "local-a/downloads/idx/versions", "root", "local-a")
	f.wantBody(t, "outer/downloads/idx/versions", "merger", "local-a,local-b,remote-a")
	f.wantBody(t, "outer/downloads/idx/versions", "merger", "local-a,local-b,remote-a~", "X-From-Artifactory", "true")
	f.patch(t, "remote-a", func(c *repo.Config) { c.ExcludesPattern = strPtr("idx/**") })
	f.wantBody(t, "remote-a/downloads/idx/versions", "merger", "remote-a")
	f.wantBody(t, "outer/steps/idx/versions", "merger", "local-a,local-b")
	f.wantBody(t, "outer/downloads/idx/versions", "merger", "local-a,local-b,remote-a")
	f.wantBody(t, "outer/downloads/idx/versions", "merger", "local-a,local-b,remote-a~", "X-From-Artifactory", "true")
	f.wantBody(t, "outer/downloads/idx/versions", "partial", "local-a")
	f.patch(t, "local-a", func(c *repo.Config) { c.ExcludesPattern = strPtr("idx/**") })
	f.wantPolicy(t, "local-a/downloads/idx/versions", "root", http.StatusNotFound, "")
	f.wantBody(t, "outer/downloads/idx/versions", "merger", "local-b,remote-a")
	f.patch(t, "remote-a", blackOut)
	f.wantBody(t, "outer/downloads/idx/versions", "merger", "local-b")
	f.patch(t, "remote-a", restore)
	f.patch(t, "inner", func(c *repo.Config) { c.ExcludesPattern = strPtr("idx/**") })
	f.wantBody(t, "outer/downloads/idx/versions", "merger", "local-b")
	f.patch(t, "outer", func(c *repo.Config) { c.ExcludesPattern = strPtr("idx/**") })
	f.wantPolicy(t, "outer/downloads/idx/versions", "root", http.StatusNotFound,
		"No repository found to serve the request for outer:idx/versions")
}
