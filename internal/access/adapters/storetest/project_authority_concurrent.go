package storetest

import (
	"context"
	"fmt"
	"runtime"
	"sync"
	"testing"
	"time"

	"github.com/google/go-cmp/cmp"

	"github.com/weaveplatform/artifactweave/internal/access/core/model"
	"github.com/weaveplatform/artifactweave/internal/access/core/port"
	"github.com/weaveplatform/artifactweave/internal/platform/testkit/storekit"
)

//nolint:wrapcheck // callbacks retain store errors; the shared snapshot runner adds operation context
func testProjectAuthoritySnapshots(t *testing.T, newStore Factory) {
	for _, order := range []struct {
		name           string
		authorityFirst bool
	}{
		{"authority first", true},
		{"value first", false},
	} {
		t.Run("project/"+order.name, func(t *testing.T) {
			s := newStore(t)
			createProjects(t, s, "alpha")
			ps := s.Projects()
			testAuthoritySnapshots(t, s, order.authorityFirst, project("alpha"), authoritySnapshotOps[model.Project]{
				authority: func(ctx context.Context) (model.AuthorizationRevision, error) {
					a, err := ps.Authority(ctx, "alpha")
					return a.Revision, err
				},
				value: func(ctx context.Context) (model.Project, error) { return ps.Get(ctx, "alpha") },
				change: func(ctx context.Context, generation int) (model.Project, error) {
					v := project("alpha")
					v.Description = fmt.Sprintf("policy generation %d", generation)
					v.AdminPrivileges.ManageMembers = generation%2 == 0
					return v, ps.Update(ctx, v)
				},
			})
		})
		t.Run("assignment/"+order.name, func(t *testing.T) {
			s := newStore(t)
			createProjects(t, s, "alpha", "beta")
			ps := s.Projects()
			initial := model.RepositoryAssignment{RepoKey: "alpha-libs", ProjectKey: "alpha"}
			storekit.Must(t, ps.PutAssignment(t.Context(), initial))
			testAuthoritySnapshots(t, s, order.authorityFirst, initial, authoritySnapshotOps[model.RepositoryAssignment]{
				authority: func(ctx context.Context) (model.AuthorizationRevision, error) {
					a, err := ps.SharingAuthority(ctx, "alpha-libs")
					return a.Revision, err
				},
				value: func(ctx context.Context) (model.RepositoryAssignment, error) {
					return ps.Assignment(ctx, "alpha-libs")
				},
				change: func(ctx context.Context, generation int) (model.RepositoryAssignment, error) {
					v := model.RepositoryAssignment{
						RepoKey: "alpha-libs", ProjectKey: "alpha",
						Environments:   []string{fmt.Sprintf("stage-%d", generation)},
						SharedWith:     []string{"beta"},
						SharedWithAll:  generation%2 == 0,
						SharedReadOnly: generation%3 == 0,
						Updated:        storekit.At(generation),
					}
					return v, ps.PutAssignment(ctx, v)
				},
			})
		})
	}
	testProjectBindingSnapshots(t, newStore)
}

type authoritySnapshotOps[V any] struct {
	authority func(context.Context) (model.AuthorizationRevision, error)
	value     func(context.Context) (V, error)
	change    func(context.Context, int) (V, error)
}

