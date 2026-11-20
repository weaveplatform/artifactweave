package replication_test

import (
	"context"
	"errors"
	"testing"

	"github.com/weaveplatform/artifactweave/internal/federation/core/model"
	"github.com/weaveplatform/artifactweave/internal/federation/core/replication"
	"github.com/weaveplatform/artifactweave/internal/kernel"
)

func TestUnitReplicationEnablementAndGlobalAdmission(t *testing.T) {
	w, ctx := newWorld(), t.Context()
	must(t, w.service.Replace(ctx, "local", replication.Input{URL: ptr("https://target.example/repo")}))
	must(t, w.service.Replace(ctx, "remote", replication.Input{}))
	first := stored(t, w, "local")
	pull := stored(t, w, "remote")
	must(t, w.service.SetEnabled(ctx, false, []string{"https://target.*"}, []string{"https://upstream.*"}))
	disabled := stored(t, w, "local")
	if disabled.Enabled || disabled.Revision == first.Revision || stored(t, w, "remote") != pull {
		t.Fatal("bulk URL selection changed the wrong target or omitted its fence")
	}
	must(t, w.service.SetEnabled(ctx, false, []string{"https://target.*"}, nil))
	if stored(t, w, "local") != disabled {
		t.Fatal("identical disable retry rotated the fence")
	}
	must(t, w.service.SetEnabled(ctx, true, nil, []string{"https://upstream.*"}))
	enabled := stored(t, w, "local")
	if !enabled.Enabled || enabled.Revision == disabled.Revision {
		t.Fatal("re-enable did not fence the previous enabled lifetime")
	}
	called := false
	admit := func(context.Context, model.Replication) error { called = true; return nil }
	if err := w.service.WithTarget(ctx, first.Key, first.Revision, admit); !errors.Is(err, kernel.ErrConflict) || called {
		t.Fatal("old enabled revision regained authority")
	}
	must(t, w.service.SetBlocked(ctx, true, true, false))
	global, err := w.service.Global(ctx)
	must(t, err)
	if !global.Push || global.Pull {
		t.Fatal("global direction selection lost")
	}
	if err := w.service.WithTarget(ctx, enabled.Key, enabled.Revision, admit); !errors.Is(err, kernel.ErrConflict) || called {
		t.Fatal("globally blocked push admitted")
	}
	must(t, w.service.WithTarget(ctx, pull.Key, pull.Revision, admit))
	if !called {
		t.Fatal("unblocked pull refused")
	}
	must(t, w.service.SetBlocked(ctx, false, true, false))
	must(t, w.service.WithTarget(ctx, enabled.Key, enabled.Revision, admit))
	w.policy = authority{any: true, project: "alpha"}
	if _, err := w.service.Global(ctx); !errors.Is(err, kernel.ErrForbidden) {
		t.Fatalf("project admin global read = %v", err)
	}
	if err := w.service.SetEnabled(ctx, false, nil, nil); !errors.Is(err, kernel.ErrForbidden) {
		t.Fatalf("project admin global enablement = %v", err)
	}
	if err := w.service.SetBlocked(ctx, true, true, true); !errors.Is(err, kernel.ErrForbidden) {
		t.Fatalf("project admin global block = %v", err)
	}
}
