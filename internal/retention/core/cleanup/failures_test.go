package cleanup_test

import (
	"context"
	"errors"
	"fmt"
	"reflect"
	"testing"

	artifactsapi "github.com/weaveplatform/artifactweave/internal/artifacts/api"
	"github.com/weaveplatform/artifactweave/internal/kernel"
	"github.com/weaveplatform/artifactweave/internal/platform/schedule"
	"github.com/weaveplatform/artifactweave/internal/retention/api"
	"github.com/weaveplatform/artifactweave/internal/retention/core/cleanup"
	"github.com/weaveplatform/artifactweave/internal/retention/core/model"
	"github.com/weaveplatform/artifactweave/internal/retention/core/port"
)

var errMetadata = errors.New("private metadata failure")

type failingStore struct {
	port.Store
	policies port.CleanupStore
}

func (s failingStore) Cleanup() port.CleanupStore { return s.policies }

type failingPolicies struct {
	port.CleanupStore
	operation string
}

func (s failingPolicies) Policy(ctx context.Context, key string) (model.CleanupPolicy, error) {
	if s.operation == "read" {
		return model.CleanupPolicy{}, errMetadata
	}
	return s.CleanupStore.Policy(ctx, key)
}

func (s failingPolicies) Policies(ctx context.Context, filter port.PolicyFilter) ([]model.CleanupPolicy, error) {
	if s.operation == "list" {
		return nil, errMetadata
	}
	return s.CleanupStore.Policies(ctx, filter)
}

func (s failingPolicies) CreatePolicy(ctx context.Context, p model.CleanupPolicy) error {
	if err := s.CleanupStore.CreatePolicy(ctx, p); err != nil {
		return err
	}
	if s.operation == "create" {
		return errMetadata
	}
	return nil
}

func (s failingPolicies) UpdatePolicy(ctx context.Context, p model.CleanupPolicy) error {
	if err := s.CleanupStore.UpdatePolicy(ctx, p); err != nil {
		return err
	}
	if s.operation == "update" {
		return errMetadata
	}
	return nil
}

func (s failingPolicies) DeletePolicy(ctx context.Context, key string) error {
	if err := s.CleanupStore.DeletePolicy(ctx, key); err != nil {
		return err
	}
	if s.operation == "delete" {
		return errMetadata
	}
	return nil
}

func TestUnitPolicyMetadataFailuresNeverPublishPartialState(t *testing.T) {
	for _, operation := range []string{"create", "read", "list", "update", "delete"} {
		t.Run(operation, func(t *testing.T) {
			s, db := world(t, "*")
			ctx := t.Context()
			p, err := s.Create(ctx, "failure-policy", input("failure-policy", ""))
			must(t, err)
			before, err := db.Schedules().Get(ctx, schedule.Reference{Kind: cleanup.ScheduleKind, Key: p.Key})
			must(t, err)
			s.Store = failingStore{Store: db, policies: failingPolicies{CleanupStore: db.Cleanup(), operation: operation}}
			switch operation {
			case "create":
				_, err = s.Create(ctx, "failed-create", input("failed-create", ""))
			case "read":
				_, err = s.Get(ctx, p.Key, false)
			case "list":
				_, err = s.List(ctx, "")
			case "update":
				err = s.Enable(ctx, p.Key, true)
			case "delete":
				err = s.Delete(ctx, p.Key)
			}
			if !errors.Is(err, errMetadata) {
				t.Fatalf("store failure changed meaning: %v", err)
			}
			after, err := db.Cleanup().Policy(ctx, p.Key)
			must(t, err)
			position, err := db.Schedules().Get(ctx, before.Reference)
			must(t, err)
			if !reflect.DeepEqual(after, p) || !reflect.DeepEqual(position, before) {
				t.Fatal("failed metadata operation changed policy or scheduling state")
			}
			_, err = db.Cleanup().Policy(ctx, "failed-create")
			if !errors.Is(err, kernel.ErrNotFound) {
				t.Fatal("failed create left a policy", err)
			}
		})
	}
}

func TestUnitPolicyAdmissionFailuresRollBackRunStart(t *testing.T) {
	s, db := world(t, "*")
	ctx := t.Context()
	p, err := s.Create(ctx, "admission-policy", input("admission-policy", ""))
	must(t, err)
	must(t, s.Enable(ctx, p.Key, true))
	p, err = s.Get(ctx, p.Key, false)
	must(t, err)
	admitted, err := s.WithPolicy(ctx, p.Key, p.Revision, func(ctx context.Context, p model.CleanupPolicy) error {
		if _, err := db.Cleanup().CreateRun(ctx, model.CleanupRun{PolicyKey: p.Key, ItemType: p.ItemType, Status: api.RunInProgress}); err != nil {
			return fmt.Errorf("record run before fault: %w", err)
		}
		return errMetadata
	})
	if admitted || !errors.Is(err, errMetadata) {
		t.Fatalf("failed run start was admitted: %t %v", admitted, err)
	}
	_, err = db.Cleanup().LastRun(ctx, p.Key)
	if !errors.Is(err, kernel.ErrNotFound) {
		t.Fatal("failed admission left a run record", err)
	}
	s.Guard = func(context.Context) error { return errMetadata }
	admitted, err = s.WithPolicy(ctx, p.Key, p.Revision, func(context.Context, model.CleanupPolicy) error { t.Fatal("failed guard admitted a run"); return nil })
	if admitted || !errors.Is(err, errMetadata) {
		t.Fatalf("failed guard lost its error: %t %v", admitted, err)
	}
	guarded, _ := world(t, "*")
	admitted, err = guarded.WithPolicy(
		ctx,
		"missing-policy",
		"old-generation",
		func(context.Context, model.CleanupPolicy) error { t.Fatal("deleted policy admitted work"); return nil },
	)
	must(t, err)
	if admitted {
		t.Fatal("missing policy was admitted")
	}
	s.Guard = db.Projects().Guard
	s.Store = failingStore{Store: db, policies: failingPolicies{CleanupStore: db.Cleanup(), operation: "read"}}
	admitted, err = s.WithPolicy(ctx, p.Key, p.Revision, func(context.Context, model.CleanupPolicy) error {
		t.Fatal("failed policy read admitted work")
		return nil
	})
	if admitted || !errors.Is(err, errMetadata) {
		t.Fatalf("admission storage failure was lost: %t %v", admitted, err)
	}
	if admitted, err := (cleanup.Service{}).WithPolicy(ctx, p.Key, p.Revision, nil); admitted || !errors.Is(err, kernel.ErrStore) {
		t.Fatalf("unavailable admission service accepted work: %t %v", admitted, err)
	}
}

