package sql_test

import (
	"errors"
	"path/filepath"
	"testing"
	"time"

	artsql "github.com/weaveplatform/artifactweave/migration/artifacts/adapters/sql"
	"github.com/weaveplatform/artifactweave/migration/artifacts/api/repo"
	"github.com/weaveplatform/artifactweave/migration/artifacts/core/model"
	"github.com/weaveplatform/artifactweave/migration/artifacts/core/port"
	"github.com/weaveplatform/artifactweave/migration/kernel"
)

// These open one SQLite file, close it and open it again, as a restart
// does, and check that what was written is read back and that deletes
// stay deleted.

func TestAccP9bPackageIndexesSurviveRestart(t *testing.T) {
	ctx := t.Context()
	path := filepath.Join(t.TempDir(), "formats.db")
	open := func() *artsql.Store {
		t.Helper()
		return mustOpenFile(t, path, nil)
	}
	s := open()
	formats := []repo.PackageType{
		repo.LuaRocks, repo.NIMModel, repo.Skills,
		repo.AgentPlugins, repo.AgentPackages,
	}
	for _, format := range formats {
		key := string(format) + "-repo"
		p, err := s.Packages().Ensure(ctx, model.Package{
			RepoKey: key, Type: format, Name: "example",
		})
		if err != nil {
			t.Fatalf("%s package: %v", format, err)
		}
		if _, err := s.Packages().PutVersion(ctx, model.PackageVersion{
			PackageID: p.ID, Version: "1.2.3", Metadata: []byte(`{"qualified":true}`),
		}); err != nil {
			t.Fatalf("%s version: %v", format, err)
		}
	}
	if err := s.Close(); err != nil {
		t.Fatal(err)
	}
	s = open()
	defer func() { _ = s.Close() }()
	for _, format := range formats {
		key := string(format) + "-repo"
		p, err := s.Packages().Find(ctx, key, "example")
		if err != nil || p.Type != format {
			t.Fatalf("%s package after restart: %+v, %v", format, p, err)
		}
		v, err := s.Packages().Version(ctx, p.ID, "1.2.3")
		if err != nil || string(v.Metadata) != `{"qualified":true}` {
			t.Fatalf("%s version after restart: %+v, %v", format, v, err)
		}
	}
}

func TestAccGitLFSLocksSurviveRestart(t *testing.T) {
	ctx := t.Context()
	path := filepath.Join(t.TempDir(), "gitlfs-locks.db")
	open := func() *artsql.Store {
		t.Helper()
		return mustOpenFile(t, path, nil)
	}
	s := open()
	if err := s.Repos().Create(ctx, repo.Config{
		Key: "lfs-local", Class: repo.Local, PackageType: repo.GitLFS,
	}); err != nil {
		t.Fatal(err)
	}
	lock := model.GitLFSLock{
		ID: "durable-1", RepoKey: "lfs-local", Path: "src/a.bin", Owner: "alice",
		LockedAt: time.Date(2026, 10, 26, 12, 0, 0, 123456000, time.UTC),
	}
	if err := s.GitLFSLocks().Create(ctx, lock); err != nil {
		t.Fatal(err)
	}
	if err := s.Close(); err != nil {
		t.Fatal(err)
	}
	s = open()
	got, err := s.GitLFSLocks().Get(ctx, lock.RepoKey, lock.ID)
	if err != nil || got != lock {
		t.Fatalf("lock after restart = %+v, %v; want %+v", got, err, lock)
	}
	rows, next, err := s.GitLFSLocks().List(ctx,
		port.GitLFSLockFilter{RepoKey: lock.RepoKey}, kernel.Page{Limit: 1})
	if err != nil || next != "" || len(rows) != 1 || rows[0] != lock {
		t.Fatalf("lock list after restart = %+v, %q, %v", rows, next, err)
	}
	if _, err := s.GitLFSLocks().Delete(ctx, lock.RepoKey, lock.ID); err != nil {
		t.Fatal(err)
	}
	if err := s.Close(); err != nil {
		t.Fatal(err)
	}
	s = open()
	defer func() { _ = s.Close() }()
	_, err = s.GitLFSLocks().Get(ctx, lock.RepoKey, lock.ID)
	if !errors.Is(err, kernel.ErrNotFound) {
		t.Fatalf("deleted lock after restart: %v, want not found", err)
	}
}

func TestAccBowerRegistrationsSurviveRestart(t *testing.T) {
	ctx := t.Context()
	path := filepath.Join(t.TempDir(), "bower-registrations.db")
	open := func() *artsql.Store {
		t.Helper()
		return mustOpenFile(t, path, nil)
	}
	s := open()
	c := repo.Config{
		Key:            "bower",
		Class:          repo.Remote,
		PackageType:    repo.Bower,
		URL:            "https://stash.example",
		VCSGitProvider: repo.Stash,
	}
	if err := s.Repos().Create(ctx, c); err != nil {
		t.Fatal(err)
	}
	registration := model.BowerRegistration{
		RepoKey: c.Key,
		Name:    "hello",
		URL:     "ssh://git@stash.example:7999/team/hello.git",
	}
	if err := s.BowerRegistrations().Create(ctx, registration); err != nil {
		t.Fatal(err)
	}
	if err := s.Close(); err != nil {
		t.Fatal(err)
	}
	s = open()
	got, err := s.BowerRegistrations().Get(ctx, c.Key, "hello")
	if err != nil || got != registration {
		t.Fatalf("registration after restart: %+v %v", got, err)
	}
	if err := s.Repos().Delete(ctx, c.Key); err != nil {
		t.Fatal(err)
	}
	if err := s.Repos().Create(ctx, c); err != nil {
		t.Fatal(err)
	}
	if err := s.Close(); err != nil {
		t.Fatal(err)
	}
	s = open()
	defer s.Close()
	if _, err := s.BowerRegistrations().
		Get(ctx, c.Key, "hello"); !errors.Is(
		err,
		kernel.ErrNotFound,
	) {
		t.Fatalf("recreated repository inherited registration: %v", err)
	}
}
