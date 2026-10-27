package item

import (
	"context"
	"errors"
	"net/http"
	"strings"
	"testing"

	"github.com/weaveplatform/artifactweave/internal/format"
	"github.com/weaveplatform/artifactweave/pkg/repo"
	"github.com/weaveplatform/artifactweave/pkg/types"
)

type guardedRecorder struct {
	*recorder
	guard func(format.Write) error
}

func (r guardedRecorder) GuardWrite(
	ctx context.Context,
	env format.Env,
	_ repo.Config,
	wr format.Write,
) error {
	r.note(ctx, env, "guard "+wr.Path)
	return r.guard(wr)
}

func TestUnitWriteGuardRunsInsideDeployTransaction(t *testing.T) {
	w, r := recording(t)
	refusal := format.Rejectf(http.StatusConflict, "claimed by another writer")
	w.svc.Formats = func(repo.PackageType) format.Handler {
		return guardedRecorder{r, func(format.Write) error { return refusal }}
	}
	_, err := w.deploy("blocked.txt", "body", types.Checksums{})
	if !errors.Is(err, refusal) || !r.calls["guard blocked.txt"] {
		t.Fatalf("error %v, calls %v", err, r.calls)
	}
	if _, ok := w.get(w.loc, "blocked.txt"); ok {
		t.Fatal("refused deploy landed")
	}
}

func TestUnitTransferWriteGuardPreservesReportsAndSources(t *testing.T) {
	for _, mode := range []string{"copy", "move", "fail fast", "fault"} {
		t.Run(mode, func(t *testing.T) {
			w, r := recording(t)
			for _, p := range []string{"src/a.txt", "src/b.txt"} {
				if _, err := w.deploy(p, p, types.Checksums{}); err != nil {
					t.Fatal(err)
				}
			}
			r.reset()
			fault := errors.New("guard store unavailable")
			w.svc.Formats = func(repo.PackageType) format.Handler {
				return guardedRecorder{r, func(wr format.Write) error {
					if mode == "fault" {
						if strings.HasSuffix(wr.Path, "b.txt") {
							return fault
						}
						return nil
					}
					if strings.HasSuffix(wr.Path, "a.txt") {
						return format.Rejectf(http.StatusConflict, "claimed by another writer")
					}
					return nil
				}}
			}
			res, err := w.svc.Transfer(context.Background(), Transfer{
				From: Target{Repo: w.loc, Path: "src"}, To: Target{Repo: w.loc, Path: "dst"},
				Move: mode == "move" || mode == "fault", FailFast: mode == "fail fast",
			})
			if mode == "fault" {
				if !errors.Is(err, fault) {
					t.Fatal(err)
				}
				if _, ok := w.get(w.loc, "dst"); ok {
					t.Fatal("failed transaction retained destination")
				}
				for _, p := range []string{"src/a.txt", "src/b.txt"} {
					if _, ok := w.get(w.loc, p); !ok {
						t.Fatalf("rollback lost source %s", p)
					}
					if n := w.refs(sums(p).SHA256); n != 1 {
						t.Fatalf("rollback retained copied blob reference for %s: %d", p, n)
					}
				}
				return
			}
			files := 1
			if mode == "fail fast" {
				files = 0
			}
			if err != nil || res.Files != files || res.Failed != 1 ||
				!strings.Contains(res.Messages[0].Text, "claimed by another writer") {
				t.Fatalf("result %+v, error %v", res, err)
			}
			if !r.calls["guard dst/a.txt"] {
				t.Fatal("guard ran outside transaction")
			}
			if _, ok := w.get(w.loc, "src/a.txt"); !ok {
				t.Fatal("refused source removed")
			}
			if _, ok := w.get(w.loc, "dst/a.txt"); ok {
				t.Fatal("refused target landed")
			}
			if _, ok := w.get(w.loc, "dst/b.txt"); ok != (files == 1) {
				t.Fatal("accepted target count differs")
			}
		})
	}
}
