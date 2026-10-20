package nuget

import (
	"context"
	"crypto/rand"
	"errors"
	"fmt"
	"strings"
	"time"

	"github.com/weaveplatform/artifactweave/internal/format"
	"github.com/weaveplatform/artifactweave/pkg/repo"
	"github.com/weaveplatform/artifactweave/pkg/types"
)

var errWriteLock = errors.New("nuget: transaction lock is unexpectedly leased")

// GuardWrite serializes all admitted writes of a version, including REST,
// checksum deploys, copies and moves, before rechecking its duplicate rule.
// PostgreSQL's READ COMMITTED transactions can otherwise both observe an
// absent version and insert it at different paths.
func (Handler) GuardWrite(
	ctx context.Context,
	env format.Env,
	c repo.Config,
	w format.Write,
) error {
	in, ok := env.Inspection.(*inspection)
	if w.Cached || w.Op == format.OpChecksum || kindOf(w.Path) == other || !ok || in.nuspec == nil {
		return nil
	}
	key := keyOf(in.nuspec)
	name := strings.Join([]string{"nuget.write", c.Key, key.pkg, key.version}, "/")
	holder := rand.Text()
	now := time.Now()
	// This is a transaction lock, not a timed lease. Acquire's INSERT/UPDATE
	// and Release's DELETE join the item transaction: PostgreSQL holds their
	// row/unique-index locks until commit or rollback, even after deletion.
	// An already-expired timestamp ensures no wall-clock expiry or node clock
	// skew can change exclusivity, and no lease survives a successful write.
	took, err := env.Store.Leases().Acquire(ctx, types.Lease{
		Name: name, Holder: holder, Task: "nuget write", Acquired: now,
		Expires: time.Unix(1, 0),
	}, now)
	if err != nil {
		return fmt.Errorf("nuget: lock %s: %w", name, err)
	}
	if !took {
		return fmt.Errorf(
			"nuget: lock %s is held outside a write transaction: %w",
			name,
			errWriteLock,
		)
	}
	if err := env.Store.Leases().Release(ctx, name, holder); err != nil {
		return fmt.Errorf("nuget: release %s: %w", name, err)
	}
	return checkPackage(ctx, env, c, w)
}
