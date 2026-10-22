package descriptor

import (
	"context"
	"errors"
	"fmt"
	"strings"
	"testing"
	"time"

	"github.com/weaveplatform/artifactweave/internal/auth"
	"github.com/weaveplatform/artifactweave/internal/format"
	"github.com/weaveplatform/artifactweave/pkg/repo"
	"github.com/weaveplatform/artifactweave/pkg/store"
	"github.com/weaveplatform/artifactweave/pkg/store/inmem"
	"github.com/weaveplatform/artifactweave/pkg/types"
)

func TestP9bDescriptorClassMatrix(t *testing.T) {
	for _, tc := range []struct {
		typeName string
		allowed  map[repo.Class]bool
	}{
		{"luarocks", map[repo.Class]bool{repo.Local: true, repo.Remote: true, repo.Virtual: true, repo.Federated: true}},
		{"nimmodel", map[repo.Class]bool{repo.Remote: true}},
		{"skills", map[repo.Class]bool{repo.Local: true}},
		{"agentplugins", map[repo.Class]bool{repo.Local: true}},
		{"agentpackages", map[repo.Class]bool{repo.Local: true, repo.Federated: true}},
	} {
		for _, pair := range []struct {
			class   repo.Class
			section string
		}{
			{repo.Local, SectionLocal},
			{repo.Remote, SectionRemote},
			{repo.Virtual, SectionVirtual},
			{repo.Federated, SectionFederated},
		} {
			t.Run(tc.typeName+"/"+string(pair.class), func(t *testing.T) {
				db := inmem.New()
				doc := fmt.Sprintf("%s:\n  pkg:\n    type: %s\n", pair.section, tc.typeName)
				if pair.class == repo.Remote {
					doc += "    url: https://up.example\n"
				}
				p, err := Parse([]byte(doc), noEnv)
				if err != nil {
					t.Fatal(err)
				}
				_, err = Apply(context.Background(), db, p, now, false)
				if !tc.allowed[pair.class] {
					if !errors.Is(err, ErrInvalid) {
						t.Fatalf("invalid type/class should fail: %v", err)
					}
					return
				}
				if err != nil {
					t.Fatal(err)
				}
				got, err := db.Repos().Get(context.Background(), "pkg")
				if err != nil || got.Class != pair.class || got.PackageType != repo.PackageType(tc.typeName) {
					t.Fatalf("stored %+v: %v", got, err)
				}
			})
		}
	}
}

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
	// The answer counts the document's 15 values, not what changed, so the
	// second apply gives the same number.
	if got := res.Merged(); got != "15 changes to config merged successfully" {
		t.Fatalf("answer %q", got)
	}
	if !strings.Contains(res.String(), "Unchanged: localRepositories.libs-local") {
		t.Fatalf("summary:\n%s", res)
	}
}