// testAuthoritySnapshots records the expected public value before each writer
// commits. Readers can therefore validate a just-committed revision without a
// bookkeeping race. The mailboxes bound how far the writer can outrun readers.
//
//nolint:err113 // these errors are contract assertion diagnostics carried through worker result channels
func testAuthoritySnapshots[V any](t *testing.T, s port.Store, authorityFirst bool, initial V, ops authoritySnapshotOps[V]) {
	t.Helper()
	const generations = 24
	ctx, cancel := context.WithTimeout(t.Context(), 30*time.Second)
	defer cancel()
	revision := storekit.Get[model.AuthorizationRevision](t)(ops.authority(ctx))
	if revision == "" {
		t.Fatal("initial authority has no revision")
	}
	history := map[model.AuthorizationRevision]V{revision: storekit.Want(initial)}
	var historyMu sync.Mutex
	updates := []chan struct{}{make(chan struct{}, 1), make(chan struct{}, 1)}
	workers := make([]func(context.Context) error, 0, 1+len(updates))
	workers = append(workers, func(ctx context.Context) error {
		for generation := 1; generation <= generations; generation++ {
			if err := s.Tx(ctx, func(ctx context.Context) error {
				want, err := ops.change(ctx, generation)
				if err != nil {
					return fmt.Errorf("mutate generation %d: %w", generation, err)
				}
				revision, err := ops.authority(ctx)
				if err != nil {
					return fmt.Errorf("read changed authority: %w", err)
				}
				historyMu.Lock()
				defer historyMu.Unlock()
				if _, exists := history[revision]; exists || revision == "" {
					return fmt.Errorf("generation %d reused or omitted its authority revision", generation)
				}
				history[revision] = storekit.Want(want)
				return nil
			}); err != nil {
				return fmt.Errorf("writer transaction: %w", err)
			}
			for _, update := range updates {
				select {
				case update <- struct{}{}:
				case <-ctx.Done():
					return fmt.Errorf("notify snapshot readers: %w", ctx.Err())
				}
			}
		}
		return nil
	})
	for _, update := range updates {
		workers = append(workers, func(ctx context.Context) error {
			seen := map[model.AuthorizationRevision]bool{}
			for range generations {
				select {
				case <-update:
				case <-ctx.Done():
					return fmt.Errorf("wait for snapshot mutation: %w", ctx.Err())
				}
				var revision model.AuthorizationRevision
				var got V
				if err := s.Tx(ctx, func(ctx context.Context) error {
					var err error
					revision, got, err = readAuthoritySnapshot(ctx, authorityFirst, ops)
					return err
				}); err != nil {
					return fmt.Errorf("reader transaction: %w", err)
				}
				historyMu.Lock()
				want, known := history[revision]
				historyMu.Unlock()
				if !known {
					return fmt.Errorf("snapshot returned an unknown authority revision")
				}
				if diff := cmp.Diff(want, got, storekit.Opts); diff != "" {
					return fmt.Errorf("incoherent authority snapshot (-want +got):\n%s", diff)
				}
				seen[revision] = true
			}
			if len(seen) < 2 {
				return fmt.Errorf("snapshot reader observed only %d policy generations", len(seen))
			}
			return nil
		})
	}
	runAuthoritySnapshotWorkers(t, ctx, cancel, workers)
}

func readAuthoritySnapshot[V any](ctx context.Context, authorityFirst bool, ops authoritySnapshotOps[V]) (model.AuthorizationRevision, V, error) {
	var revision model.AuthorizationRevision
	var value V
	var err error
	if authorityFirst {
		revision, err = ops.authority(ctx)
	} else {
		value, err = ops.value(ctx)
	}
	if err != nil {
		return revision, value, fmt.Errorf("read first snapshot half: %w", err)
	}
	// Give the concurrent writer a scheduling opportunity between the public
	// calls; the transaction must still bind both halves to one authority.
	runtime.Gosched()
	if authorityFirst {
		value, err = ops.value(ctx)
	} else {
		revision, err = ops.authority(ctx)
	}
	if err != nil {
		return revision, value, fmt.Errorf("read second snapshot half: %w", err)
	}
	return revision, value, nil
}

func runAuthoritySnapshotWorkers(t *testing.T, ctx context.Context, cancel context.CancelFunc, workers []func(context.Context) error) {
	t.Helper()
	start := make(chan struct{})
	results := make(chan error, len(workers))
	var wg sync.WaitGroup
	for _, worker := range workers {
		wg.Go(func() {
			<-start
			err := worker(ctx)
			if err != nil {
				cancel()
			}
			results <- err
		})
	}
	done := make(chan struct{})
	go func() { wg.Wait(); close(done) }()
	close(start)
	select {
	case <-done:
	case <-ctx.Done():
		cancel()
		select {
		case <-done:
		case <-time.After(5 * time.Second):
			t.Fatal("snapshot workers did not stop after cancellation")
		}
	}
	close(results)
	for err := range results {
		if err != nil {
			t.Error(err)
		}
	}
}
