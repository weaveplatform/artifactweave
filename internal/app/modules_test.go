package app

import (
	"context"
	"io"
	"log/slog"
	"strings"
	"testing"

	"github.com/weaveplatform/artifactweave/internal/access/core/token"
	"github.com/weaveplatform/artifactweave/internal/app/adapters/inmem"
	"github.com/weaveplatform/artifactweave/internal/app/config"
	"github.com/weaveplatform/artifactweave/internal/artifacts/api/repo"
	fedmodel "github.com/weaveplatform/artifactweave/internal/federation/core/model"
	"github.com/weaveplatform/artifactweave/internal/kernel"
	"github.com/weaveplatform/artifactweave/internal/platform/instance"
	workersmodel "github.com/weaveplatform/artifactweave/internal/workers/core/model"
)

// buildModules over a store whose every call panics, because its embedded
// moduleStore is nil, succeeds: no constructor reads or writes anything, so
// building the modules after the work queue moves none of OpenRuntime's I/O.
// Without a store, the first constructor's refusal names its module.
func TestUnitBuildModulesDoesNoIO(t *testing.T) {
	mods, err := buildModules(config.Default(), moduleDeps{store: struct{ moduleStore }{}})
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

// The workers model keeps its own copy of federation's include and exclude
// glob rule, because neither module may import the other's model (decision
// 0044, dependency rule 6). Only the composition root sees both, so it holds
// the copy to the same answers.
func TestUnitWorkerFilterGlobsMatchFederations(t *testing.T) {
	for _, c := range []struct {
		include, exclude []string
		name             string
	}{
		{nil, nil, "a.jar"},
		{[]string{"*.jar"}, nil, "a.jar"},
		{[]string{"*.jar"}, nil, "a.tmp"},
		{nil, []string{"*.tmp"}, "a.tmp"},
		{[]string{"team-*"}, []string{"team-secret"}, "team-a"},
		{[]string{"team-*"}, []string{"team-secret"}, "team-secret"},
		{[]string{"a/*"}, nil, "a/b"},
		{[]string{"a/*"}, nil, "a/b/c"},
		// A malformed glob matches nothing.
		{[]string{"["}, nil, "a"},
		{nil, []string{"["}, "a"},
	} {
		federation := fedmodel.PatternFilter{Include: c.include, Exclude: c.exclude}.Matches(c.name)
		worker := workersmodel.WorkerFilter{
			AnyLocal: true, IncludePatterns: c.include, ExcludePatterns: c.exclude,
		}.Matches("libs", repo.Local, c.name)
		if worker != federation {
			t.Errorf("%+v: workers %t, federation %t", c, worker, federation)
		}
	}
}

// TestAccRuntimeTokens: the first start creates the instance identity and the
// signing key; a restart keeps both, so a token issued before it still
// verifies.
func TestAccRuntimeTokens(t *testing.T) {
	ctx := context.Background()
	cfg := testConfig(t)
	log := slog.New(slog.NewTextHandler(io.Discard, nil))
	rt, err := OpenRuntime(ctx, cfg, log, RuntimeOptions{})
	if err != nil {
		t.Fatal(err)
	}
	id, err := rt.Store.Instance().Get(ctx)
	if err != nil {
		t.Fatal(err)
	}
	if instance.ValidateInstanceID(id.InstanceID) != nil || rt.Tokens.Issuer() == "" {
		t.Fatalf("identity %q, issuer %q", id.InstanceID, rt.Tokens.Issuer())
	}
	issued, err := rt.Tokens.Issue(ctx, token.Request{Username: "admin"})
	if err != nil {
		t.Fatal(err)
	}
	if err := rt.Close(ctx); err != nil {
		t.Fatal(err)
	}
	rt, err = OpenRuntime(ctx, cfg, log, RuntimeOptions{})
	if err != nil {
		t.Fatal(err)
	}
	defer func() { _ = rt.Close(ctx) }()
	again, err := rt.Store.Instance().Get(ctx)
	if err != nil || again.InstanceID != id.InstanceID {
		t.Fatalf("identity changed: %q, %v", again.InstanceID, err)
	}
	keys, err := rt.Store.Keys().SigningKeys(ctx)
	if err != nil || len(keys) != 1 {
		t.Fatalf("signing keys %d, %v", len(keys), err)
	}
	if _, err := rt.Tokens.Verify(ctx, issued.AccessToken); err != nil {
		t.Fatalf("a token issued before the restart: %v", err)
	}
}

// instanceOf replaces a store's instance identity store.
type instanceOf struct {
	*inmem.Store
	inst instance.InstanceStore
}

func (s instanceOf) Instance() instance.InstanceStore { return s.inst }

type fakeInstance struct {
	getErr, initErr error
}

func (f fakeInstance) Init(context.Context, instance.InstanceIdentity) error { return f.initErr }

func (f fakeInstance) Get(context.Context) (instance.InstanceIdentity, error) {
	return instance.InstanceIdentity{}, f.getErr
}

func (f fakeInstance) Update(context.Context, instance.InstanceIdentity) error { return nil }

func TestAccOpenTokensWithoutIdentity(t *testing.T) {
	ctx := context.Background()
	// The identity cannot be read back: the token service refuses to start.
	if _, err := openTokens(
		ctx,
		instanceOf{inmem.New(), fakeInstance{getErr: kernel.ErrNotFound}},
		tokenKeyBits,
	); err == nil {
		t.Fatal("tokens without an identity")
	}
}
