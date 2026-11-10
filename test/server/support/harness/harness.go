package harness

import (
	"net/http"
	"testing"

	"github.com/weaveplatform/artifactweave/internal/server"
	"github.com/weaveplatform/artifactweave/migration/platform/testkit/repopath"
)

// Config is the configuration the production router is built from: the
// stores, authentication, base URL, compatibility version and every option a
// suite sets. It is an alias, not a copy, so a suite's composite literals and
// Tune functions name the type the router takes; WEB-22 points it at
// app.RouterConfig.
type Config = server.Config

// Tune adjusts a Config before the router is built from it, for a suite that
// needs another blob store, a logger it can read or an option turned on. It is
// an alias, so any func(*Config) is a Tune.
type Tune = func(*Config)

// Mirrors is the type of Config.Mirrors, the blob mirrors whose state the
// storage summary reports and whose repair an administrator starts. A suite
// implements it to choose what the router sees.
type Mirrors = server.Mirrors

// MirrorStatus is one tier's mirror in what Mirrors reports.
type MirrorStatus = server.MirrorStatus

// SearchLimits is the type of Config.Search, the result caps of the GET
// searches.
type SearchLimits = server.SearchLimits

// DefaultRedirectTTL is how long a signed download URL stays valid when
// Config.Redirect sets no TTL.
const DefaultRedirectTTL = server.DefaultRedirectTTL

// Router returns the production router built from c, with every protocol
// route, authentication and the middleware the server runs. Suites build the
// router only through Router, so WEB-22 re-points this one call at
// app.NewRouter.
func Router(c Config) http.Handler { return server.New(c) }

// RepoFile returns the absolute path of rel, a slash-separated path relative to
// the repository root such as "api/access.openapi.yaml", and fails tb when rel
// is empty, absolute or has "." or ".." elements. It is repopath.Path rather
// than a search of its own for go.mod, so every test finds the root one way.
func RepoFile(tb testing.TB, rel string) string {
	tb.Helper()
	return repopath.Path(tb, rel)
}
