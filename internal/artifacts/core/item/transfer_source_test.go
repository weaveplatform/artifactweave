package item

import (
	"context"
	"errors"
	"net/http"
	"testing"

	accessapi "github.com/weaveplatform/artifactweave/internal/access/api"
	"github.com/weaveplatform/artifactweave/internal/artifacts/api/repo"
	"github.com/weaveplatform/artifactweave/internal/artifacts/core/format"
	"github.com/weaveplatform/artifactweave/internal/artifacts/core/model"
	"github.com/weaveplatform/artifactweave/internal/kernel"
)

type transferSourceGuard struct {
	bare
	check func(repo.Config, model.Item) error
}

func TestUnitTransferRechecksEveryCurrentSourceIncludingSidecars(t *testing.T) {
	for _, move := range []bool{false, true} {
		for _, folder := range []bool{false, true} {
			t.Run(map[bool]string{false: "copy", true: "move"}[move]+map[bool]string{false: "/sidecar", true: "/descendant"}[folder], func(t *testing.T) {
				w := keeping(t)
				w.must("src/x.bin", "content")
				w.must("src/x.bin.sig", "signed content")
				dst := w.addRepo(repo.Config{Key: "target", Class: repo.Local})
				path := "src/x.bin"
				destination := "out/x.bin"
				if folder {
					path = "src"
					destination = "out"
				}
				checkedAdditional := false
				_, err := w.svc.Transfer(t.Context(), Transfer{
					From: Target{Repo: w.loc, Path: path, CheckCurrent: func(ctx context.Context, current string, _ bool, want accessapi.Actions) error {
						if !w.db.InTx(ctx) || !want.Has(accessapi.ActionRead) || move && !want.Has(accessapi.ActionDelete) {
							t.Fatal("source action did not join the current publication check")
						}
						if current != path {
							checkedAdditional = true
							return kernel.ErrForbidden
						}
						return nil
					}},
					To: Target{Repo: dst, Path: destination}, Move: move,
				})
				if !checkedAdditional || !errors.Is(err, kernel.ErrForbidden) {
					t.Fatal("revoked child source was published", err)
				}
				w.has(w.loc, true, "src/x.bin", "src/x.bin.sig")
				if _, exists := w.get(dst, "out"); exists {
					t.Fatal("refused source created a destination or its parents")
				}
			})
		}
	}
}

func (g transferSourceGuard) GuardTransferSource(
	_ context.Context,
	_ format.Env,
	c repo.Config,
	it model.Item,
) error {
	return g.check(c, it)
}

func TestUnitTransferSourceGuardPrecedesDestinationInspection(t *testing.T) {
	for _, mode := range []string{"allow", "reject", "fault"} {
		t.Run(mode, func(t *testing.T) {
			w := newWorld(t)
			src := w.must("data.txt", "secret")
			dst := w.addRepo(repo.Config{Key: "target", Class: repo.Local, PackageType: repo.Conda})
			r := &recorder{calls: map[string]bool{}}
			fault := errors.New("source authority unavailable")
			called := false
			guard := transferSourceGuard{check: func(c repo.Config, it model.Item) error {
				called = true
				if c.Key != w.loc.Key || it.ID != src.ID || len(r.order) != 0 {
					t.Fatalf(
						"source guard received %s:%s after destination calls %v",
						c.Key,
						it.FullPath(),
						r.order,
					)
				}
				switch mode {
				case "reject":
					return format.Rejectf(http.StatusForbidden, "source cannot leave its format")
				case "fault":
					return fault
				default:
					return nil
				}
			}}
			w.svc.Formats = func(kind repo.PackageType) format.Handler {
				if kind == dst.PackageType {
					return r
				}
				return guard
			}
			result, err := w.svc.Transfer(t.Context(), Transfer{
				From: Target{Repo: w.loc, Path: src.FullPath()},
				To:   Target{Repo: dst, Path: "copy.txt"}, Move: true,
			})
			if !called {
				t.Fatal("source guard did not run")
			}
			if mode == "fault" && !errors.Is(err, fault) || mode != "fault" && err != nil {
				t.Fatalf("transfer result %+v, error %v", result, err)
			}
			_, landed := w.get(dst, "copy.txt")
			_, retained := w.get(w.loc, src.FullPath())
			if landed != (mode == "allow") || retained != (mode != "allow") {
				t.Fatalf("landed %v, retained %v", landed, retained)
			}
			if mode == "reject" &&
				(result.Failed != 1 || len(result.Messages) == 0 || result.Messages[0].Level != LevelError) {
				t.Fatalf("source refusal was not a per-item failure: %+v", result)
			}
			if mode != "allow" && len(r.order) != 0 {
				t.Fatalf("refused source reached destination hooks: %v", r.order)
			}
		})
	}
}

func TestUnitTransferSourceGuardFaultAbortsWholeFolder(t *testing.T) {
	w := newWorld(t)
	w.must("src/a.txt", "first")
	w.must("src/b.txt", "second")
	dst := w.addRepo(repo.Config{Key: "target", Class: repo.Local, PackageType: repo.Conda})
	r := &recorder{calls: map[string]bool{}}
	fault := errors.New("source policy unavailable after accepted item")
	checked := 0
	w.svc.Formats = func(kind repo.PackageType) format.Handler {
		if kind == dst.PackageType {
			return r
		}
		return transferSourceGuard{check: func(_ repo.Config, it model.Item) error {
			checked++
			if it.FullPath() == "src/b.txt" {
				if len(r.order) == 0 {
					t.Fatal("first item was not planned before the fault")
				}
				return fault
			}
			return nil
		}}
	}
	_, err := w.svc.Transfer(t.Context(), Transfer{
		From: Target{Repo: w.loc, Path: "src"}, To: Target{Repo: dst, Path: "out"}, Move: true,
	})
	if !errors.Is(err, fault) || checked != 2 {
		t.Fatalf("checked %d sources, error %v", checked, err)
	}
	if _, ok := w.get(dst, "out"); ok {
		t.Fatal("aborted plan wrote a destination")
	}
	for p, body := range map[string]string{"src/a.txt": "first", "src/b.txt": "second"} {
		if _, ok := w.get(w.loc, p); !ok || w.refs(sums(body).SHA256) != 1 {
			t.Fatalf("aborted plan changed source or references for %s", p)
		}
	}
}