type failingScheduleDelete struct{ schedule.Store }

func (s failingScheduleDelete) Delete(ctx context.Context, ref schedule.Reference) error {
	if err := s.Store.Delete(ctx, ref); err != nil {
		return err
	}
	return errMetadata
}

func TestUnitPolicyDeleteScheduleFailureKeepsPolicyAndPosition(t *testing.T) {
	s, db := world(t, "*")
	ctx := t.Context()
	p, err := s.Create(ctx, "delete-schedule-fault", input("delete-schedule-fault", ""))
	must(t, err)
	before, err := db.Schedules().Get(ctx, schedule.Reference{Kind: cleanup.ScheduleKind, Key: p.Key})
	must(t, err)
	s.Schedules = failingScheduleDelete{s.Schedules}
	if err := s.Delete(ctx, p.Key); !errors.Is(err, errMetadata) {
		t.Fatal("schedule deletion failure was hidden", err)
	}
	stored, err := db.Cleanup().Policy(ctx, p.Key)
	must(t, err)
	after, err := db.Schedules().Get(ctx, before.Reference)
	must(t, err)
	if !reflect.DeepEqual(stored, p) || !reflect.DeepEqual(after, before) {
		t.Fatal("failed schedule deletion left partial policy deletion")
	}
}

type failingRepositories struct{}

func (failingRepositories) ProjectRepository(context.Context, string) (artifactsapi.ProjectRepository, error) {
	return artifactsapi.ProjectRepository{}, errMetadata
}

func TestUnitPolicyRepositoryFailuresNeverBecomeEmptySelections(t *testing.T) {
	s, _ := world(t, "*")
	ctx := t.Context()
	p := input("repository-policy", "alpha")
	p.Criteria.Repos = []string{"alpha-npm"}
	_, err := s.Create(ctx, p.Key, p)
	must(t, err)
	s.Repositories = failingRepositories{}
	if _, err := s.Get(ctx, p.Key, true); !errors.Is(err, errMetadata) {
		t.Fatal("availability failure became a successful empty selection", err)
	}
	if _, err := s.Update(ctx, p.Key, p); !errors.Is(err, errMetadata) {
		t.Fatal("repository read failure became invalid/missing input", err)
	}
	s.Repositories = nil
	if _, err := s.Get(ctx, p.Key, true); !errors.Is(err, kernel.ErrStore) {
		t.Fatal("missing lookup became empty selection", err)
	}
	if _, err := s.Update(ctx, p.Key, p); !errors.Is(err, kernel.ErrStore) {
		t.Fatal("missing repository reader allowed publication", err)
	}
	s.ProjectExists = func(context.Context, string) error { return errMetadata }
	if _, err := s.Update(ctx, p.Key, p); !errors.Is(err, errMetadata) {
		t.Fatal("project storage error became missing project", err)
	}
}

func TestUnitPolicyMissingAuthorityAndSchedulingFailClosed(t *testing.T) {
	s, db := world(t, "*")
	ctx := t.Context()
	s.Authorize = nil
	if _, err := s.List(ctx, ""); !errors.Is(err, kernel.ErrForbidden) {
		t.Fatal("missing authority admitted a read", err)
	}
	s.Authorize = func(context.Context) (cleanup.Policy, error) { return nil, nil }
	if _, err := s.List(ctx, ""); !errors.Is(err, kernel.ErrForbidden) {
		t.Fatal("nil policy admitted a read", err)
	}
	s.Authorize = func(context.Context) (cleanup.Policy, error) { return nil, errMetadata }
	if _, err := s.List(ctx, ""); !errors.Is(err, errMetadata) {
		t.Fatal("authority failure changed meaning", err)
	}
	s, _ = world(t, "none")
	if _, err := s.List(ctx, ""); !errors.Is(err, kernel.ErrForbidden) {
		t.Fatal("denied authority admitted a read", err)
	}
	s, db = world(t, "*")
	s.Schedules = nil
	if _, err := s.Create(ctx, "no-schedule", input("no-schedule", "")); !errors.Is(err, kernel.ErrStore) {
		t.Fatal("missing scheduling store allowed creation", err)
	}
	if _, err := db.Cleanup().Policy(ctx, "no-schedule"); !errors.Is(err, kernel.ErrNotFound) {
		t.Fatal("missing scheduling store left partial policy", err)
	}
	if _, err := (cleanup.Service{}).List(ctx, ""); !errors.Is(err, kernel.ErrStore) {
		t.Fatal("missing policy store admitted a read", err)
	}
}
