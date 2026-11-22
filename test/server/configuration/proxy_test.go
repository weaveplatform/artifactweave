package configuration_test

import (
	"bytes"
	"context"
	"crypto/tls"
	"encoding/base64"
	"encoding/json"
	"encoding/pem"
	"errors"
	"fmt"
	"io"
	"maps"
	"net/http"
	"net/url"
	"strings"
	"testing"
	"time"

	"github.com/weaveplatform/artifactweave/internal/app/adapters/inmem"
	appsql "github.com/weaveplatform/artifactweave/internal/app/adapters/sql"
	"github.com/weaveplatform/artifactweave/internal/artifacts/adapters/upstream"
	"github.com/weaveplatform/artifactweave/internal/artifacts/adapters/upstream/fakeupstream"
	"github.com/weaveplatform/artifactweave/internal/artifacts/api/repo"
	"github.com/weaveplatform/artifactweave/internal/artifacts/core/remote"
	"github.com/weaveplatform/artifactweave/internal/kernel"
	"github.com/weaveplatform/artifactweave/internal/platform/configdoc"
	"github.com/weaveplatform/artifactweave/internal/platform/crypt"
	"github.com/weaveplatform/artifactweave/internal/platform/sql/dialect"
	"github.com/weaveplatform/artifactweave/internal/platform/sql/sqltest"
	"github.com/weaveplatform/artifactweave/internal/platform/testkit/storekit"
	"github.com/weaveplatform/artifactweave/test/server/support/harness"
)

func TestAccProxyPasswordDescriptorMemory(t *testing.T) {
	t.Run("credentials", func(t *testing.T) { proxyPasswords(t, inmem.New(), nil) })
	t.Run("rollback", func(t *testing.T) { proxyRollback(t, inmem.New(), nil) })
}

func TestAccProxyPasswordDescriptorSQL(t *testing.T) {
	keys := storekit.Get[*crypt.Keyring](t)(crypt.New(bytes.Repeat([]byte{9}, 32)))
	sqltest.RunModule(t, appsql.Modules(), func(base *dialect.Store) *appsql.Store {
		return appsql.New(base.DB(), base.Dialect(), keys)
	}, func(t *testing.T, newStore func(*testing.T) *appsql.Store) {
		t.Run("credentials", func(t *testing.T) {
			db := newStore(t)
			proxyPasswords(t, db, proxySQLSnapshot(db, keys))
		})
		t.Run("rollback", func(t *testing.T) {
			db := newStore(t)
			proxyRollback(t, db, proxySQLSnapshot(db, keys))
		})
	}, sqltest.WithPostgres(), sqltest.WithKeyring(keys))
}

// The snapshot checks the physical secret rows as well as the public reader.
// Comparing it before and after a no-op also detects an unnecessary reseal.
type proxySnapshot func(*testing.T, configdoc.ConfigDocument) []byte

func proxySQLSnapshot(db *appsql.Store, keys *crypt.Keyring) proxySnapshot {
	return func(t *testing.T, document configdoc.ConfigDocument) []byte {
		t.Helper()
		ctx := t.Context()
		var public string
		storekit.Must(t, db.DB().QueryRowContext(ctx, db.Dialect().Rebind(
			"SELECT document FROM config_documents WHERE kind = ? AND key = ?"),
			"proxy", "corp").Scan(&public))
		if strings.Contains(public, "password") || public != string(document.Document) {
			t.Fatal("stored proxy public JSON differs from its secret-free document")
		}
		var rows int
		storekit.Must(t, db.DB().QueryRowContext(ctx, db.Dialect().Rebind(
			"SELECT COUNT(*) FROM config_secrets WHERE kind = ? AND key = ?"), "proxy", "corp").Scan(&rows))
		if rows != len(document.Secrets) {
			t.Fatal("proxy has unexpected physical secret rows")
		}
		if rows == 0 {
			return nil
		}
		var sealed []byte
		storekit.Must(t, db.DB().QueryRowContext(ctx, db.Dialect().Rebind(
			"SELECT value FROM config_secrets WHERE kind = ? AND key = ? AND name = ?"),
			"proxy", "corp", "password").Scan(&sealed))
		plain, err := keys.Open("config_secrets.value", "proxy/corp/password", sealed)
		if !crypt.IsSealed(sealed) || err != nil || string(plain) != document.Secrets["password"] {
			t.Fatal("proxy password did not retain its sealed purpose and row identity")
		}
		if _, err := keys.Open("config_secrets.value", "proxy/another/password", sealed); err == nil {
			t.Fatal("proxy password authenticated against another row")
		}
		return sealed
	}
}

