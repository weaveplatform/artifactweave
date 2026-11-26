package federation_test

import (
	"encoding/json"
	"log/slog"
	"net/http"
	"slices"
	"strconv"
	"strings"
	"testing"

	"github.com/weaveplatform/artifactweave/internal/access/accesstest"
	"github.com/weaveplatform/artifactweave/internal/access/core/authn"
	"github.com/weaveplatform/artifactweave/internal/app/adapters/inmem"
	"github.com/weaveplatform/artifactweave/internal/artifacts/api/repo"
	"github.com/weaveplatform/artifactweave/internal/federation/core/model"
	"github.com/weaveplatform/artifactweave/test/server/support"
	"github.com/weaveplatform/artifactweave/test/server/support/harness"
)

// federatedWorld is one instance at https://a.example with two paired
// peers, B (two-way trust) and R (receiving-only trust), and a non-admin
// user, dev.
type federatedWorld struct {
	t  *testing.T
	db *inmem.Store
	h  http.Handler
}

func newFederatedWorld(t *testing.T, baseURL string) *federatedWorld {
	t.Helper()
	db := inmem.New()
	support.Seed(t, db)
	if err := db.Users().Create(t.Context(), accesstest.User{Name: "dev", PasswordHash: support.CheapHash}); err != nil {
		t.Fatal(err)
	}
	for _, p := range []model.Peer{
		{InstanceID: "b", BaseURL: "https://b.example", UseCases: []model.UseCase{model.UseFederatedRepo}},
		{InstanceID: "b2", BaseURL: "https://b2.example", UseCases: []model.UseCase{model.UseFederatedRepo}},
		{InstanceID: "r", BaseURL: "https://r.example", UseCases: []model.UseCase{model.UseFederatedRepoRead}},
	} {
		if err := db.Peers().Put(t.Context(), p); err != nil {
			t.Fatal(err)
		}
	}
	for _, c := range []repo.Config{
		{Key: "local", Class: repo.Local, PackageType: repo.Generic},
		{Key: "tf", Class: repo.Local, PackageType: repo.TerraformBackend},
		{Key: "remote", Class: repo.Remote, PackageType: repo.Generic, URL: "https://up.example"},
	} {
		if err := db.Repos().Create(t.Context(), *repo.Defaults(&c)); err != nil {
			t.Fatal(err)
		}
	}
	auth, err := authn.NewAuthenticator(db.Users(), db.Settings(), authn.Options{Hash: support.TestHash, Groups: db.Groups()})
	if err != nil {
		t.Fatal(err)
	}
	return &federatedWorld{t: t, db: db, h: harness.Router(harness.Config{
		Store: db, Auth: auth, PasswordHash: support.TestHash, BaseURL: baseURL,
		Logger: slog.New(slog.DiscardHandler),
	})}
}

// call sends method path as user ("" is anonymous) and requires status and,
// when want is set, a body containing it.
func (w *federatedWorld) call(user, method, path, body string, status int, want string) string {
	w.t.Helper()
	headers := map[string]string{}
	if user != "" {
		headers = support.Basic(user, "pw")
	}
	if body != "" {
		headers["Content-Type"] = "application/json"
	}
	rec := support.Do(w.t, w.h, method, path, headers, body)
	if rec.Code != status || !strings.Contains(rec.Body.String(), want) {
		w.t.Fatalf("%s %s as %q: %d %s, want %d containing %q", method, path, user, rec.Code, rec.Body, status, want)
	}
	return rec.Body.String()
}

func (w *federatedWorld) memberURLs(key string) []string {
	w.t.Helper()
	var c struct {
		Rclass  string `json:"rclass"`
		Members []struct {
			URL string `json:"url"`
		} `json:"members"`
	}
	if err := json.Unmarshal([]byte(w.call("admin", http.MethodGet, "/artifactory/api/repositories/"+key, "", 200, "")), &c); err != nil {
		w.t.Fatal(err)
	}
	out := []string{c.Rclass}
	for _, m := range c.Members {
		out = append(out, m.URL)
	}
	return out
}

// members lists the membership table's rows for key.
func (w *federatedWorld) members(key string) []string {
	w.t.Helper()
	ms, err := w.db.Federation().Members(w.t.Context(), key)
	if err != nil {
		w.t.Fatal(err)
	}
	var out []string
	for _, m := range ms {
		out = append(out, m.PeerInstanceID+" "+m.BaseURL+" "+m.RemoteRepoKey)
	}
	return out
}

