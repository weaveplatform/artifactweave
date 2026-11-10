package app

import (
	"context"
	"io"
	"log/slog"
	"strings"
	"testing"

	"github.com/weaveplatform/artifactweave/migration/access/core/token"
	"github.com/weaveplatform/artifactweave/migration/app/adapters/inmem"
	"github.com/weaveplatform/artifactweave/migration/app/config"
	"github.com/weaveplatform/artifactweave/pkg/store"
	"github.com/weaveplatform/artifactweave/pkg/types"
)

// buildModules over a store whose every call panics, because its embedded
// store.Store is nil, succeeds: no constructor reads or writes anything, so
// building the modules after the work queue moves none of OpenRuntime's I/O.
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
	if types.ValidateInstanceID(id.InstanceID) != nil || rt.Tokens.Issuer() == "" {
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
	inst store.InstanceStore
}

func (s instanceOf) Instance() store.InstanceStore { return s.inst }

type fakeInstance struct {
	getErr, initErr error
}

func (f fakeInstance) Init(context.Context, types.InstanceIdentity) error { return f.initErr }

func (f fakeInstance) Get(context.Context) (types.InstanceIdentity, error) {
	return types.InstanceIdentity{}, f.getErr
}

func (f fakeInstance) Update(context.Context, types.InstanceIdentity) error { return nil }

func TestAccOpenTokensWithoutIdentity(t *testing.T) {
	ctx := context.Background()
	// The identity cannot be read back: the token service refuses to start.
	if _, err := openTokens(
		ctx,
		instanceOf{inmem.New(), fakeInstance{getErr: store.ErrNotFound}},
		tokenKeyBits,
	); err == nil {
		t.Fatal("tokens without an identity")
	}
}