func proxyPasswords(t *testing.T, db harness.Store, snapshot proxySnapshot) {
	t.Helper()
	api := configurationAPI(t, db)
	stamp := time.Date(2026, time.November, 22, 1, 0, 0, 0, time.UTC)
	var before configdoc.ConfigDocument
	var sealed []byte
	step := func(name, fields, password, category string) {
		t.Helper()
		stamp = stamp.Add(time.Hour)
		plan, err := api.Descriptor.Parse([]byte("proxies:\n  corp:\n"+fields), nil)
		storekit.Must(t, err)
		result, err := api.Descriptor.Apply(t.Context(), plan, stamp, false)
		storekit.Must(t, err)
		groups := map[string][]string{
			"created": result.Created, "updated": result.Updated,
			"deleted": result.Deleted, "unchanged": result.Unchanged,
		}
		for kind, entries := range groups {
			if kind == category {
				if len(entries) != 1 || entries[0] != "proxies.corp" {
					t.Fatal(name, "did not report the expected", category)
				}
			} else if len(entries) != 0 {
				t.Fatal(name, "unexpectedly reported", kind)
			}
		}
		after := proxyDocument(t, db)
		wantTime := stamp
		if category == "unchanged" {
			wantTime = before.Updated
		}
		if !after.Updated.Equal(wantTime) {
			t.Fatal(name, "did not preserve the expected document timestamp")
		}
		wantSecrets := 0
		if password != "" {
			wantSecrets = 1
		}
		if len(after.Secrets) != wantSecrets || after.Secrets["password"] != password {
			t.Fatal(name, "did not preserve the expected credential state")
		}
		for _, private := range []string{"password", "proxy-first-private", "proxy-rotated-private"} {
			if bytes.Contains(after.Document, []byte(private)) || strings.Contains(result.String(), private) ||
				strings.Contains(result.Merged(), private) {
				t.Fatal(name, "disclosed credentials in a public document or diagnostic")
			}
		}
		if snapshot != nil {
			next := snapshot(t, after)
			if category == "unchanged" && !bytes.Equal(next, sealed) {
				t.Fatal(name, "rewrote sealed credentials during a no-op")
			}
			sealed = next
		}
		before = after
	}
	step("create empty", "    host: proxy.invalid\n    port: 3128\n    password: ''\n", "", "created")
	step("repeat empty", "    password: ''\n", "", "unchanged")
	step("set credential", "    password: proxy-first-private\n", "proxy-first-private", "updated")
	step("repeat credential", "    password: proxy-first-private\n", "proxy-first-private", "unchanged")
	step("omit credential", "    host: proxy.invalid\n", "proxy-first-private", "unchanged")
	step("update public field", "    host: replacement.invalid\n", "proxy-first-private", "updated")
	step("rotate credential", "    password: proxy-rotated-private\n", "proxy-rotated-private", "updated")
	step("null clear", "    password: null\n", "", "updated")
	step("repeat null", "    password: null\n", "", "unchanged")
	step("restore credential", "    password: proxy-first-private\n", "proxy-first-private", "updated")
	step("empty clear", "    password: ''\n", "", "updated")
	step("repeat empty clear", "    password: ''\n", "", "unchanged")
}

func proxyDocument(t *testing.T, db harness.Store) configdoc.ConfigDocument {
	t.Helper()
	document, err := db.Configs().Get(t.Context(), configdoc.ConfigProxy, "corp")
	storekit.Must(t, err)
	return document
}

var errProxyWrite = errors.New("fixture final configuration write failed")

type failingProxyDocuments struct {
	configdoc.ConfigStore
	sawWrites bool
}

