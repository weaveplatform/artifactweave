package descriptor

import (
	"context"
	"errors"
	"strings"
	"testing"
	"time"

	"github.com/weaveplatform/artifactweave/pkg/repo"
	"github.com/weaveplatform/artifactweave/pkg/store"
	"github.com/weaveplatform/artifactweave/pkg/store/inmem"
	"github.com/weaveplatform/artifactweave/pkg/types"
)

var now = time.Date(2026, 10, 13, 12, 0, 0, 0, time.UTC)

func apply(t *testing.T, st store.Store, doc string) (Result, error) {
	t.Helper()
	p, err := Parse([]byte(doc), env(map[string]string{"PROXY_PASSWORD": "pp"}))
	if err != nil {
		t.Fatal(err)
	}
	return Apply(context.Background(), st, p, now, false)
}

func mustApply(t *testing.T, st store.Store, doc string) Result {
	t.Helper()
	res, err := apply(t, st, doc)
	if err != nil {
		t.Fatal(err)
	}
	return res
}

func seedRepo(t *testing.T, st store.Store, c repo.Config) {
	t.Helper()
	if err := st.Repos().Create(context.Background(), *repo.Defaults(&c)); err != nil {
		t.Fatal(err)
	}
}

const full = `
localRepositories:
  libs-local:
    type: maven
    description: built here
remoteRepositories:
  maven-central:
    type: maven
    url: https://repo1.maven.org/maven2/
virtualRepositories:
  maven-all:
    type: maven
    repositories: [libs-local, maven-central]
proxies:
  corp:
    host: proxy.example.com
    port: 3128
    password: ${PROXY_PASSWORD}
propertySets:
  build:
    visible: true
    properties:
      status:
        predefinedValues: [ok, failed]
repoLayouts:
  custom:
    artifactPathPattern: "[org]/[module]/[baseRev]/[module]-[baseRev].[ext]"
    distinctiveDescriptorPathPattern: false
mailServer:
  host: smtp.example
`

func TestApplyCreatesThenNothing(t *testing.T) {
	ctx := context.Background()
	db := inmem.New()
	res := mustApply(t, db, full)
	if got := strings.Join(res.Created, " "); got != "localRepositories.libs-local "+
		"remoteRepositories.maven-central virtualRepositories.maven-all proxies.corp "+
		"propertySets.build repoLayouts.custom" {
		t.Fatalf("created %s", got)
	}
	v, err := db.Repos().Get(ctx, "maven-all")
	if err != nil || strings.Join(v.Repositories, ",") != "libs-local,maven-central" {
		t.Fatalf("virtual %+v %v", v, err)
	}
	proxy, err := db.Configs().Get(ctx, types.ConfigProxy, "corp")
	if err != nil || proxy.Secrets["password"] != "pp" ||
		strings.Contains(string(proxy.Document), "pp") {
		t.Fatalf("proxy %+v %v: the password belongs in the sealed secrets only", proxy, err)
	}
	// The same descriptor again changes nothing.
	res = mustApply(t, db, full)
	if len(res.Created)+len(res.Updated)+len(res.Deleted) != 0 || len(res.Unchanged) != 6 {
		t.Fatalf("second apply %+v", res)
	}
	if !strings.Contains(res.String(), "Ignored (accepted without effect): mailServer") {
		t.Fatalf("summary:\n%s", res)
	}
}

