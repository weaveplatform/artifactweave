package agentpluginshttp

import (
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"slices"
	"strings"
	"testing"

	accessapi "github.com/weaveplatform/artifactweave/internal/access/api"
	"github.com/weaveplatform/artifactweave/internal/access/api/accessapitest"
	"github.com/weaveplatform/artifactweave/internal/artifacts/adapters/http/itemkit"
	"github.com/weaveplatform/artifactweave/internal/artifacts/adapters/http/itemkit/kittest"
	"github.com/weaveplatform/artifactweave/internal/artifacts/api/repo"
	"github.com/weaveplatform/artifactweave/internal/kernel"
)

// listed fails the test unless rec is a marketplace file of plugins-local,
// served as JSON that no shared cache may keep, and returns its plugins as
// name@version, in order.
func listed(t *testing.T, rec *httptest.ResponseRecorder) []string {
	t.Helper()
	if rec.Code != http.StatusOK {
		t.Fatalf("answered %d %s, want 200", rec.Code, rec.Body)
	}
	if got := rec.Header().Get("Content-Type"); got != "application/json" {
		t.Errorf("Content-Type %q, want application/json", got)
	}
	// A marketplace file is rendered for its caller, so a shared cache that
	// kept one would hand it to callers who may not read what it lists.
	if got := rec.Header().Get("Cache-Control"); got != "private, no-store" {
		t.Errorf("Cache-Control %q, want private, no-store", got)
	}
	var doc struct {
		Name  string `json:"name"`
		Owner struct {
			Name string `json:"name"`
		} `json:"owner"`
		Plugins []struct {
			Name    string `json:"name"`
			Version string `json:"version"`
		} `json:"plugins"`
	}
	if err := json.Unmarshal(rec.Body.Bytes(), &doc); err != nil {
		t.Fatalf("marketplace %s: %v", rec.Body, err)
	}
	if doc.Name != pluginsLocal || doc.Owner.Name != pluginsLocal {
		t.Errorf("marketplace named %q, owned by %q, want %s for both",
			doc.Name, doc.Owner.Name, pluginsLocal)
	}
	out := make([]string, 0, len(doc.Plugins))
	for _, p := range doc.Plugins {
		out = append(out, p.Name+"@"+p.Version)
	}
	return out
}

// TestAccAgentPluginsMarketplaceListsWhatTheCallerMayRead: a marketplace
// file is rendered for each request, the same at its API path and at the
// repository root. It lists each plugin's latest version that the caller
// may read, of the harness the file names. A version whose ZIP the
// repository's patterns reject stays listed, as 7.x's stored marketplace
// lists each plugin's latest version, rather than giving way to an older
// one; only the ZIP's download refuses.
func TestAccAgentPluginsMarketplaceListsWhatTheCallerMayRead(t *testing.T) {
	w := newItemWorld(t)
	local := pluginsRepo(pluginsLocal, repo.Local)
	local.ExcludesPattern = new("secret/**,alpha/2.0.0/**")
	w.repos(local)
	w.plugin("alpha", "1.0.0", "claude")
	w.plugin("alpha", "2.0.0", "claude")
	w.plugin("beta", "1.0.0", "cursor")
	w.plugin("hidden", "1.0.0", "claude")
	w.plugin("secret", "1.0.0", "claude")
	// reader may read the repository and every bundle in it but alpha's
	// newest and hidden's.
	reader := &caller{kernel.Principal{Name: "reader"}, &accessapitest.Decider{
		AllowsFolderFunc: func(
			accessapi.ResourceType, accessapi.Repo, string, accessapi.Actions,
		) bool {
			return true
		},
		AllowsFunc: func(
			_ accessapi.ResourceType, _ accessapi.Repo, p string, _ accessapi.Actions,
		) bool {
			return !strings.HasPrefix(p, "alpha/2.0.0/") && !strings.HasPrefix(p, "hidden/")
		},
	}}
	for _, tc := range []struct {
		file string
		as   *caller
		want []string
	}{
		{
			"marketplace.json", admin,
			[]string{"alpha@2.0.0", "beta@1.0.0", "hidden@1.0.0", "secret@1.0.0"},
		},
		{"claude-marketplace.json", admin, []string{"alpha@2.0.0", "hidden@1.0.0", "secret@1.0.0"}},
		{"cursor-marketplace.json", admin, []string{"beta@1.0.0"}},
		{"codex-marketplace.json", admin, []string{}},
		{"marketplace.json", reader, []string{"alpha@1.0.0", "beta@1.0.0", "secret@1.0.0"}},
		{"claude-marketplace.json", reader, []string{"alpha@1.0.0", "secret@1.0.0"}},
	} {
		for _, target := range []string{
			apiPath(pluginsLocal, tc.file), itemPath(pluginsLocal, tc.file),
		} {
			t.Run(tc.as.p.Name+" "+target, func(t *testing.T) {
				got := listed(t, w.do(http.MethodGet, target, tc.as))
				if !slices.Equal(got, tc.want) {
					t.Errorf("lists %v, want %v", got, tc.want)
				}
			})
		}
	}
	wantError(t, w.do(http.MethodGet, itemPath(pluginsLocal, "alpha/2.0.0/alpha-2.0.0.zip"), admin),
		http.StatusNotFound, "The repository 'plugins-local' rejected the artifact "+
			"'plugins-local:alpha/2.0.0/alpha-2.0.0.zip' due to its include/exclude pattern settings.")
}