func (f *failingProxyDocuments) Put(ctx context.Context, document configdoc.ConfigDocument) error {
	if err := f.ConfigStore.Put(ctx, document); err != nil {
		return err //nolint:wrapcheck // test double
	}
	if document.Kind != configdoc.ConfigPropertySet {
		return nil
	}
	proxy, err := f.ConfigStore.Get(ctx, configdoc.ConfigProxy, "corp")
	f.sawWrites = err == nil && bytes.Contains(proxy.Document, []byte("replacement.invalid")) &&
		proxy.Secrets["password"] == "proxy-rotated-private"
	return errProxyWrite
}

type failingProxyStore struct {
	harness.Store
	documents *failingProxyDocuments
}

func (f failingProxyStore) Configs() configdoc.ConfigStore { return f.documents }

func proxyRollback(t *testing.T, db harness.Store, snapshot proxySnapshot) {
	t.Helper()
	api := configurationAPI(t, db)
	applyConfiguration(t, api, t.Context(), "proxies:\n  corp:\n    host: proxy.invalid\n    port: 3128\n    password: proxy-first-private\n")
	before := proxyDocument(t, db)
	var sealed []byte
	if snapshot != nil {
		sealed = snapshot(t, before)
	}
	fault := &failingProxyDocuments{ConfigStore: db.Configs()}
	failing := configurationAPI(t, failingProxyStore{Store: db, documents: fault})
	const patch = "proxies:\n  corp:\n    host: replacement.invalid\n    password: proxy-rotated-private\npropertySets:\n  last:\n    visible: true\n"
	plan, err := failing.Descriptor.Parse([]byte(patch), nil)
	storekit.Must(t, err)
	_, err = failing.Descriptor.Apply(t.Context(), plan, time.Now().Add(time.Hour), false)
	if !errors.Is(err, errProxyWrite) || !fault.sawWrites {
		t.Fatal("the descriptor did not fail after writing public configuration and rotated credentials")
	}
	if strings.Contains(err.Error(), "private") {
		t.Fatal("failed descriptor disclosed a credential")
	}
	after := proxyDocument(t, db)
	if !bytes.Equal(after.Document, before.Document) || !maps.Equal(after.Secrets, before.Secrets) ||
		!after.Updated.Equal(before.Updated) {
		t.Fatal("failed multi-entry descriptor retained public or credential changes")
	}
	if _, err := db.Configs().Get(t.Context(), configdoc.ConfigPropertySet, "last"); !errors.Is(err, kernel.ErrNotFound) {
		t.Fatal("failed descriptor retained its last public write")
	}
	if snapshot != nil && !bytes.Equal(snapshot(t, after), sealed) {
		t.Fatal("failed descriptor changed the original sealed password")
	}
	result := applyConfiguration(t, api, t.Context(), patch)
	if len(result.Updated) != 1 || len(result.Created) != 1 || proxyDocument(t, db).Secrets["password"] != "proxy-rotated-private" {
		t.Fatal("a healthy retry did not commit the descriptor after rollback")
	}
}

func TestAccPatchProxyPasswordKeepsAuthorizationAndValueCount(t *testing.T) {
	w := newRepoWorld(t)
	const initial = "proxies:\n  corp:\n    host: proxy.invalid\n    port: 3128\n    password: proxy-first-private\n"
	if rec := w.do(http.MethodPatch, configPath, "admin", initial, "Content-Type", "application/yaml"); rec.Code != http.StatusOK {
		t.Fatal("administrator could not configure the proxy", rec.Code)
	}
	before := proxyDocument(t, w.db)
	const clear = "proxies:\n  corp:\n    host: proxy.invalid\n    port: 3128\n    password: ''\n"
	for _, tc := range []struct {
		name, user string
		status     int
	}{{"anonymous", "", http.StatusUnauthorized}, {"reader", "reader", http.StatusForbidden}} {
		t.Run(tc.name, func(t *testing.T) {
			rec := w.do(http.MethodPatch, configPath, tc.user, clear, "Content-Type", "application/yaml")
			after := proxyDocument(t, w.db)
			if rec.Code != tc.status || !bytes.Equal(after.Document, before.Document) ||
				!maps.Equal(after.Secrets, before.Secrets) || !after.Updated.Equal(before.Updated) {
				t.Fatal("denied patch changed proxy configuration", rec.Code)
			}
		})
	}
	for range 2 {
		rec := w.do(http.MethodPatch, configPath, "admin", clear, "Content-Type", "application/yaml")
		if rec.Code != http.StatusOK || rec.Body.String() != "3 changes to config merged successfully" {
			t.Fatal("proxy password clear changed PATCH's value-count answer", rec.Code)
		}
		after := proxyDocument(t, w.db)
		if len(after.Secrets) != 0 || strings.Contains(rec.Body.String(), "private") {
			t.Fatal("proxy clear retained or disclosed credentials")
		}
		if len(before.Secrets) == 0 && !after.Updated.Equal(before.Updated) {
			t.Fatal("repeated public patch rewrote the proxy timestamp")
		}
		before = after
	}
}