func TestApplyUpdatesAndDeletes(t *testing.T) {
	ctx := context.Background()
	db := inmem.New()
	mustApply(t, db, full)
	res := mustApply(t, db, `
localRepositories:
  libs-local:
    description: null
    includesPattern: "com/**"
remoteRepositories:
  maven-central: null
proxies:
  corp:
    port: 8080
    password: null
propertySets:
  build: null
repoLayouts:
  missing: null
`)
	if strings.Join(res.Updated, " ") != "localRepositories.libs-local proxies.corp" ||
		strings.Join(res.Deleted, " ") != "remoteRepositories.maven-central propertySets.build" ||
		strings.Join(res.Unchanged, " ") != "repoLayouts.missing" {
		t.Fatalf("result %+v", res)
	}
	local, err := db.Repos().Get(ctx, "libs-local")
	if err != nil || local.Description != "" || local.IncludesPattern == nil ||
		*local.IncludesPattern != "com/**" {
		t.Fatalf("local %+v %v", local, err)
	}
	// Deleting a member removes it from the virtual repositories too.
	if v, _ := db.Repos().Get(ctx, "maven-all"); strings.Join(v.Repositories, ",") != "libs-local" {
		t.Fatalf("virtual members %v", v.Repositories)
	}
	proxy, err := db.Configs().Get(ctx, types.ConfigProxy, "corp")
	if err != nil || len(proxy.Secrets) != 0 || !strings.Contains(string(proxy.Document), "8080") {
		t.Fatalf("proxy %+v %v", proxy, err)
	}
	// Deleting what is already gone is not a change.
	res = mustApply(t, db, "remoteRepositories:\n  maven-central: null\n")
	if len(res.Deleted) != 0 || len(res.Unchanged) != 1 {
		t.Fatalf("repeat delete %+v", res)
	}
}

func TestApplyRefusesAndWritesNothing(t *testing.T) {
	ctx := context.Background()
	db := inmem.New()
	seedRepo(t, db, repo.Config{Key: "remote-one", Class: repo.Remote, URL: "https://up.example"})
	seedRepo(t, db, repo.Config{Key: "Mixed-Case", Class: repo.Local})
	for name, doc := range map[string]string{
		"protected":            "localRepositories:\n  artifactory-build-info: null\n",
		"delete wrong class":   "localRepositories:\n  remote-one: null\n",
		"change class":         "localRepositories:\n  remote-one:\n    description: x\n",
		"change package type":  "remoteRepositories:\n  remote-one:\n    type: npm\n",
		"key taken":            "localRepositories:\n  mixed-case:\n    type: generic\n",
		"invalid new":          "localRepositories:\n  bad:\n    type: not-a-type\n",
		"unknown member":       "virtualRepositories:\n  v:\n    type: generic\n    repositories: [nowhere]\n",
		"member deleted":       "localRepositories:\n  Mixed-Case: null\nvirtualRepositories:\n  v:\n    type: generic\n    repositories: [Mixed-Case]\n",
		"missing key pair":     "localRepositories:\n  signed:\n    type: debian\n    primaryKeyPairRef: absent\n",
		"proxy without host":   "proxies:\n  p:\n    port: 80\n",
		"proxy bad port":       "proxies:\n  p:\n    host: h\n    port: 70000\n",
		"proxy fractional":     "proxies:\n  p:\n    host: h\n    port: 80.5\n",
		"proxy secret type":    "proxies:\n  p:\n    host: h\n    port: 80\n    password: 12\n",
		"layout no pattern":    "repoLayouts:\n  l:\n    descriptorPathPattern: x\n",
		"layout not a string":  "repoLayouts:\n  l:\n    artifactPathPattern: 3\n",
		"layout not a boolean": "repoLayouts:\n  l:\n    artifactPathPattern: \"[org]\"\n    distinctiveDescriptorPathPattern: maybe\n",
		"layout bad pattern":   "repoLayouts:\n  l:\n    artifactPathPattern: \"[org\"\n",
		"properties scalar":    "propertySets:\n  s:\n    properties: 3\n",
		"property scalar":      "propertySets:\n  s:\n    properties:\n      p: 3\n",
	} {
		// Every bad document carries a good entry too, in a section it
		// does not already use, which must not be written either.
		good, kind := "proxies:\n  good:\n    host: h\n    port: 80\n", types.ConfigProxy
		if strings.Contains(doc, "proxies:") {
			good, kind = "propertySets:\n  good:\n    visible: true\n", types.ConfigPropertySet
		}
		res, err := apply(t, db, doc+good)
		if !errors.Is(err, ErrInvalid) {
			t.Errorf("%s: %+v, %v", name, res, err)
		}
		if _, err := db.Configs().Get(ctx, kind, "good"); !errors.Is(err, store.ErrNotFound) {
			t.Fatalf("%s: a refused patch wrote its good entry", name)
		}
	}
}