// TestAccAgentPluginsMarketplaceHeadAnswersTheHeadersAlone: HEAD shares the
// GET handler at both paths, so it answers the file's status and headers
// and writes no body.
func TestAccAgentPluginsMarketplaceHeadAnswersTheHeadersAlone(t *testing.T) {
	w := newItemWorld(t)
	w.repos(pluginsRepo(pluginsLocal, repo.Local))
	w.plugin("alpha", "1.0.0", "claude")
	for _, target := range []string{
		apiPath(pluginsLocal, "marketplace.json"), itemPath(pluginsLocal, "marketplace.json"),
	} {
		t.Run(target, func(t *testing.T) {
			rec := w.do(http.MethodHead, target, admin)
			if rec.Code != http.StatusOK || rec.Body.Len() != 0 ||
				rec.Header().Get("Content-Type") != "application/json" ||
				rec.Header().Get("Cache-Control") != "private, no-store" {
				t.Errorf("answered %d %v with %d bytes, want 200 and the JSON headers alone",
					rec.Code, rec.Header(), rec.Body.Len())
			}
		})
	}
}

// TestAccAgentPluginsMarketplaceAPIServesOnlyALocalRepositorysFiles: only a
// local Agent Plugins repository has marketplace files, and only the names
// the target version renders, so the API path answers 404 for anything
// else; a key that names no repository is answered as the item routes
// answer one.
func TestAccAgentPluginsMarketplaceAPIServesOnlyALocalRepositorysFiles(t *testing.T) {
	w := newItemWorld(t)
	w.repos(pluginsRepo(pluginsLocal, repo.Local), pluginsRepo(pluginsRemote, repo.Remote))
	for _, tc := range []struct {
		name, key, file, message string
	}{
		{"generic repository", kittest.LocalKey, "marketplace.json", "Could not find resource"},
		{"remote repository", pluginsRemote, "marketplace.json", "Could not find resource"},
		{
			"remote repository's cache", pluginsRemote + "-cache", "marketplace.json",
			"Could not find resource",
		},
		{"name of no marketplace file", pluginsLocal, "plugins.json", "Could not find resource"},
		{
			"reserved name the target version does not render", pluginsLocal,
			"vscode-marketplace.json", "Could not find resource",
		},
		{
			"absent repository", "absent", "marketplace.json",
			"Repository 'absent' does not exist",
		},
		{
			"local repository's cache", pluginsLocal + "-cache", "marketplace.json",
			"Repository 'plugins-local-cache' does not exist",
		},
	} {
		t.Run(tc.name, func(t *testing.T) {
			rec := w.do(http.MethodGet, apiPath(tc.key, tc.file), admin)
			wantError(t, rec, http.StatusNotFound, tc.message)
		})
	}
}

