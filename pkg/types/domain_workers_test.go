package types_test

import (
	"errors"
	"strings"
	"testing"
	"time"

	"github.com/weaveplatform/artifactweave/migration/artifacts/api/repo"
	"github.com/weaveplatform/artifactweave/pkg/types"
)

func TestUnitWorkers(t *testing.T) {
	if !types.WorkerAction("BEFORE_DOWNLOAD").Before() ||
		types.WorkerAction("AFTER_CREATE").Before() {
		t.Error("WorkerAction.Before")
	}
	wf := types.WorkerFilter{
		RepoKeys:        []string{"libs"},
		AnyRemote:       true,
		ExcludePatterns: []string{"*.tmp"},
	}
	cases := []struct {
		key  string
		c    repo.Class
		path string
		want bool
	}{
		{"libs", repo.Local, "a.jar", true},
		{"libs", repo.Local, "a.tmp", false},
		{"cache", repo.Remote, "a.jar", true},
		{"other", repo.Local, "a.jar", false},
		{"fed", repo.Federated, "a.jar", false},
	}
	for _, c := range cases {
		if got := wf.Matches(c.key, c.c, c.path); got != c.want {
			t.Errorf("%+v = %v", c, got)
		}
	}
	if !(types.WorkerFilter{AnyLocal: true, AnyFederated: true}).Matches(
		"fed",
		repo.Federated,
		"x",
	) {
		t.Error("AnyFederated")
	}
	w := types.Worker{BlockedUntil: t0}
	if !w.Blocked(t0.Add(-time.Second)) || w.Blocked(t0) {
		t.Error("Blocked")
	}
	if err := types.ValidateWorkerSource("export default async () => ({status: 1})"); err != nil {
		t.Error(err)
	}
	for _, src := range []string{"", strings.Repeat("x", types.MaxWorkerSource+1)} {
		if err := types.ValidateWorkerSource(src); !errors.Is(err, types.ErrInvalidWorker) {
			t.Errorf("len %d: %v", len(src), err)
		}
	}
	if err := types.ValidateWorkerState(
		map[string]string{"k": strings.Repeat("é", types.MaxWorkerStateValue)},
	); err != nil {
		t.Errorf("limits count characters, not bytes: %v", err)
	}
	tooMany := map[string]string{}
	for i := range types.MaxWorkerStateItems + 1 {
		tooMany[strings.Repeat("k", i+1)] = "v"
	}
	for _, bad := range []map[string]string{
		tooMany,
		{"": "v"},
		{strings.Repeat("k", types.MaxWorkerStateKey+1): "v"},
		{"k": strings.Repeat("v", types.MaxWorkerStateValue+1)},
	} {
		if err := types.ValidateWorkerState(bad); !errors.Is(err, types.ErrInvalidWorker) {
			t.Errorf("%d items: %v", len(bad), err)
		}
	}
}
