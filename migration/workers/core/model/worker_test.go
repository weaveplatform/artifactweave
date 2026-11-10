package model_test

import (
	"errors"
	"strings"
	"testing"
	"time"

	"github.com/weaveplatform/artifactweave/migration/artifacts/api/repo"
	"github.com/weaveplatform/artifactweave/migration/workers/core/model"
)

var t0 = time.Date(2026, 10, 13, 12, 0, 0, 0, time.UTC)

func TestUnitWorkers(t *testing.T) {
	if !model.WorkerAction("BEFORE_DOWNLOAD").Before() ||
		model.WorkerAction("AFTER_CREATE").Before() {
		t.Error("WorkerAction.Before")
	}
	wf := model.WorkerFilter{
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
	if !(model.WorkerFilter{AnyLocal: true, AnyFederated: true}).Matches(
		"fed",
		repo.Federated,
		"x",
	) {
		t.Error("AnyFederated")
	}
	w := model.Worker{BlockedUntil: t0}
	if !w.Blocked(t0.Add(-time.Second)) || w.Blocked(t0) {
		t.Error("Blocked")
	}
	if err := model.ValidateWorkerSource("export default async () => ({status: 1})"); err != nil {
		t.Error(err)
	}
	for _, src := range []string{"", strings.Repeat("x", model.MaxWorkerSource+1)} {
		if err := model.ValidateWorkerSource(src); !errors.Is(err, model.ErrInvalidWorker) {
			t.Errorf("len %d: %v", len(src), err)
		}
	}
	if err := model.ValidateWorkerState(
		map[string]string{"k": strings.Repeat("é", model.MaxWorkerStateValue)},
	); err != nil {
		t.Errorf("limits count characters, not bytes: %v", err)
	}
	tooMany := map[string]string{}
	for i := range model.MaxWorkerStateItems + 1 {
		tooMany[strings.Repeat("k", i+1)] = "v"
	}
	for _, bad := range []map[string]string{
		tooMany,
		{"": "v"},
		{strings.Repeat("k", model.MaxWorkerStateKey+1): "v"},
		{"k": strings.Repeat("v", model.MaxWorkerStateValue+1)},
	} {
		if err := model.ValidateWorkerState(bad); !errors.Is(err, model.ErrInvalidWorker) {
			t.Errorf("%d items: %v", len(bad), err)
		}
	}
}