// TestApplyDocumentedSpellings stores the descriptor's spellings where the
// repositories API stores the same fields: yumGroupFileNames, p2.urls and
// cocoaPods.cocoaPodsSpecsRepoUrl among them (#396).
func TestApplyDocumentedSpellings(t *testing.T) {
	ctx := context.Background()
	db := inmem.New()
	res := mustApply(t, db, `
localRepositories:
  state-local:
    type: terraformbe
  rpm-local:
    type: rpm
    yumGroupFileNames: groups.xml
    downloadRedirect:
      enabled: true
remoteRepositories:
  pods-remote:
    type: cocoapods
    url: https://cdn.cocoapods.example
    remoteRepoLayout: simple-default
    cocoaPods:
      cocoaPodsSpecsRepoUrl: https://specs.example
      specRepoProvider:
        provider: github
virtualRepositories:
  urls-virtual:
    type: p2
    p2:
      urls: [https://p2.example]
`)
	if len(res.Created) != 4 {
		t.Fatalf("created %+v", res)
	}
	state, _ := db.Repos().Get(ctx, "state-local")
	rpm, _ := db.Repos().Get(ctx, "rpm-local")
	pods, _ := db.Repos().Get(ctx, "pods-remote")
	urls, _ := db.Repos().Get(ctx, "urls-virtual")
	if state.PackageType != repo.TerraformBackend ||
		!rpm.DownloadRedirect.Bool() || rpm.YumGroupFileNames != "groups.xml" ||
		pods.RemoteRepoLayoutRef != "simple-default" ||
		pods.PodsSpecsRepoURL != "https://specs.example" ||
		strings.Join(urls.P2URLs, " ") != "https://p2.example" {
		t.Fatalf("stored %+v %+v %+v %+v", state, rpm, pods, urls)
	}
	res = mustApply(t, db, "localRepositories:\n  rpm-local:\n    yumGroupFileNames: other.xml\n")
	if strings.Join(res.Updated, " ") != "localRepositories.rpm-local" {
		t.Fatalf("update %+v", res)
	}
	if rpm, _ = db.Repos().Get(ctx, "rpm-local"); rpm.YumGroupFileNames != "other.xml" {
		t.Fatalf("updated %+v", rpm)
	}
	res = mustApply(t, db, "localRepositories:\n  rpm-local:\n    yumGroupFileNames: null\n")
	if rpm, _ = db.Repos().
		Get(ctx, "rpm-local"); len(res.Updated) != 1 ||
		rpm.YumGroupFileNames != "" {
		t.Fatalf("cleared %+v %+v", res, rpm)
	}
}

