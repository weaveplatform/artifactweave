package cleanup_test

import (
	"context"
	"errors"
	"reflect"
	"testing"

	"github.com/weaveplatform/artifactweave/internal/kernel"
	"github.com/weaveplatform/artifactweave/internal/platform/schedule"
	"github.com/weaveplatform/artifactweave/internal/retention/adapters/storetest"
	"github.com/weaveplatform/artifactweave/internal/retention/core/model"
)

func TestUnitCleanupPolicyBoundaryFailuresPreservePublishedState(t *testing.T) {
	for _, tc := range []struct {
		name, operation, action string
	}{
		{name: "update policy read", operation: "Policy", action: "update"},
		{name: "update active run read", operation: "Runs", action: "update"},
		{name: "update policy write", operation: "UpdatePolicy", action: "update"},
		{name: "delete active run read", operation: "Runs", action: "delete"},
		{name: "delete policy write", operation: "DeletePolicy", action: "delete"},
		{name: "force disable collection read", operation: "Policies", action: "disable"},
		{name: "force disable policy write", operation: "UpdatePolicy", action: "disable"},
		{name: "force disable stop listing", operation: "Runs", action: "disable"},
	} {
		t.Run(tc.name, func(t *testing.T) {
			s, db := world(t, "*")
			ctx := t.Context()
			p, err := s.Create(ctx, "policy-boundary", input("policy-boundary", "alpha"))
			must(t, err)
			must(t, s.Enable(ctx, p.Key, true))
			p, err = s.Get(ctx, p.Key, false)
			must(t, err)
			before, err := db.Schedules().Get(ctx, schedule.Reference{Kind: "cleanup:package", Key: p.Key})
			must(t, err)
			hit := false
			s.Store = cleanupBoundaryStore{Store: db, cleanup: storetest.CleanupFaults{CleanupStore: db.Cleanup(), After: func(_ context.Context, method string) error {
				if method == tc.operation {
					hit = true
					return errCleanupBoundary
				}
				return nil
			}}}
			switch tc.action {
			case "update":
				in := input(p.Key, p.ProjectKey)
				in.Description = "changed configuration"
				_, err = s.Update(ctx, p.Key, in)
			case "delete":
				err = s.Delete(ctx, p.Key)
			case "disable":
				err = s.DisableAll(ctx, p.ProjectKey)
			default:
				t.Fatalf("unknown policy action %q", tc.action)
			}
			if !hit || !errors.Is(err, errCleanupBoundary) {
				t.Fatal("policy operation ignored its store failure", err)
			}
			after, err := db.Cleanup().Policy(ctx, p.Key)
			must(t, err)
			position, err := db.Schedules().Get(ctx, before.Reference)
			must(t, err)
			if !reflect.DeepEqual(p, after) || !reflect.DeepEqual(before, position) {
				t.Fatal("failed policy change published configuration or scheduler state")
			}
		})
	}
}

func TestUnitCleanupProjectLookupFailuresCannotPublishChanges(t *testing.T) {
	for _, operation := range []string{"create", "update", "enable", "admit", "replay"} {
		for _, failure := range []string{"missing", "empty", "store"} {
			t.Run(operation+"/"+failure, func(t *testing.T) {
				s, db := world(t, "*")
				s.Work = db.WorkQueue()
				ctx := t.Context()
				p, err := s.Create(ctx, "lifetime-boundary", input("lifetime-boundary", "alpha"))
				must(t, err)
				var run model.CleanupRun
				if operation == "replay" {
					run, err = s.ManualRun(ctx, p.Key, true)
					must(t, err)
				}
				s.ProjectIncarnation = nil
				want := kernel.ErrStore
				if failure == "empty" {
					s.ProjectIncarnation = func(context.Context, string) (string, error) { return "", nil }
				}
				if failure == "store" {
					want = errCleanupBoundary
					s.ProjectIncarnation = func(context.Context, string) (string, error) { return "", errCleanupBoundary }
				}
				switch operation {
				case "create":
					_, err = s.Create(ctx, "new-lifetime-boundary", input("new-lifetime-boundary", "alpha"))
				case "update":
					p.Description = "changed"
					_, err = s.Update(ctx, p.Key, p)
				case "enable":
					err = s.Enable(ctx, p.Key, true)
				case "admit":
					_, err = s.ManualRun(ctx, p.Key, true)
				case "replay":
					var id int64
					id, err = s.RunTrigger(ctx, p.Key, run.ID)
					if id != 0 {
						t.Fatal("failed lifetime check returned a trigger")
					}
				}
				if !errors.Is(err, want) {
					t.Fatal("project lookup failure changed meaning", err)
				}
				stored, err := db.Cleanup().Policy(ctx, p.Key)
				must(t, err)
				if stored.Enabled || stored.Description != "" || stored.Revision != p.Revision {
					t.Fatal("failed lifetime check published a policy change")
				}
				_, err = db.Cleanup().Policy(ctx, "new-lifetime-boundary")
				if !errors.Is(err, kernel.ErrNotFound) {
					t.Fatal("failed project lookup created a policy", err)
				}
			})
		}
	}
}
