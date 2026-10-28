package app

import (
	"bytes"
	"crypto/sha256"
	"fmt"
	"io"
	"log/slog"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/weaveplatform/artifactweave/internal/fakeupstream"
	condafmt "github.com/weaveplatform/artifactweave/internal/format/conda"
	"github.com/weaveplatform/artifactweave/internal/format/formats"
	"github.com/weaveplatform/artifactweave/internal/server"
	"github.com/weaveplatform/artifactweave/pkg/repo"
	"github.com/weaveplatform/artifactweave/pkg/store"
)

func newCondaRemoteWorld(t *testing.T) (*remoteWorld, map[string][]byte, string) {
	t.Helper()
	w := newRemoteWorld(t)
	w.c = *repo.Defaults(&repo.Config{Key: w.c.Key, Class: repo.Remote, PackageType: repo.Conda, URL: w.up.URL})
	w.update()
	w.h = server.New(
		server.Config{
			Store:          w.rt.store,
			Blobs:          w.rt.blobs,
			Auth:           w.rt.auth,
			Tokens:         w.rt.tokens,
			Keys:           w.rt.keys,
			RemoteSources:  w.rt.remoteSources,
			RemoteHTTP:     &w.rt.remoteHTTP,
			Logger:         slog.New(slog.NewTextHandler(io.Discard, nil)),
			FormatSettings: formats.Settings{Conda: condafmt.Settings{ShardsEnabled: true}},
		},
	)
	const name = "artifactweave-conda-fixture-1.0.0-0.tar.bz2"
	body, err := os.ReadFile(filepath.Join("..", "format", "conda", "testdata", name))
	if err != nil {
		t.Fatal(err)
	}
	id, _ := condafmt.ParsePackagePath("noarch/" + name)
	record, err := condafmt.ReadPackage(t.Context(), bytes.NewReader(body), int64(len(body)), id)
	if err != nil {
		t.Fatal(err)
	}
	record.Size = int64(len(body))
	record.SHA256 = fmt.Sprintf("%x", sha256.Sum256(body))
	index := condafmt.EmptyRepodata("noarch")
	index.Packages[name] = record
	files, err := condafmt.EncodeShards(index, "", "noarch", "./")
	if err != nil {
		t.Fatal(err)
	}
	var shard string
	for name, body := range files {
		w.up.Set(
			"/noarch/"+name,
			fakeupstream.Resource{
				Body:   body,
				Header: http.Header{"Content-Type": {"application/zstd"}},
			},
		)
		if condafmt.IsShardFilename(name) {
			shard = name
		}
	}
	w.up.Set("/noarch/"+name, fakeupstream.Resource{Body: body})
	return w, files, shard
}

func TestAccCondaShardRemoteCacheRefreshAndOffline(t *testing.T) {
	w, files, shard := newCondaRemoteWorld(t)
	base := "/artifactory/api/conda/remote/noarch/"
	remoteStatus(t, w.request("GET", base+condafmt.ShardIndexFilename, "reader", ""), 200)
	remoteStatus(t, w.request("GET", base+shard, "reader", ""), 200)
	if w.up.Count("GET", "/noarch/"+shard) != 1 {
		t.Fatal("shard was not reused from cache")
	}
	entries, _, err := w.rt.store.RemoteCache().
		List(t.Context(), "remote", "noarch/"+shard, store.Page{Limit: 100})
	if err != nil || len(entries) != 1 || entries[0].Blob.Checksums.SHA256 == "" {
		t.Fatalf("validated shard not cached: %+v %v", entries, err)
	}
	// A metadata change gets a new hash; the previous cached bytes cannot be
	// relabeled or returned under the new content-addressed path.
	id, _ := condafmt.ParseMetadataPath("noarch/" + condafmt.ShardIndexFilename)
	indexDoc, err := condafmt.DecodeShard(
		files[shard],
		id,
		"artifactweave-conda-fixture",
		strings.TrimSuffix(shard, ".msgpack.zst"),
	)
	if err != nil {
		t.Fatal(err)
	}
	for filename, record := range indexDoc.Packages {
		record.License = "BSD-3-Clause"
		indexDoc.Packages[filename] = record
	}
	changed, err := condafmt.EncodeShards(indexDoc, "", "noarch", "./")
	if err != nil {
		t.Fatal(err)
	}
	var nextShard string
	for name, body := range changed {
		w.up.Set(
			"/noarch/"+name,
			fakeupstream.Resource{
				Body:   body,
				Header: http.Header{"Content-Type": {"application/zstd"}},
			},
		)
		if condafmt.IsShardFilename(name) {
			nextShard = name
		}
	}
	if nextShard == shard {
		t.Fatal("changed content reused hash")
	}
	remoteStatus(t, w.request("POST", "/artifactory/api/zap/remote-cache/noarch", "admin", ""), 200)
	remoteStatus(t, w.request("GET", base+condafmt.ShardIndexFilename, "reader", ""), 200)
	remoteStatus(t, w.request("GET", base+shard, "reader", ""), 404)
	next := w.request("GET", base+nextShard, "reader", "")
	remoteStatus(t, next, 200)
	if fmt.Sprintf("%x.msgpack.zst", sha256.Sum256(next.Body.Bytes())) != nextShard {
		t.Fatal("response hash does not match filename")
	}
	// A confirmed new index replaces the former package-name mapping after zap.
	empty, err := condafmt.EncodeShards(condafmt.EmptyRepodata("noarch"), "", "noarch", "./")
	if err != nil {
		t.Fatal(err)
	}
	w.up.Set(
		"/noarch/"+condafmt.ShardIndexFilename,
		fakeupstream.Resource{
			Body:   empty[condafmt.ShardIndexFilename],
			Header: http.Header{"Content-Type": {"application/zstd"}},
		},
	)
	remoteStatus(t, w.request("POST", "/artifactory/api/zap/remote-cache/noarch", "admin", ""), 200)
	response := w.request("GET", base+condafmt.ShardIndexFilename, "reader", "")
	remoteStatus(t, response, 200)
	index, err := condafmt.DecodeShardIndex(response.Body.Bytes())
	if err != nil || len(index.Shards) != 0 {
		t.Fatalf("stale shard advertised: %+v %v", index, err)
	}
	w.c.Offline = new(true)
	w.update()
	before := len(w.up.Requests())
	remoteStatus(t, w.request("GET", base+condafmt.ShardIndexFilename, "reader", ""), 200)
	if len(w.up.Requests()) != before {
		t.Fatal("offline index contacted upstream")
	}
}

