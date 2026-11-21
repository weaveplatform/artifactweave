package remote_test

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"errors"
	"io"
	"net/http"
	"testing"
	"time"

	"github.com/weaveplatform/artifactweave/internal/artifacts/adapters/upstream/fakeupstream"
	"github.com/weaveplatform/artifactweave/internal/artifacts/core/remote/source"
	"github.com/weaveplatform/artifactweave/internal/kernel"
)

func selectedContent(body string) *source.ContentExpectation {
	sum := sha256.Sum256([]byte(body))
	return &source.ContentExpectation{Size: int64(len(body)), Checksums: kernel.Checksums{SHA256: hex.EncodeToString(sum[:])}}
}

func TestAccSelectedContentWarmsOrdinaryCacheIdentity(t *testing.T) {
	w := policyFixture(t)
	w.up.Set("/file.txt", fakeupstream.Resource{Body: []byte("verified")})
	q := w.request("file.txt", source.ResourceKindArtifact)
	ordinary := q
	q.Resource.SelectedContent = selectedContent("verified")
	if w.p.CacheKey(q, "credential-1") != w.p.CacheKey(ordinary, "credential-1") {
		t.Fatal("selected transfer created a private alternate cache namespace")
	}
	w.mustRead(q, "verified")
	count := w.up.Count("", "")
	w.up.Set("/file.txt", fakeupstream.Resource{Body: []byte("different")})
	w.mustRead(ordinary, "verified")
	if w.up.Count("", "") != count {
		t.Fatal("ordinary warm read missed the replication cache entry")
	}
	q.Resource.SelectedContent = selectedContent("different")
	w.mustRead(q, "different")
	w.mustRead(ordinary, "different")
}

func TestAccSelectedContentRejectsBadPublication(t *testing.T) {
	for _, tc := range []struct {
		name     string
		body     string
		size     int64
		truncate int64
	}{
		{"wrong checksum", "corrupt!", 8, 0},
		{"wrong selected size", "verified", 9, 0},
		{"truncated body", "verified", 8, 3},
	} {
		t.Run(tc.name, func(t *testing.T) {
			w := policyFixture(t)
			w.up.Set("/bad.bin", fakeupstream.Resource{Body: []byte(tc.body), Fault: fakeupstream.Fault{TruncateAfter: tc.truncate}})
			q := w.request("bad.bin", source.ResourceKindArtifact)
			q.Resource.SelectedContent = selectedContent("verified")
			q.Resource.SelectedContent.Size = tc.size
			result, err := w.read(q)
			if err == nil || result.Item != nil || result.Stream != nil {
				t.Fatal("unverified selected content was delivered", err)
			}
			if _, err := w.db.Items().Get(t.Context(), w.c.Key, true, q.Resource.Path); !errors.Is(err, kernel.ErrNotFound) {
				t.Fatal("bad transfer published a cached item", err)
			}
		})
	}
}

func TestAccSelectedContentSupersedesCachedMiss(t *testing.T) {
	w := policyFixture(t)
	q := w.request("later.bin", source.ResourceKindArtifact)
	if _, err := w.read(q); !errors.Is(err, kernel.ErrNotFound) {
		t.Fatal("fixture did not cache upstream miss", err)
	}
	w.up.Set("/later.bin", fakeupstream.Resource{Body: []byte("verified")})
	q.Resource.SelectedContent = selectedContent("verified")
	w.mustRead(q, "verified")
	q.Resource.SelectedContent = nil
	w.mustRead(q, "verified")
}

func TestAccSelectedContentRetainsNativeValidatorAndOfflineRules(t *testing.T) {
	w := policyFixture(t)
	w.up.Set("/native.json", fakeupstream.Resource{Body: []byte("verified"), Header: http.Header{"Content-Type": {"application/json"}}})
	q := w.request("native.json", source.ResourceKindMetadata)
	q.Resource.RepositoryFile, q.Resource.Variant = true, "fixture-native-v1"
	validated := false
	q.Resource.ValidateMetadata = func(context.Context, io.ReadSeeker, int64, source.MetadataResponse) error {
		validated = true
		return nil
	}
	q.Resource.SelectedContent = selectedContent("verified")
	w.mustRead(q, "verified")
	if !validated {
		t.Fatal("transfer expectation replaced the native validator")
	}
	count := w.up.Count("", "")
	q.Repo.Offline = ptr(true)
	q.Resource.SelectedContent = selectedContent("different")
	result, err := w.read(q)
	if err == nil || result.Item != nil || result.Stream != nil || w.up.Count("", "") != count {
		t.Fatal("offline transfer fetched or claimed mismatched cached bytes", err)
	}
}

func TestAccSelectedContentCannotBorrowOrdinaryFillValidation(t *testing.T) {
	w := policyFixture(t)
	gate := &fakeupstream.Barrier{}
	ctx, cancel := context.WithTimeout(t.Context(), 5*time.Second)
	defer cancel()
	defer gate.Release()
	w.up.Set("/flight.bin", fakeupstream.Resource{Body: []byte("ordinary"), Fault: fakeupstream.Fault{BeforeBody: gate}})
	ordinary := w.request("flight.bin", source.ResourceKindArtifact)
	type answer struct {
		result source.Result
		err    error
	}
	first := make(chan answer, 1)
	go func() {
		result, err := w.p.Read(ctx, ordinary, w.permit)
		first <- answer{result, err}
	}()
	consumed := false
	defer func() {
		gate.Release()
		cancel()
		if !consumed {
			old := <-first
			if old.result.Stream != nil {
				_ = old.result.Stream.Body.Close()
			}
		}
	}()
	select {
	case <-gate.Reached():
	case <-ctx.Done():
		t.Fatal("ordinary fill did not reach body barrier")
	}
	w.up.Set("/flight.bin", fakeupstream.Resource{Body: []byte("selected")})
	selected := ordinary
	selected.Resource.SelectedContent = selectedContent("selected")
	result, err := w.p.Read(ctx, selected, w.permit)
	gate.Release()
	old := <-first
	consumed = true
	if err != nil {
		t.Fatal("selected operation joined ordinary unverified flight", err)
	}
	if w.bytes(result) != "selected" || old.err == nil {
		t.Fatal("concurrent fill reused another operation's validation or publication")
	}
	w.mustRead(ordinary, "selected")
}