func TestAccProxyServicesDescriptorSQL(t *testing.T) {
	keys := storekit.Get[*crypt.Keyring](t)(crypt.New(bytes.Repeat([]byte{9}, 32)))
	sqltest.RunModule(t, appsql.Modules(), func(base *dialect.Store) *appsql.Store {
		return appsql.New(base.DB(), base.Dialect(), keys)
	}, func(t *testing.T, newStore func(*testing.T) *appsql.Store) {
		for _, tc := range []struct {
			name, services       string
			explicit, historical bool
		}{
			{name: "provider explicit", services: `"jfrt,jfxr"`, explicit: true},
			{name: "provider default", services: `"jfrt,jfxr"`},
			{name: "empty default", services: `""`},
			{name: "source separators", services: `"jfxr; jfrt\njfxr"`},
			{name: "historical list", services: `["jfrt","jfxr"]`, historical: true},
			{name: "historical null", services: `null`, historical: true},
			{name: "historical absent", historical: true},
		} {
			for _, secure := range []bool{false, true} {
				t.Run(fmt.Sprintf("%s/https=%t", tc.name, secure), func(t *testing.T) {
					proxyServicesRetrieval(t, newStore(t), keys, tc.services, tc.explicit, tc.historical, secure)
				})
			}
		}
	}, sqltest.WithPostgres(), sqltest.WithKeyring(keys))
}

