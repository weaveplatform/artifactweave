package instance_test

import (
	"context"
	"errors"
	"testing"

	"github.com/weaveplatform/artifactweave/internal/kernel"
	"github.com/weaveplatform/artifactweave/internal/platform/instance"
)

// instanceOf is the Store that EnsureInstance takes, over a fake identity
// store.
type instanceOf struct {
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

func TestAccEnsureInstance(t *testing.T) {
	ctx := context.Background()
	boom := errors.New("boom")
	for name, tc := range map[string]struct {
		inst fakeInstance
		ok   bool
	}{
		"read fails":  {fakeInstance{getErr: boom}, false},
		"init fails":  {fakeInstance{getErr: kernel.ErrNotFound, initErr: boom}, false},
		"lost a race": {fakeInstance{getErr: kernel.ErrNotFound, initErr: kernel.ErrConflict}, true},
	} {
		err := instance.EnsureInstance(ctx, instanceOf{tc.inst})
		if (err == nil) != tc.ok {
			t.Errorf("%s: %v", name, err)
		}
	}
}