// TestAccAgentPluginsMarketplaceOverlayLeavesOtherRequestsToTheLookup: at
// the item path the overlay renders a marketplace file only for a request
// without properties for a reserved name at the root of a local Agent
// Plugins repository, and answers 404 for the reserved name it does not
// render. Every other request reaches the generic lookup, which serves the
// file stored at the path where the overlay would have rendered one.
func TestAccAgentPluginsMarketplaceOverlayLeavesOtherRequestsToTheLookup(t *testing.T) {
	w := newItemWorld(t)
	w.repos(pluginsRepo(pluginsLocal, repo.Local), pluginsRepo(pluginsRemote, repo.Remote))
	// The format refuses uploads at the reserved names; these are stored
	// past it, so that what the generic lookup serves tells it apart.
	w.put(kittest.LocalKey, false, "marketplace.json", `{"stored":"generic"}`)
	w.put(pluginsLocal, false, "marketplace.json", `{"stored":"root"}`)
	w.put(pluginsLocal, false, "vscode-marketplace.json", `{"stored":"vscode"}`)
	w.put(pluginsLocal, false, "docs/marketplace.json", `{"stored":"docs"}`)
	w.put(pluginsLocal, false, "plugins.json", `{"stored":"plugins"}`)
	w.put(pluginsRemote, true, "marketplace.json", `{"stored":"cache"}`)
	for _, tc := range []struct {
		name, target, stored string
	}{
		{
			"generic repository", itemPath(kittest.LocalKey, "marketplace.json"),
			`{"stored":"generic"}`,
		},
		{"below the root", itemPath(pluginsLocal, "docs/marketplace.json"), `{"stored":"docs"}`},
		{
			"name of no marketplace file", itemPath(pluginsLocal, "plugins.json"),
			`{"stored":"plugins"}`,
		},
		{
			"request with properties", itemPath(pluginsLocal, "marketplace.json;team=a"),
			`{"stored":"root"}`,
		},
		{
			"request ending in a slash", itemPath(pluginsLocal, "marketplace.json/"),
			`{"stored":"root"}`,
		},
		{"remote repository", itemPath(pluginsRemote, "marketplace.json"), `{"stored":"cache"}`},
		{
			"remote repository's cache", itemPath(pluginsRemote+"-cache", "marketplace.json"),
			`{"stored":"cache"}`,
		},
	} {
		t.Run(tc.name, func(t *testing.T) {
			rec := w.do(http.MethodGet, tc.target, admin)
			if rec.Code != http.StatusOK || rec.Body.String() != tc.stored {
				t.Errorf("answered %d %s, want 200 and the stored %s",
					rec.Code, rec.Body, tc.stored)
			}
		})
	}
	t.Run("marketplace file", func(t *testing.T) {
		got := listed(t, w.do(http.MethodGet, itemPath(pluginsLocal, "marketplace.json"), admin))
		if len(got) != 0 {
			t.Errorf("lists %v, want no plugins", got)
		}
	})
	t.Run("reserved name the target version does not render", func(t *testing.T) {
		rec := w.do(http.MethodGet, itemPath(pluginsLocal, "vscode-marketplace.json"), admin)
		wantError(t, rec, http.StatusNotFound, "Could not find resource")
	})
}

// TestUnitAgentPluginsOverlayDeclinesACacheAlias: only a remote repository
// has a cache, so a cache alias names no marketplace file, and the overlay
// leaves it to the generic lookup without writing anything. The item route
// refuses a local repository's alias before the overlays run, so only a
// direct call reaches this check.
func TestUnitAgentPluginsOverlayDeclinesACacheAlias(t *testing.T) {
	w := newItemWorld(t)
	local := w.repos(pluginsRepo(pluginsLocal, repo.Local))[0]
	w.plugin("alpha", "1.0.0", "claude")
	alias := pluginsLocal + "-cache"
	rec := httptest.NewRecorder()
	r := kittest.Request(t, http.MethodGet, itemPath(alias, "marketplace.json"))
	req := itemkit.ItemRequest{Key: alias, Path: "marketplace.json"}
	if TryAgentMarketplaceItem(w.kit, rec, r, w.world.Store, local, true, req,
		kittest.Admin, kittest.AdminAccess()) {
		t.Fatalf("the overlay answered a cache alias: %d %s", rec.Code, rec.Body)
	}
	if rec.Body.Len() != 0 || len(rec.Header()) != 0 {
		t.Errorf("the overlay wrote %v %s before declining", rec.Header(), rec.Body)
	}
}