func TestAccCondaInvalidShardNeverPublishesCacheOrFallsBack(t *testing.T) {
	for _, mode := range []string{"digest", "missing", "redirect", "media"} {
		t.Run(mode, func(t *testing.T) {
			w, files, shard := newCondaRemoteWorld(t)
			resource := fakeupstream.Resource{
				Body:   files[shard],
				Header: http.Header{"Content-Type": {"application/zstd"}},
			}
			switch mode {
			case "digest":
				resource.Body = []byte("wrong bytes")
			case "missing":
				resource.Status = 404
			case "redirect":
				resource.Redirect = "http://example.invalid/shard"
			case "media":
				resource.Header.Set("Content-Type", "text/html")
			}
			w.up.Set("/noarch/"+shard, resource)
			response := w.request(
				"GET",
				"/artifactory/api/conda/remote/noarch/"+condafmt.ShardIndexFilename,
				"reader",
				"",
			)
			if response.Code == 200 {
				t.Fatal("invalid shard returned success")
			}
			entries, _, err := w.rt.store.RemoteCache().
				List(t.Context(), "remote", "noarch/"+shard, store.Page{Limit: 100})
			if err != nil {
				t.Fatal(err)
			}
			for _, entry := range entries {
				if entry.Blob.Checksums.SHA256 != "" {
					t.Fatal("invalid shard cached")
				}
			}
			if w.up.Count("GET", "/noarch/repodata.json") != 0 {
				t.Fatal("shard failure masked by classic fallback")
			}
		})
	}
}

func TestAccCondaShardFetchRechecksPermissionAndRepositoryRevision(t *testing.T) {
	for _, mutation := range []string{"permission", "source", "credentials"} {
		t.Run(mutation, func(t *testing.T) {
			w, files, shard := newCondaRemoteWorld(t)
			barrier := &fakeupstream.Barrier{}
			t.Cleanup(barrier.Release)
			w.up.Set(
				"/noarch/"+shard,
				fakeupstream.Resource{
					Body:   files[shard],
					Header: http.Header{"Content-Type": {"application/zstd"}},
					Fault:  fakeupstream.Fault{BeforeBody: barrier},
				},
			)
			done := make(chan *httptest.ResponseRecorder, 1)
			go func() {
				done <- w.request("GET", "/artifactory/api/conda/remote/noarch/"+condafmt.ShardIndexFilename, "reader", "")
			}()
			select {
			case <-barrier.Reached():
			case <-time.After(10 * time.Second):
				t.Fatal("shard fetch did not start")
			}
			switch mutation {
			case "permission":
				if err := w.rt.store.Permissions().Delete(t.Context(), "remote-read"); err != nil {
					t.Fatal(err)
				}
			case "source":
				w.c.URL = w.up.URL + "/changed"
				w.update()
			case "credentials":
				w.c.Password = "changed"
				w.update()
			}
			barrier.Release()
			select {
			case response := <-done:
				if response.Code == 200 {
					t.Fatal("revoked shard succeeded")
				}
			case <-time.After(10 * time.Second):
				t.Fatal("request did not stop")
			}
			entries, _, err := w.rt.store.RemoteCache().
				List(t.Context(), "remote", "noarch/"+shard, store.Page{Limit: 100})
			if err != nil {
				t.Fatal(err)
			}
			for _, entry := range entries {
				if entry.Blob.Checksums.SHA256 != "" {
					t.Fatal("revoked shard published")
				}
			}
		})
	}
}