func proxyServicesRetrieval(
	t *testing.T, db *appsql.Store, keys *crypt.Keyring, services string,
	explicit, historical, secure bool,
) {
	t.Helper()
	originOptions := fakeupstream.Options{Auth: fakeupstream.Auth{Username: "origin-user", Password: "origin-private"}}
	if secure {
		originOptions.TLS = &tls.Config{MinVersion: tls.VersionTLS12}
	}
	origin := fakeupstream.New(t, originOptions)
	origin.Set("/item", fakeupstream.Resource{Body: []byte("descriptor-proxied-content")})
	proxy := fakeupstream.NewProxy(t, fakeupstream.Auth{Username: "proxy-user", Password: "proxy-private"}, origin)
	address := storekit.Get[*url.URL](t)(url.Parse(proxy.URL))
	api := configurationAPI(t, db)
	serviceValue := services
	if historical {
		serviceValue = `"jfrt,jfxr"`
	}
	selection := ""
	if explicit {
		selection = "    proxy: corp\n"
	}
	patch := fmt.Sprintf(`proxies:
  corp:
    host: %s
    port: %s
    username: proxy-user
    password: proxy-private
    platformDefault: true
    services: %s
remoteRepositories:
  proxy-proof:
    type: generic
    url: %s
    username: origin-user
    password: origin-private
%s`, address.Hostname(), address.Port(), serviceValue, origin.URL, selection)
	applyConfiguration(t, api, t.Context(), patch)
	before := proxyDocument(t, db)
	if historical {
		var document map[string]any
		storekit.Must(t, json.Unmarshal(before.Document, &document))
		delete(document, "services")
		if services != "" {
			var value any
			storekit.Must(t, json.Unmarshal([]byte(services), &value))
			document["services"] = value
		}
		before.Document = storekit.Get[[]byte](t)(json.Marshal(document))
		storekit.Must(t, db.Configs().Put(t.Context(), before))
	}
	snapshot := proxySQLSnapshot(db, keys)
	sealed := snapshot(t, before)
	resolverOptions := upstream.ResolverOptions{}
	if secure {
		resolverOptions.RootCAsPEM = pem.EncodeToMemory(&pem.Block{Type: "CERTIFICATE", Bytes: origin.Certificate().Raw})
	}
	resolver := storekit.Get[*upstream.Resolver](t)(upstream.NewResolver(db, keys, resolverOptions))
	configuration := storekit.Get[repo.Config](t)(db.Repos().Get(t.Context(), "proxy-proof"))
	pool := &upstream.Pool{}
	t.Cleanup(pool.Close)
	resolve := func() (remote.Options, *upstream.Client) {
		t.Helper()
		var options remote.Options
		storekit.Must(t, db.Tx(t.Context(), func(ctx context.Context) error {
			var err error
			options, err = resolver.Options(ctx, repo.Masked(configuration))
			return err
		}))
		return options, storekit.Get[*upstream.Client](t)(pool.Client(repo.Masked(configuration), options))
	}
	options, client := resolve()
	if proxy.Count("", "") != 0 || origin.Count("", "") != 0 {
		t.Fatal("configuration resolution performed network I/O")
	}
	response := storekit.Get[*http.Response](t)(client.Do(t.Context(), remote.Request{
		Method: http.MethodGet, Path: "item", Header: http.Header{
			"Authorization": {"Bearer caller-private"}, "Proxy-Authorization": {"caller-private"}, "Cookie": {"caller=private"},
		},
	}))
	content := storekit.Get[[]byte](t)(io.ReadAll(response.Body))
	storekit.Must(t, response.Body.Close())
	if response.StatusCode != http.StatusOK || string(content) != "descriptor-proxied-content" {
		t.Fatal("descriptor proxy did not retrieve origin content", response.StatusCode)
	}
	requests, origins := proxy.Requests(), origin.Requests()
	if len(requests) != 1 || len(origins) != 1 || !requests[0].Authorized || !origins[0].Authorized ||
		origins[0].Header.Get("Proxy-Authorization") != "" || origins[0].Header.Get("Cookie") != "" {
		t.Fatal("proxy and repository credential separation failed")
	}
	proxyAuth := "Basic " + base64.StdEncoding.EncodeToString([]byte("proxy-user:proxy-private"))
	if !requests[0].HeaderMatches("Proxy-Authorization", proxyAuth) {
		t.Fatal("proxy did not receive its configured credential")
	}
	wantMethod := http.MethodGet
	if secure {
		wantMethod = http.MethodConnect
	}
	if requests[0].Method != wantMethod || secure && requests[0].Header.Get("Authorization") != "" {
		t.Fatal("proxy routing or CONNECT credential boundary failed")
	}
	after := proxyDocument(t, db)
	if !bytes.Equal(before.Document, after.Document) || !before.Updated.Equal(after.Updated) ||
		!bytes.Equal(sealed, snapshot(t, after)) {
		t.Fatal("proxy read rewrote public configuration or sealed credentials")
	}
	// Both a public-document update and a sealed-secret update must replace the
	// pooled client. A revoked proxy credential must fail before origin retrieval.
	applyConfiguration(t, api, t.Context(), "proxies:\n  corp:\n    services: 'jfrt'\n")
	changed, replacement := resolve()
	if changed.Revision == options.Revision || replacement == client {
		t.Fatal("proxy configuration retained stale source/client state")
	}
	applyConfiguration(t, api, t.Context(), "proxies:\n  corp:\n    password: revoked-proxy-private\n")
	rotated, revoked := resolve()
	if rotated.Revision == changed.Revision || revoked == replacement || strings.Contains(rotated.Revision, "private") {
		t.Fatal("proxy secret retained stale source/client state or disclosed credentials")
	}
	denied, err := revoked.Do(t.Context(), remote.Request{Method: http.MethodGet, Path: "item"})
	if secure {
		if !errors.Is(err, remote.ErrRequest) || strings.Contains(err.Error(), "private") {
			t.Fatal("CONNECT denial lost its redacted request failure", err)
		}
	} else {
		storekit.Must(t, err)
		storekit.Must(t, denied.Body.Close())
		if denied.StatusCode != http.StatusProxyAuthRequired {
			t.Fatal("forward proxy did not deny its revoked credential", denied.StatusCode)
		}
	}
	requests = proxy.Requests()
	if len(requests) != 2 || requests[1].Authorized || origin.Count("", "") != 1 {
		t.Fatal("revoked proxy credentials reached origin or leaked", err)
	}
}
