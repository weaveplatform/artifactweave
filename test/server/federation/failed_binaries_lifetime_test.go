package federation_test

import (
	"context"
	"encoding/json"
	"errors"
	"net/http"
	"strings"
	"testing"
	"time"

	"github.com/weaveplatform/artifactweave/internal/federation/core/federated"
	"github.com/weaveplatform/artifactweave/internal/federation/core/model"
	streamsync "github.com/weaveplatform/artifactweave/internal/federation/core/sync"
	"github.com/weaveplatform/artifactweave/internal/platform/outbox"
	"github.com/weaveplatform/artifactweave/internal/platform/sql/sqltest"
)

func testFailedBinaryLifetime(t *testing.T, driver string, dsn func() string) {
	_, b := newStreamWorld(t, driver, dsn(), dsn(), model.DeliveryPush, model.DeliveryPush)
	const key = "binary-lifetime"
	const digest = "1111111111111111111111111111111111111111111111111111111111111111"
	const stream = "repo:" + key
	const repository = "/artifactory/api/repositories/" + key
	const base = "/artifactory/api/federation/"
	create := func() {
		b.call(http.MethodPut, repository, `{"rclass":"federated","packageType":"generic"}`, http.StatusOK)
	}
	seed := func() {
		if _, err := b.runtime.Store.Outbox().
			RecordFailedFetch(t.Context(), outbox.FailedBlobFetch{Stream: stream, RepoKey: key, SHA256: digest, SHA1: strings.Repeat("a", 40), FirstFailed: time.Now()}); err != nil {
			t.Fatal(err)
		}
		if err := b.runtime.Store.Stamps().Record(t.Context(), model.ItemStamp{RepoKey: key, Path: "old", SHA256: digest, Pending: []byte(`{"path":"old"}`)}); err != nil {
			t.Fatal(err)
		}
		if err := b.engine.QueueFetch(t.Context(), stream, digest); err != nil {
			t.Fatal(err)
		}
	}
	counts := func(want int) {
		t.Helper()
		var got struct{ FailedTasks int }
		raw := b.call(http.MethodGet, base+"binaries/tasks/"+key, "", http.StatusOK)
		if err := json.Unmarshal([]byte(raw), &got); err != nil || got.FailedTasks != want {
			t.Fatalf("failed task counts %s, want%d (%v)", raw, want, err)
		}
	}
	empty := func() {
		t.Helper()
		counts(0)
		if raw := strings.TrimSpace(b.call(http.MethodGet, base+"binaries/failures/list?repoKey="+key, "", http.StatusOK)); raw != "[]" {
			t.Fatalf("new lifetime exposed old checksum/path: %s", raw)
		}
		if err := b.runtime.Work.EnsureFresh(t.Context(), streamsync.FetchKind, stream+" "+digest); err != nil {
			t.Fatal(err)
		}
		b.call(http.MethodGet, "/artifactory/"+key+"/old", "", http.StatusNotFound)
		other, err := b.runtime.Store.Outbox().FailedFetches(t.Context(), "repo:unrelated")
		if err != nil || len(other) != 1 {
			t.Fatalf("unrelated failures changed %+v %v", other, err)
		}
	}
	create()
	if _, err := b.runtime.Store.Outbox().RecordFailedFetch(t.Context(), outbox.FailedBlobFetch{Stream: "repo:unrelated", SHA256: digest}); err != nil {
		t.Fatal(err)
	}
	seed()
	counts(1)
	// Actual administrator repository deletion and recreation must reset failed
	// binary state even when its queued retry has not run yet.
	b.call(http.MethodDelete, repository, "", http.StatusOK)
	create()
	empty()
	seed()
	counts(1)
	// Clearing rolls back with its repository operation, including both pending
	// stamps and failure records, rather than partially erasing diagnostics.
	boom := errors.New("repository reset rolled back")
	err := b.runtime.Store.Tx(t.Context(), func(ctx context.Context) error {
		ms := federated.Membership{Store: b.runtime.Store}
		if err := ms.Clear(ctx, key); err != nil {
			return err
		}
		return boom
	})
	if !errors.Is(err, boom) {
		t.Fatal(err)
	}
	counts(1)
	pending, err := b.runtime.Store.Stamps().PendingOn(t.Context(), key, digest)
	if err != nil || len(pending) != 1 {
		t.Fatalf("pending stamp escaped reset rollback %+v %v", pending, err)
	}
	// Leaving the federated class starts a new failed-binary lifetime too.
	b.call(http.MethodPost, base+"convertToLocal", `["`+key+`"]`, http.StatusOK)
	b.call(http.MethodPost, base+"migrate/"+key, "", http.StatusOK)
	empty()
}

func TestAccFailedBinaryLifetimeSQLite(t *testing.T) {
	t.Parallel()
	testFailedBinaryLifetime(t, "", func() string { return "" })
}

func TestAccFailedBinaryLifetimePostgres(t *testing.T) {
	t.Parallel()
	dsn := sqltest.PostgresDSN(t)
	testFailedBinaryLifetime(t, "postgres", func() string { return sqltest.ScratchDSN(t, dsn) })
}