// TestApplyNullClearsString clears a string field and leaves the rest.
func TestApplyNullClearsString(t *testing.T) {
	ctx := context.Background()
	db := inmem.New()
	mustApply(t, db, `
remoteRepositories:
  r:
    type: generic
    url: https://up.example
    username: u
    password: p
    remoteRepoLayout: simple-default
    blackedOut: true
`)
	res := mustApply(t, db, `
remoteRepositories:
  r:
    username: null
    password: ~
    remoteRepoLayout: null
`)
	r, err := db.Repos().Get(ctx, "r")
	if strings.Join(res.Updated, " ") != "remoteRepositories.r" || err != nil ||
		r.Username != "" || r.Password != "" || r.RemoteRepoLayoutRef != "" ||
		r.BlackedOut == nil || !*r.BlackedOut {
		t.Fatalf("%+v: %+v %v", res, r, err)
	}
	if _, err := Parse(
		[]byte("remoteRepositories:\n  r:\n    blackedOut: null\n"),
		noEnv,
	); err == nil {
		t.Fatal("parsed a null on a boolean")
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

// A repository the patch deletes goes to the trash can as deleted by the
// caller of PATCH /api/system/configuration, or by no one for config apply,
// which runs outside a request (#504).
func TestApplyTrashesDeletedRepositoriesAsTheCaller(t *testing.T) {
	for by, ctx := range map[string]context.Context{
		"admin": auth.WithPrincipal(context.Background(), auth.Principal{Name: "admin", Admin: true}),
		"":      context.Background(),
	} {
		db := inmem.New()
		seedRepo(t, db, repo.Config{Key: "gone", Class: repo.Local})
		if _, err := db.Items().Put(ctx, types.Item{RepoKey: "gone", Name: "f.txt"}); err != nil {
			t.Fatal(err)
		}
		p, err := Parse([]byte("localRepositories:\n  gone: null\n"), env(nil))
		if err != nil {
			t.Fatal(err)
		}
		if _, err := Apply(ctx, db, p, now, false); err != nil {
			t.Fatal(err)
		}
		trashed, _, err := db.Trash().List(ctx, "gone", "", store.Page{})
		if err != nil || len(trashed) != 1 || trashed[0].DeletedBy != by {
			t.Fatalf("by %q: %+v, %v", by, trashed, err)
		}
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
		"reserved key":         "localRepositories:\n  Release-Bundles:\n    type: generic\n",
		"forbidden character":  "localRepositories:\n  \"a|b\":\n    type: generic\n",
		"docker key case":      "localRepositories:\n  Docker-Up:\n    type: docker\n",
		"remote key too long":  "remoteRepositories:\n  " + strings.Repeat("r", 59) + ":\n    type: generic\n    url: https://x\n",
		"unknown member":       "virtualRepositories:\n  v:\n    type: generic\n    repositories: [nowhere]\n",
		"member deleted":       "localRepositories:\n  Mixed-Case: null\nvirtualRepositories:\n  v:\n    type: generic\n    repositories: [Mixed-Case]\n",
		"member of other type": "virtualRepositories:\n  v:\n    type: npm\n    repositories: [remote-one]\n",
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

// An entry never changes a repository's key, in case either: the entry's
// own key field must match its name exactly, and a name that spells a
// stored key in another case is a create the taken key refuses. The seeded
// build-info repository takes updates and keeps its package type (#472).
func TestApplyKeepsKeysAndUpdatesBuildInfo(t *testing.T) {
	ctx := context.Background()
	db := inmem.New()
	seedRepo(t, db, repo.Config{Key: "libs-local", Class: repo.Local})
	seedRepo(t, db, repo.Config{
		Key: types.BuildInfoRepository, Class: repo.Local, PackageType: repo.BuildInfo,
	})
	for _, doc := range []string{
		"localRepositories:\n  libs-local:\n    key: LIBS-LOCAL\n",
		"localRepositories:\n  LIBS-LOCAL:\n    description: x\n",
	} {
		p, err := Parse([]byte(doc), env(nil))
		if err == nil {
			_, err = Apply(ctx, db, p, now, false)
		}
		if !errors.Is(err, ErrInvalid) {
			t.Errorf("%q: %v", doc, err)
		}
	}
	res := mustApply(
		t,
		db,
		"localRepositories:\n  artifactory-build-info:\n    type: buildinfo\n    description: builds\n",
	)
	got, err := db.Repos().Get(ctx, types.BuildInfoRepository)
	if err != nil || len(res.Updated) != 1 || got.Description != "builds" ||
		got.PackageType != repo.BuildInfo {
		t.Fatalf("build-info %+v %+v %v", res, got, err)
	}
}

// A virtual repository may have no members, and keeps taking changes once
// its last member is deleted; a Maven virtual repository aggregates the rest
// of its family.
func TestApplyVirtualMembers(t *testing.T) {
	ctx := context.Background()
	db := inmem.New()
	res := mustApply(t, db, `
localRepositories:
  gradle-local:
    type: gradle
virtualRepositories:
  npm-empty:
    type: npm
  npm-listed-empty:
    type: npm
    repositories: []
  maven-all:
    type: maven
    repositories: [gradle-local]
`)
	if len(res.Created) != 4 {
		t.Fatalf("created %v", res.Created)
	}
	mustApply(t, db, "localRepositories:\n  gradle-local: null\n")
	res = mustApply(t, db, "virtualRepositories:\n  maven-all:\n    description: no members\n")
	if strings.Join(res.Updated, " ") != "virtualRepositories.maven-all" {
		t.Fatalf("update of an emptied virtual %+v", res)
	}
	if v, err := db.Repos().Get(ctx, "maven-all"); err != nil || len(v.Repositories) != 0 {
		t.Fatalf("maven-all %+v %v", v, err)
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

func (f faulty) WorkQueue() store.WorkQueue { return faultyWork{f.Store.WorkQueue(), f.fail} }

type faultyWork struct {
	store.WorkQueue
	fail string
}

func (f faultyWork) Enqueue(
	ctx context.Context,
	kind, key string,
	payload []byte,
	at time.Time,
) error {
	if f.fail == "enqueue" {
		return errBoom
	}
	return f.WorkQueue.Enqueue(ctx, kind, key, payload, at) //nolint:wrapcheck // test double
}

// A patch that gives a Terraform repository the terraformType it lacked
// queues its reindex in the patch's transaction, so the files it stored are
// indexed as that type says (decision 0040); one that changes it is
// refused, and one that keeps it queues nothing.
func TestApplyQueuesATerraformReindex(t *testing.T) {
	ctx := context.Background()
	db := inmem.New()
	seedRepo(t, db, repo.Config{Key: "tf", Class: repo.Local, PackageType: repo.Terraform})
	queued := func() int64 {
		t.Helper()
		stats, err := db.WorkQueue().Stats(ctx)
		if err != nil {
			t.Fatal(err)
		}
		var n int64
		for _, s := range stats {
			if s.Kind == format.KindReindex {
				n += s.Pending
			}
		}
		return n
	}
	mustApply(t, db, "localRepositories:\n  tf:\n    terraformType: module\n")
	if c, err := db.Repos().Get(ctx, "tf"); err != nil || c.TerraformType != repo.TerraformModule {
		t.Fatalf("%+v %v", c.TerraformType, err)
	}
	if _, err := db.WorkQueue().Get(ctx, format.KindReindex, "tf"); err != nil || queued() != 1 {
		t.Fatalf("no reindex queued: %v", err)
	}
	mustApply(t, db, "localRepositories:\n  tf:\n    terraformType: MODULE\n    description: x\n")
	if queued() != 1 {
		t.Fatalf("%d reindexes queued", queued())
	}
	if _, err := apply(
		t,
		db,
		"localRepositories:\n  tf:\n    terraformType: provider\n",
	); !errors.Is(
		err, ErrInvalid) ||
		!strings.Contains(
			err.Error(),
			"The field 'terraformType' of a Terraform repository cannot change once set",
		) {
		t.Fatalf("a change: %v", err)
	}
	fresh := inmem.New()
	seedRepo(t, fresh, repo.Config{Key: "tf", Class: repo.Local, PackageType: repo.Terraform})
	if _, err := apply(t, faulty{fresh, "enqueue"},
		"localRepositories:\n  tf:\n    terraformType: module\n"); !errors.Is(err, errBoom) {
		t.Fatalf("a failed enqueue: %v", err)
	}
	if c, _ := fresh.Repos().Get(ctx, "tf"); c.TerraformType != "" {
		t.Fatalf("the update outlived its failed reindex: %s", c.TerraformType)
	}
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

// TestApplyEnvironments checks Artifactory's rule for a repository's
// environments through the descriptor: ignored outside a project, DEV by
// default in one, a name in another case stored in the environment's own
// spelling and unchanged on the next apply, and an unknown name refusing the
// whole descriptor.
func TestApplyEnvironments(t *testing.T) {
	ctx := context.Background()
	db := inmem.New()
	const doc = `
localRepositories:
  env-local:
    type: generic
    projectKey: proj
    environments: [prod]
  default-local:
    type: generic
    projectKey: proj
  loose-local:
    type: generic
    environments: [nope]
`
	if res := mustApply(t, db, doc); len(res.Created) != 3 {
		t.Fatalf("first apply %+v", res)
	}
	for key, want := range map[string]string{
		"env-local": "PROD", "default-local": "DEV", "loose-local": "",
	} {
		c, err := db.Repos().Get(ctx, key)
		if err != nil || strings.Join(c.Environments, ",") != want {
			t.Fatalf("%s: %v %v, want %q", key, c.Environments, err, want)
		}
	}
	if res := mustApply(t, db, doc); len(res.Unchanged) != 3 || len(res.Updated) != 0 {
		t.Fatalf("second apply %+v: the stored spelling and defaults must compare equal", res)
	}
	_, err := apply(t, db, `
localRepositories:
  other-local:
    type: generic
  env-local:
    environments: [DEV, nope]
`)
	if !errors.Is(err, ErrInvalid) ||
		!strings.Contains(err.Error(), "Environment 'nope' does not exist") {
		t.Fatalf("unknown environment: %v", err)
	}
	if _, err := db.Repos().Get(ctx, "other-local"); err == nil {
		t.Fatal("a refused descriptor created a repository")
	}
	if c, _ := db.Repos().Get(ctx, "env-local"); strings.Join(c.Environments, ",") != "PROD" {
		t.Fatalf("a refused descriptor changed a repository: %v", c.Environments)
	}
}