// Federated repository members through the repositories API (#1930): each
// member must be a paired peer, the instance lists itself at its base URL
// whatever form it was given in, and refusals are 400s that store nothing.
func TestAccFederatedRepositoryMembers(t *testing.T) {
	t.Parallel()
	w := newFederatedWorld(t, "https://a.example/")
	const put = "/artifactory/api/repositories/"

	w.call("admin", http.MethodPut, put+"fed",
		`{"rclass":"federated","packageType":"generic","members":[{"url":"https://d.example/artifactory/fed"}]}`, 400,
		`Federated member 'https://d.example/artifactory/fed' is not a paired peer: pair the instances for federated-repo/* first`)
	w.call("admin", http.MethodPut, put+"fed",
		`{"rclass":"federated","packageType":"generic","members":[{"url":"https://r.example/artifactory/fed"}]}`, 400,
		`is not a paired peer: pair the instances for federated-repo/* first`)
	w.call("admin", http.MethodPut, put+"receiver",
		`{"rclass":"federated","packageType":"generic","members":[{"url":"https://b.example/artifactory/fed","mode":"receiver"}]}`, 200, "")
	var receiver struct{ Members []repo.Member }
	if err := json.Unmarshal([]byte(w.call("admin", http.MethodGet, put+"receiver", "", 200, "")), &receiver); err != nil {
		t.Fatal(err)
	}
	if !slices.ContainsFunc(receiver.Members, func(m repo.Member) bool {
		return m.URL == "https://b.example/artifactory/fed" && m.Mode == repo.ModeReceiver
	}) {
		t.Fatalf("receiver mode not retained by API: %+v", receiver.Members)
	}
	receiverMembers, err := w.db.Federation().Members(t.Context(), "receiver")
	if err != nil || len(receiverMembers) != 1 || receiverMembers[0].Mode != repo.ModeReceiver {
		t.Fatalf("receiver membership: %+v, %v", receiverMembers, err)
	}
	w.call("admin", http.MethodPut, put+"fed",
		`{"rclass":"federated","packageType":"generic","members":[{"url":"not a url"}]}`, 400, `members.url`)
	eleven := make([]string, 11)
	for i := range eleven {
		eleven[i] = `{"url":"https://b.example/artifactory/f` + strconv.Itoa(i) + `"}`
	}
	w.call("admin", http.MethodPut, put+"fed",
		`{"rclass":"federated","packageType":"generic","members":[`+strings.Join(eleven, ",")+`]}`, 400,
		"allows at most 10 members, this instance's included")
	w.call("admin", http.MethodPut, put+"fed",
		`{"rclass":"federated","packageType":"generic","members":[{"url":"https://b.example/fed","mode":"sideways"}]}`, 400,
		`is not one of bidirectional, receiver`)
	w.call("admin", http.MethodGet, put+"fed", "", 404, "")
	w.call("dev", http.MethodPut, put+"fed", `{"rclass":"federated","packageType":"generic"}`, 403, "")

	w.call("admin", http.MethodPut, put+"fed",
		`{"rclass":"federated","packageType":"generic","members":[{"url":"https://a.example/fed/"},{"url":"https://b.example/fedb","enabled":true}]}`, 200,
		"Successfully created repository 'fed'")
	if got, want := w.memberURLs("fed"), []string{"federated", "https://a.example/artifactory/fed", "https://b.example/artifactory/fedb"}; !slices.Equal(got, want) {
		t.Fatalf("members %v, want %v", got, want)
	}
	if got := w.members("fed"); !slices.Equal(got, []string{"b https://b.example/artifactory fedb"}) {
		t.Fatalf("membership %v", got)
	}

	// An update is checked as a create is; a refused one changes nothing.
	w.call("admin", http.MethodPost, put+"fed", `{"members":[{"url":"https://d.example/fed"}]}`, 400, "is not a paired peer")
	w.call("admin", http.MethodPost, put+"fed", `{"members":[{"url":"https://b2.example/artifactory/fed"}]}`, 200, "")
	if got, want := w.memberURLs("fed"), []string{"federated", "https://b2.example/artifactory/fed", "https://a.example/artifactory/fed"}; !slices.Equal(got, want) {
		t.Fatalf("members after update %v, want %v", got, want)
	}

	// federatedRepoUrlBase, once set, is the instance's own member base.
	w.call("admin", http.MethodPatch, "/artifactory/api/system/configuration", "federatedRepoUrlBase: https://fed.example/\n", 200, "")
	if got := w.memberURLs("fed"); got[2] != "https://fed.example/artifactory/fed" {
		t.Fatalf("members with federatedRepoUrlBase %v", got)
	}
	w.call("admin", http.MethodPatch, "/artifactory/api/system/configuration", "federatedRepoUrlBase: ftp://x\n", 400, "federatedRepoUrlBase")
	w.call("admin", http.MethodDelete, put+"fed", "", 200, "")
	if got := w.members("fed"); len(got) != 0 {
		t.Fatalf("a deleted repository kept members %v", got)
	}
}

