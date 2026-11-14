package repo_test

import (
	"testing"

	"github.com/weaveplatform/artifactweave/internal/artifacts/api/repo"
)

// A Conan repository's full read says enableConanSupport is true whatever
// the client sent, as 7.x builds the flag from the package type (#1440). The
// v2 read and the configurations list, which take their shape from
// Relevant, leave it out for every package type, and another type's full
// read says false whatever was stored, as 7.x sets the flag for Conan
// repositories only (RepositoryConfigurationBase.setPackageTypeConfig,
// 7.161.26; RTFACT-31100's Maven read) (#1479).
func TestUnitEnableConanSupportIsTheConanTypes(t *testing.T) {
	for _, class := range []repo.Class{repo.Local, repo.Remote, repo.Virtual, repo.Federated} {
		for name, sent := range map[string]*bool{
			"unsent": nil, "false": ptr(false), "true": ptr(true),
		} {
			c := repo.Config{
				Key: "c", Class: class, PackageType: repo.Conan, EnableConanSupport: sent,
			}
			full := repo.Full(c)
			if full.EnableConanSupport == nil || !*full.EnableConanSupport {
				t.Errorf("%s, %s: full read %v, want true", class, name, full.EnableConanSupport)
			}
			if v2 := repo.Relevant(full); v2.EnableConanSupport != nil {
				t.Errorf("%s, %s: v2 read %v, want none", class, name, *v2.EnableConanSupport)
			}
		}
	}
	other := repo.Config{Key: "g", Class: repo.Local, PackageType: repo.Generic}
	other.EnableConanSupport = ptr(true)
	if got := repo.Full(other).EnableConanSupport; got == nil || *got {
		t.Errorf("generic full read %v, want false", got)
	}
	npm := repo.Config{Key: "n", PackageType: repo.NPM}
	if got := repo.Full(npm).EnableConanSupport; got == nil || *got {
		t.Errorf("npm full read %v, want false", got)
	}
}