// TestAccAgentPluginsMarketplaceRefusesBeforeRendering: a marketplace file
// is refused, at both paths, in a blacked-out repository and where the
// repository's patterns exclude it, before it is rendered. A caller who may
// not read the repository is refused first, and the anonymous user
// challenged, so neither learns how the repository is set (DV-057).
func TestAccAgentPluginsMarketplaceRefusesBeforeRendering(t *testing.T) {
	w := newItemWorld(t)
	dark := pluginsRepo("plugins-dark", repo.Local)
	dark.BlackedOut = new(true)
	closed := pluginsRepo("plugins-closed", repo.Local)
	closed.ExcludesPattern = new("*.json")
	w.repos(pluginsRepo(pluginsLocal, repo.Local), dark, closed)
	w.plugin("alpha", "1.0.0", "claude")
	const (
		blackedOut = "The repository 'plugins-dark' is blacked out and cannot serve artifact " +
			"'plugins-dark:marketplace.json'."
		excluded = "The repository 'plugins-closed' rejected the artifact " +
			"'plugins-closed:marketplace.json' due to its include/exclude pattern settings."
	)
	for _, tc := range []struct {
		name    string
		key     string
		as      *caller
		status  int
		message string
	}{
		{"blacked-out repository", "plugins-dark", admin, http.StatusNotFound, blackedOut},
		{
			"blacked-out repository, to a caller who may not read it", "plugins-dark", user,
			http.StatusForbidden, "Forbidden",
		},
		{
			"blacked-out repository, to an anonymous user who may not read it", "plugins-dark",
			anonymous, http.StatusUnauthorized, "Authentication is required",
		},
		{
			"excluded by the patterns, to a caller who may not read it", "plugins-closed", user,
			http.StatusForbidden, "Forbidden",
		},
		{"excluded by the patterns", "plugins-closed", admin, http.StatusNotFound, excluded},
		{
			"caller who may not read the repository", pluginsLocal, user,
			http.StatusForbidden, "Forbidden",
		},
		{
			"anonymous user who may not read the repository", pluginsLocal, anonymous,
			http.StatusUnauthorized, "Authentication is required",
		},
	} {
		for _, target := range []string{
			apiPath(tc.key, "marketplace.json"), itemPath(tc.key, "marketplace.json"),
		} {
			t.Run(tc.name+" at "+target, func(t *testing.T) {
				wantError(t, w.do(http.MethodGet, target, tc.as), tc.status, tc.message)
			})
		}
	}
}

// TestAccAgentPluginsMarketplaceFailsClosed: a server without a store
// answers the API path as an unimplemented operation, a request that
// carries no caller is challenged, and a caller whose permissions cannot
// be evaluated, a repository that cannot be looked up and a marketplace
// whose plugins cannot be listed answer 500, naming nothing internal and
// logging the step that failed.
func TestAccAgentPluginsMarketplaceFailsClosed(t *testing.T) {
	marketplace := apiPath(pluginsLocal, "marketplace.json")
	for _, tc := range []struct {
		name    string
		tune    func(*itemkit.Deps)
		target  string
		as      *caller
		status  int
		message string
		logged  string
	}{
		{
			name:   "no store",
			tune:   func(d *itemkit.Deps) { d.Store = nil },
			target: marketplace, as: admin,
			status: http.StatusNotImplemented, message: "not implemented by artifactweave yet",
		},
		{
			name:   "no caller",
			target: marketplace,
			status: http.StatusUnauthorized, message: "Authentication is required",
		},
		{
			name:   "permissions cannot be evaluated",
			tune:   func(d *itemkit.Deps) { d.Access.Authorizer = failingAuthorizer },
			target: marketplace, as: &caller{p: kernel.Principal{Name: "user"}},
			status: http.StatusInternalServerError, message: "internal error",
			logged: "item permissions",
		},
		{
			name:   "repository lookup fails",
			tune:   func(d *itemkit.Deps) { d.Store = repoLookupFails{d.Store} },
			target: marketplace, as: admin,
			status: http.StatusInternalServerError, message: "internal error",
			logged: "resolve repository",
		},
		{
			name:   "plugins cannot be listed",
			tune:   func(d *itemkit.Deps) { d.Store = packageListingFails{d.Store} },
			target: marketplace, as: admin,
			status: http.StatusInternalServerError, message: "internal error",
			logged: "agent plugin marketplace",
		},
		{
			name:   "plugins cannot be listed at the item path",
			tune:   func(d *itemkit.Deps) { d.Store = packageListingFails{d.Store} },
			target: itemPath(pluginsLocal, "marketplace.json"), as: admin,
			status: http.StatusInternalServerError, message: "internal error",
			logged: "agent plugin marketplace",
		},
	} {
		t.Run(tc.name, func(t *testing.T) {
			var tune []func(*itemkit.Deps)
			if tc.tune != nil {
				tune = append(tune, tc.tune)
			}
			w := newItemWorld(t, tune...)
			w.repos(pluginsRepo(pluginsLocal, repo.Local))
			w.plugin("alpha", "1.0.0", "claude")
			wantError(t, w.do(http.MethodGet, tc.target, tc.as), tc.status, tc.message)
			if tc.logged != "" {
				w.wantLogged(t, tc.logged)
			}
		})
	}
}