// Without urlBase, federatedRepoUrlBase or a configured base URL, a
// federated repository with members is refused with 7.x's message; one
// without any is stored as it is.
func TestAccFederatedRepositoryNeedsBaseURL(t *testing.T) {
	t.Parallel()
	w := newFederatedWorld(t, "")
	w.call("admin", http.MethodPut, "/artifactory/api/repositories/fed",
		`{"rclass":"federated","packageType":"generic","members":[{"url":"https://b.example/artifactory/fed"}]}`, 400,
		"Custom base URL must be configured for federated repositories' functionality")
	w.call("admin", http.MethodPut, "/artifactory/api/repositories/fed", `{"rclass":"federated","packageType":"generic"}`, 200, "")
	if got := w.memberURLs("fed"); !slices.Equal(got, []string{"federated"}) {
		t.Fatalf("members %v", got)
	}
}

// configSync, migrate, convertToLocal and the member URL rewrite (#1930).
func TestAccFederatedRepositoryOperations(t *testing.T) {
	t.Parallel()
	w := newFederatedWorld(t, "https://a.example")
	const fed = "/artifactory/api/federation/"

	for _, op := range []struct{ method, path, body string }{
		{http.MethodPost, fed + "configSync/local", ""},
		{http.MethodPost, fed + "migrate/local", ""},
		{http.MethodPost, fed + "convertToLocal", `["local"]`},
		{http.MethodPost, fed + "replaceUrl", `{"oldBaseUrl":"https://b.example","newBaseUrl":"https://b2.example"}`},
		{http.MethodPost, fed + "updateMemberUrl", `{"oldBaseUrl":"https://b.example","newBaseUrl":"https://b2.example"}`},
	} {
		w.call("dev", op.method, op.path, op.body, http.StatusForbidden, "")
		w.call("", op.method, op.path, op.body, http.StatusUnauthorized, "")
	}

	w.call("admin", http.MethodPost, fed+"configSync/missing", "", 404, "Repository 'missing' does not exist")
	w.call("admin", http.MethodPost, fed+"configSync/local", "", 400, "Repository 'local' is not a federated repository")
	w.call("admin", http.MethodPost, fed+"migrate/remote", "", 400, "Repository 'remote' is not a local repository")
	w.call("admin", http.MethodPost, fed+"migrate/tf", "", 400, "terraformbackend")
	w.call("admin", http.MethodPost, fed+"migrate/local", "", 200, "Repository 'local' was converted to a federated repository")
	if got := w.memberURLs("local"); !slices.Equal(got, []string{"federated", "https://a.example/artifactory/local"}) {
		t.Fatalf("migrated %v", got)
	}
	w.call("admin", http.MethodPost, "/artifactory/api/repositories/local", `{"members":[{"url":"https://b.example/artifactory/local"}]}`, 200, "")
	if got := w.call("admin", http.MethodPost, fed+"configSync/local", "", 200, ""); got != "Configuration sync executed successfully" {
		t.Fatalf("configSync answered %q", got)
	}

	w.call("admin", http.MethodPost, fed+"replaceUrl", `{"oldBaseUrl":"https://b.example","newBaseUrl":"https://d.example"}`, 400, "is not a paired peer")
	if got := w.call("admin", http.MethodPost, fed+"replaceUrl", `{"oldBaseUrl":"https://b.example","newBaseUrl":"https://b2.example"}`, 200, ""); got !=
		"Binding URL from URL https://b.example to new https://b2.example finished." {
		t.Fatalf("replaceUrl answered %q", got)
	}
	if got := w.memberURLs("local"); got[1] != "https://b2.example/artifactory/local" {
		t.Fatalf("after replaceUrl %v", got)
	}
	w.call("admin", http.MethodPost, fed+"updateMemberUrl", `{"oldBaseUrl":"https://b2.example/artifactory","newBaseUrl":"https://b.example"}`, 200,
		"Binding URL from URL https://b2.example/artifactory to new https://b.example finished.")
	w.call("admin", http.MethodPost, fed+"replaceUrl", `{"oldBaseUrl":"","newBaseUrl":"https://b.example"}`, 400, "oldBaseUrl and newBaseUrl are required")

	w.call("admin", http.MethodPost, fed+"convertToLocal", `[]`, 400, "A list of federated repository keys is required")
	w.call("admin", http.MethodPost, fed+"convertToLocal", `["local"]`, 400,
		`{"failedConversionRepositories":{"local":"Repository 'local' is federated with the enabled member 'https://b.example/artifactory/local': remove it first"}}`)
	w.call("admin", http.MethodPost, "/artifactory/api/repositories/local", `{"members":[{"url":"https://b.example/artifactory/local","enabled":"false"}]}`, 200, "")
	w.call("admin", http.MethodPost, fed+"convertToLocal", `["local","remote"]`, 200,
		`{"failedConversionRepositories":{"remote":"Repository 'remote' is not a federated repository"}}`)
	if got := w.memberURLs("local"); !slices.Equal(got, []string{"local"}) {
		t.Fatalf("converted %v", got)
	}
	w.call("admin", http.MethodPost, fed+"migrate/local", "", 200, "")
	w.call("admin", http.MethodPost, fed+"convertToLocal", `["local"]`, 200, `"Converted to local repositories: local"`)
}
