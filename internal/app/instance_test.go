package app

import (
	"context"
	"errors"
	"io"
	"log/slog"
	"testing"

	"github.com/weaveplatform/artifactweave/internal/auth/token"
	"github.com/weaveplatform/artifactweave/pkg/store"
	"github.com/weaveplatform/artifactweave/pkg/store/inmem"
	"github.com/weaveplatform/artifactweave/pkg/types"
)

// TestAccRuntimeTokens: the first start creates the instance identity and the
// signing key; a restart keeps both, so a token issued before it still
// verifies.
func TestAccRuntimeTokens(t *testing.T) {
	ctx := context.Background()
	cfg := testConfig(t)
	log := slog.New(slog.NewTextHandler(io.Discard, nil))
	rt, err := openRuntime(ctx, cfg, log)
	if err != nil {
		t.Fatal(err)
	}
	id, err := rt.store.Instance().Get(ctx)
	if err != nil {
		t.Fatal(err)
	}
	if types.ValidateInstanceID(id.InstanceID) != nil || rt.tokens.Issuer() == "" {
		t.Fatalf("identity %q, issuer %q", id.InstanceID, rt.tokens.Issuer())
	}
	issued, err := rt.tokens.Issue(ctx, token.Request{Username: "admin"})
	if err != nil {
		t.Fatal(err)
	}
	if err := rt.close(ctx); err != nil {
		t.Fatal(err)
	}
	rt, err = openRuntime(ctx, cfg, log)
	if err != nil {
		t.Fatal(err)
	}
	defer func() { _ = rt.close(ctx) }()
	again, err := rt.store.Instance().Get(ctx)
	if err != nil || again.InstanceID != id.InstanceID {
		t.Fatalf("identity changed: %q, %v", again.InstanceID, err)
	}
	keys, err := rt.store.Keys().SigningKeys(ctx)
	if err != nil || len(keys) != 1 {
		t.Fatalf("signing keys %d, %v", len(keys), err)
	}
	if _, err := rt.tokens.Verify(ctx, issued.AccessToken); err != nil {
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

func TestAccEnsureInstance(t *testing.T) {
	ctx := context.Background()
	boom := errors.New("boom")
	for name, tc := range map[string]struct {
		inst fakeInstance
		ok   bool
	}{
		"read fails":  {fakeInstance{getErr: boom}, false},
		"init fails":  {fakeInstance{getErr: store.ErrNotFound, initErr: boom}, false},
		"lost a race": {fakeInstance{getErr: store.ErrNotFound, initErr: store.ErrConflict}, true},
	} {
		err := ensureInstance(ctx, instanceOf{inmem.New(), tc.inst})
		if (err == nil) != tc.ok {
			t.Errorf("%s: %v", name, err)
		}
	}
	// The identity cannot be read back: the token service refuses to start.
	if _, err := openTokens(
		ctx,
		instanceOf{inmem.New(), fakeInstance{getErr: store.ErrNotFound}},
	); err == nil {
		t.Fatal("tokens without an identity")
	}
}
