package item

import (
	"context"
	"errors"
	"fmt"
	"testing"

	"github.com/weaveplatform/artifactweave/internal/artifacts/api/repo"
	"github.com/weaveplatform/artifactweave/internal/artifacts/core/format"
	"github.com/weaveplatform/artifactweave/internal/artifacts/core/model"
	"github.com/weaveplatform/artifactweave/internal/artifacts/ecosystem/generic"
	"github.com/weaveplatform/artifactweave/internal/kernel"
)

// errStatement is a statement the database failed to run, as the SQL
// store's Wrap passes it up: the driver's error marked kernel.ErrStore.
var errStatement = fmt.Errorf("sqlite: ensure package: %w", errors.Join(kernel.ErrStore, errDown))

// failingHooks is a format whose hooks fail with err at the hook that
// at names: "admit", "deployed" or "annotated".
type failingHooks struct {
	generic.Handler
	at  string
	err error
}

func (h failingHooks) Admit(
	ctx context.Context,
	env format.Env,
	c repo.Config,
	w format.Write,
) (format.Write, error) {
	if h.at == "admit" {
		return w, h.err
	}
	return h.Handler.Admit(ctx, env, c, w)
}

func (h failingHooks) Deployed(context.Context, format.Env, repo.Config, model.Item) error {
	if h.at == "deployed" {
		return fmt.Errorf("hex: link package index: %w", h.err)
	}
	return nil
}

func (h failingHooks) Annotated(
	context.Context,
	format.Env,
	repo.Config,
	model.Item,
	format.Annotation,
) error {
	if h.at == "annotated" {
		return h.err
	}
	return nil
}

// A remote file whose publication meets a statement the store failed to
// run under a format's hook is a StorageFault, wherever the hook is, and
// the cache keeps nothing: the server's 500, not the upstream's 502. A
// hook's refusal of the content keeps its error (#1470).
func TestUnitCacheMarksAStoreFailureUnderAHook(t *testing.T) {
	for _, at := range []string{"admit", "deployed", "annotated"} {
		for name, tc := range map[string]struct {
			err   error
			cause error
		}{
			"store failure": {err: errStatement, cause: kernel.ErrStore},
			"refusal":       {err: errBadCopy},
		} {
			t.Run(at+" "+name, func(t *testing.T) {
				w, u := remoteWorld(t)
				// Annotated runs for the properties the publication sets.
				u.Properties = model.Properties{"origin": {"upstream"}}
				w.svc.Formats = func(repo.PackageType) format.Handler {
					return failingHooks{at: at, err: tc.err}
				}
				_, err := w.svc.Cache(t.Context(), u)
				if !errors.Is(err, tc.err) {
					t.Fatal(err)
				}
				wantMarked(t, err, tc.cause)
				w.wantNothingCached(u)
			})
		}
	}
}
