package app

import (
	"strings"
	"testing"

	"github.com/weaveplatform/artifactweave/migration/app/config"
	"github.com/weaveplatform/artifactweave/pkg/store"
)

// buildModules over a store whose every call panics, because its embedded
// store.Store is nil, succeeds: no constructor reads or writes anything, so
// building the modules after the work queue moves none of openRuntime's I/O.
// Without a store, the first constructor's refusal names its module.
func TestUnitBuildModulesDoesNoIO(t *testing.T) {
	mods, err := buildModules(config.Default(), moduleDeps{store: struct{ store.Store }{}})
	if err != nil {
		t.Fatal(err)
	}
	if mods.access == nil || mods.artifacts == nil || mods.configuration == nil {
		t.Fatalf("modules = %+v, want all three", mods)
	}
	if _, err := buildModules(config.Default(), moduleDeps{}); err == nil ||
		!strings.HasPrefix(err.Error(), "access: ") {
		t.Fatalf("buildModules without a store = %v, want the access module's refusal", err)
	}
}
