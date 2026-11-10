package accessapitest

import (
	"context"
	"fmt"
	"sync"

	accessapi "github.com/weaveplatform/artifactweave/internal/access/api"
	"github.com/weaveplatform/artifactweave/internal/kernel"
)

var _ accessapi.AnonymousAccess = (*AnonymousAccess)(nil)

// AnonymousAccess is an AnonymousAccess over one setting held in memory. The
// zero value is a setting written as off. It is safe for concurrent use;
// set its fields before sharing it, and read the setting back with Enabled.
type AnonymousAccess struct {
	// On is the setting.
	On bool
	// Unwritten makes Enabled answer as for a setting never written: an
	// error matching kernel.ErrNotFound, which callers read as off.
	// SetEnabled clears it.
	Unwritten bool
	// Err, when set, is every call's error, and SetEnabled then changes
	// nothing, as when the store cannot be read or written.
	Err error

	mu sync.Mutex
}

// Enabled returns On; Err, when set; or, while Unwritten, an error matching
// kernel.ErrNotFound.
func (a *AnonymousAccess) Enabled(context.Context) (bool, error) {
	a.mu.Lock()
	defer a.mu.Unlock()
	switch {
	case a.Err != nil:
		return false, a.Err
	case a.Unwritten:
		return false, fmt.Errorf(
			"accessapitest: anonymous access never written: %w",
			kernel.ErrNotFound,
		)
	default:
		return a.On, nil
	}
}

// SetEnabled sets On to on and clears Unwritten, or returns Err, when set,
// and changes nothing.
func (a *AnonymousAccess) SetEnabled(_ context.Context, on bool) error {
	a.mu.Lock()
	defer a.mu.Unlock()
	if a.Err != nil {
		return a.Err
	}
	a.On, a.Unwritten = on, false
	return nil
}
