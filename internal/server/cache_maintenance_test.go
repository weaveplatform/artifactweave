package server

import (
	"context"
	"errors"
	"net/http"
	"strings"
	"testing"
	"time"

	"github.com/weaveplatform/artifactweave/internal/service/item"
	"github.com/weaveplatform/artifactweave/pkg/store"
	"github.com/weaveplatform/artifactweave/pkg/types"
)

func TestCacheMaintenancePermissionsAndAliases(t *testing.T) {
	w := newItemWorld(t, false)
	ctx := context.Background()
	c, err := w.db.Repos().Get(ctx, "up")
	if err != nil {
		t.Fatal(err)
	}
	svc := &item.Service{Store: w.db, Blobs: w.blobs}
	entries := map[string]types.RemoteCacheEntry{}
	for p, kind := range map[string]types.RemoteKind{"allowed/index": types.RemoteMetadata, "allowed/a.jar": types.RemoteUnique, "outside/index": types.RemoteMetadata} {
		key := types.RemoteCacheKey{RepoKey: c.Key, Path: p}
		token, err := w.db.RemoteCache().Begin(ctx, key)
		if err != nil {
			t.Fatal(err)
		}
		out, err := svc.Cache(
			ctx,
			item.CacheUpload{
				Token:  token,
				Repo:   c,
				Kind:   kind,
				Body:   strings.NewReader("bytes"),
				Length: 5,
			},
		)
		if err != nil {
			t.Fatal(err)
		}
		entries[p] = out.Entry
	}
	if err = w.db.Permissions().
		Create(ctx, types.Permission{Name: "cache-maintenance", Resources: map[types.ResourceType]types.Resource{types.ResourceArtifact: {Actions: types.Grants{Users: map[string]types.Actions{"user": types.ActionManage}}, Targets: map[string]types.Target{"up": {Include: []string{"allowed/**"}}}}}}); err != nil {
		t.Fatal(err)
	}
	for _, tc := range []struct {
		user, path string
		status     int
	}{
		{"", "/artifactory/api/zap/up-cache/allowed", http.StatusUnauthorized},
		{"reader", "/artifactory/api/zap/up-cache/allowed", http.StatusForbidden},
		{"user", "/artifactory/api/zap/up-cache/outside", http.StatusForbidden},
		{"admin", "/artifactory/api/zap/up/allowed", http.StatusBadRequest},
		{"user", "/artifactory/api/zap/up-cache/allowed", http.StatusOK},
	} {
		wantStatus(t, w.do(http.MethodPost, tc.path, tc.user, ""), tc.status)
	}
	meta, err := w.db.RemoteCache().Read(ctx, entries["allowed/index"].Token.Key)
	if err != nil || !meta.ValidatedAt.IsZero() {
		t.Fatal("metadata not expired", err)
	}
	for _, p := range []string{"allowed/a.jar", "outside/index"} {
		e, err := w.db.RemoteCache().Read(ctx, entries[p].Token.Key)
		if err != nil || e.Token != entries[p].Token {
			t.Fatal("zap exceeded scope", p, err)
		}
	}
	wantStatus(
		t,
		w.do(http.MethodGet, "/artifactory/up-cache/allowed/a.jar", "reader", ""),
		http.StatusOK,
	)
	// Delete-only authorization is also sufficient, and a confirmed miss can
	// be removed through either remote name without needing a stored item.
	if err = w.db.Permissions().
		Create(ctx, types.Permission{Name: "cache-delete", Resources: map[types.ResourceType]types.Resource{types.ResourceArtifact: {Actions: types.Grants{Users: map[string]types.Actions{"deployer": types.ActionDelete}}, Targets: map[string]types.Target{"up": {Include: []string{"allowed/**"}}}}}}); err != nil {
		t.Fatal(err)
	}
	wantStatus(
		t,
		w.do(http.MethodPost, "/artifactory/api/zap/up-cache/allowed", "deployer", ""),
		http.StatusOK,
	)
	for _, alias := range []string{"up", "up-cache"} {
		key := types.RemoteCacheKey{RepoKey: c.Key, Path: "allowed/missing"}
		token, err := w.db.RemoteCache().Begin(ctx, key)
		if err != nil {
			t.Fatal(err)
		}
		if err = w.db.RemoteCache().Negative(ctx, token, time.Now().Add(time.Hour)); err != nil {
			t.Fatal(err)
		}
		wantStatus(
			t,
			w.do(http.MethodDelete, "/artifactory/"+alias+"/allowed/missing", "reader", ""),
			http.StatusForbidden,
		)
		wantStatus(
			t,
			w.do(http.MethodDelete, "/artifactory/"+alias+"/allowed/missing", "deployer", ""),
			http.StatusNoContent,
		)
		if _, err = w.db.RemoteCache().Read(ctx, key); !errors.Is(err, store.ErrNotFound) {
			t.Fatal("negative remained", err)
		}
	}
}