func TestApplyDryRun(t *testing.T) {
	db := inmem.New()
	p, err := Parse([]byte(full), env(map[string]string{"PROXY_PASSWORD": "pp"}))
	if err != nil {
		t.Fatal(err)
	}
	res, err := Apply(context.Background(), db, p, now, true)
	if err != nil || len(res.Created) != 6 || !strings.HasPrefix(res.String(), "Dry run") {
		t.Fatalf("dry run %+v %v", res, err)
	}
	if repos, _ := db.Repos().List(context.Background(), store.RepoFilter{}); len(repos) != 0 {
		t.Fatalf("dry run wrote %v", repos)
	}
}

// faulty fails the named store operations.
type faulty struct {
	*inmem.Store
	fail string
}

var errBoom = errors.New("boom")

func (f faulty) Repos() store.RepoStore     { return faultyRepos{f.Store.Repos(), f.fail} }
func (f faulty) Configs() store.ConfigStore { return faultyConfigs{f.Store.Configs(), f.fail} }

type faultyRepos struct {
	store.RepoStore
	fail string
}

func (f faultyRepos) List(ctx context.Context, rf store.RepoFilter) ([]repo.Config, error) {
	if f.fail == "list" {
		return nil, errBoom
	}
	return f.RepoStore.List(ctx, rf) //nolint:wrapcheck // test double
}

func (f faultyRepos) Create(ctx context.Context, c repo.Config) error {
	if f.fail == "create" {
		return errBoom
	}
	return f.RepoStore.Create(ctx, c) //nolint:wrapcheck // test double
}

func (f faultyRepos) Delete(ctx context.Context, key string) error {
	if f.fail == "delete" {
		return errBoom
	}
	return f.RepoStore.Delete(ctx, key) //nolint:wrapcheck // test double
}

type faultyConfigs struct {
	store.ConfigStore
	fail string
}

func (f faultyConfigs) Get(
	ctx context.Context,
	k types.ConfigKind,
	key string,
) (types.ConfigDocument, error) {
	if f.fail == "config get" {
		return types.ConfigDocument{}, errBoom
	}
	return f.ConfigStore.Get(ctx, k, key) //nolint:wrapcheck // test double
}

func (f faultyConfigs) Put(ctx context.Context, d types.ConfigDocument) error {
	if f.fail == "config put" {
		return errBoom
	}
	return f.ConfigStore.Put(ctx, d) //nolint:wrapcheck // test double
}

func (f faultyConfigs) Delete(ctx context.Context, k types.ConfigKind, key string) error {
	if f.fail == "config delete" {
		return errBoom
	}
	return f.ConfigStore.Delete(ctx, k, key) //nolint:wrapcheck // test double
}

func TestApplyStoreErrors(t *testing.T) {
	for fail, doc := range map[string]string{
		"list":          "localRepositories:\n  x:\n    type: generic\n",
		"create":        "localRepositories:\n  x:\n    type: generic\n",
		"delete":        "localRepositories:\n  gone: null\n",
		"config get":    "proxies:\n  p:\n    host: h\n    port: 80\n",
		"config put":    "proxies:\n  p:\n    host: h\n    port: 80\n",
		"config delete": "proxies:\n  old: null\n",
	} {
		db := inmem.New()
		seedRepo(t, db, repo.Config{Key: "gone", Class: repo.Local})
		if err := db.Configs().Put(context.Background(), types.ConfigDocument{
			Kind: types.ConfigProxy, Key: "old", Document: []byte(`{"host":"h","port":1}`),
		}); err != nil {
			t.Fatal(err)
		}
		_, err := apply(t, faulty{db, fail}, doc)
		if !errors.Is(err, errBoom) || errors.Is(err, ErrInvalid) {
			t.Errorf("%s: %v", fail, err)
		}
	}
}

func TestStoredDocumentThatIsNotJSON(t *testing.T) {
	db := inmem.New()
	if err := db.Configs().Put(context.Background(), types.ConfigDocument{
		Kind: types.ConfigProxy, Key: "p", Document: []byte("not json"),
	}); err != nil {
		t.Fatal(err)
	}
	if _, err := apply(t, db, "proxies:\n  p:\n    host: h\n    port: 80\n"); err == nil {
		t.Fatal("merged onto an unreadable stored document")
	}
	if sameDocument(types.ConfigDocument{Document: []byte("not json")}, []byte("{}"), nil) {
		t.Fatal("an unreadable stored document compared equal")
	}
}
