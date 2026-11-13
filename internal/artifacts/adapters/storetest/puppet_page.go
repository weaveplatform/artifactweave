package storetest

import (
	"testing"

	"github.com/weaveplatform/artifactweave/internal/artifacts/api/repo"
	"github.com/weaveplatform/artifactweave/internal/artifacts/core/model"
	"github.com/weaveplatform/artifactweave/internal/platform/testkit/storekit"
)

// RunVersionNames runs the contract of PackageVersionNames on a store from
// newStore: a package's version names come in exactly the order Versions
// returns its versions, whatever order they were written in, follow a
// version written or deleted later, and are a package's own. The adapters'
// tests call it beside Run.
func RunVersionNames(t *testing.T, newStore Factory) {
	t.Run("VersionNames", func(t *testing.T) {
		ctx := t.Context()
		s := newStore(t).Packages()
		ensure := func(name string) model.Package {
			t.Helper()
			return storekit.Get[model.Package](t)(s.Ensure(ctx, model.Package{
				RepoKey: "puppet-local", Type: repo.Puppet, Name: name,
			}))
		}
		pkg, other := ensure("acme-widget"), ensure("acme-other")
		put := func(p model.Package, version string) model.PackageVersion {
			t.Helper()
			return storekit.Get[model.PackageVersion](t)(s.PutVersion(ctx, model.PackageVersion{
				PackageID: p.ID, Version: version, Metadata: []byte(`{"name":"` + p.Name + `"}`),
			}))
		}
		names := storekit.Get[[]string](t)
		// "1.0" and "1.0.0" compare equal and share an order key, so the
		// version string orders them, as it orders Versions'.
		for _, v := range []string{"1.10.0", "1.0.0", "2.0.0-rc.1", "1.0", "1.2.0", "2.0.0"} {
			put(pkg, v)
		}
		put(other, "9.0.0")
		order := func() []string {
			vs := storekit.Get[[]model.PackageVersion](t)(s.Versions(ctx, pkg.ID))
			out := make([]string, 0, len(vs))
			for _, v := range vs {
				out = append(out, v.Version)
			}
			return out
		}
		want := []string{"1.0", "1.0.0", "1.2.0", "1.10.0", "2.0.0-rc.1", "2.0.0"}
		storekit.Same(t, "Versions' order", want, order())
		storekit.Same(t, "VersionNames", want, names(s.VersionNames(ctx, pkg.ID)))
		put(pkg, "0.9.0")
		storekit.Must(t, s.DeleteVersion(ctx, put(pkg, "1.5.0").ID))
		want = append([]string{"0.9.0"}, want...)
		storekit.Same(t, "VersionNames after writes", want, names(s.VersionNames(ctx, pkg.ID)))
		storekit.Same(t, "Versions' order after writes", want, order())
		storekit.Same(t, "another package's", []string{"9.0.0"},
			names(s.VersionNames(ctx, other.ID)))
		if got := names(s.VersionNames(ctx, other.ID+pkg.ID+1000)); len(got) != 0 {
			t.Fatalf("VersionNames of no package = %q", got)
		}
		storekit.Must(t, s.Delete(ctx, other.ID))
		if got := names(s.VersionNames(ctx, other.ID)); len(got) != 0 {
			t.Fatalf("VersionNames of a deleted package = %q", got)
		}
	})
}
