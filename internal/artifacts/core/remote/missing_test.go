package remote_test

import (
	"errors"
	"fmt"
	"net/http"
	"reflect"
	"testing"
	"time"

	"github.com/weaveplatform/artifactweave/internal/artifacts/adapters/upstream/fakeupstream"
	"github.com/weaveplatform/artifactweave/internal/artifacts/core/model"
	"github.com/weaveplatform/artifactweave/internal/artifacts/core/remote/source"
	"github.com/weaveplatform/artifactweave/internal/kernel"
)

func TestAccPolicyMissingOriginRetainsExpiredFile(t *testing.T) {
	for _, kind := range []source.ResourceKind{source.ResourceKindArtifact, source.ResourceKindMetadata} {
		for _, method := range []string{http.MethodGet, http.MethodHead} {
			for _, historical := range []bool{false, true} {
				t.Run(fmt.Sprint(kind, "/", method, "/historical=", historical), func(t *testing.T) {
					w := policyFixture(t)
					q := w.request("file.txt", kind)
					q.Resource.Mutable = true
					w.up.Set("/file.txt", fakeupstream.Resource{Body: []byte("old bytes"), ETag: `"old"`, Modified: w.clock.Now()})
					w.mustRead(q, "old bytes")
					key := w.p.CacheKey(q, "credential-1")
					if historical {
						token, err := w.db.RemoteCache().Begin(t.Context(), key)
						if err != nil {
							t.Fatal(err)
						}
						if err = w.db.RemoteCache().Negative(t.Context(), token, w.clock.Now()); err != nil {
							t.Fatal(err)
						}
					}
					before, err := w.db.RemoteCache().Read(t.Context(), key)
					if err != nil {
						t.Fatal(err)
					}
					w.clock.Advance(31 * time.Second)
					w.up.Set("/file.txt", fakeupstream.Resource{Status: 404})
					q.Resource.Method = method
					result, err := w.read(q)
					if err != nil {
						t.Fatal("retained expired file", err)
					}
					if method == http.MethodGet {
						if got := w.bytes(result); got != "old bytes" {
							t.Fatal("changed retained bytes", got)
						}
					} else if result.Stream != nil {
						if result.Stream.Size != int64(len("old bytes")) {
							t.Fatal("HEAD lost retained size")
						}
						_ = result.Stream.Body.Close()
					}
					after, err := w.db.RemoteCache().Read(t.Context(), key)
					if err != nil {
						t.Fatal(err)
					}
					if !after.ValidatedAt.Equal(w.clock.Now()) || !after.NegativeUntil.IsZero() {
						t.Fatalf("retained copy was not unexpired: %+v", after)
					}
					if after.Token.Epoch != before.Token.Epoch || after.Token.Revision != before.Token.Revision+1 {
						t.Fatal("unexpected publication token", before.Token, after.Token)
					}
					before.Token = after.Token
					before.ValidatedAt = after.ValidatedAt
					before.NegativeUntil = time.Time{}
					if !reflect.DeepEqual(before, after) {
						t.Fatalf("unexpire changed retained content or validators: before%+v after%+v", before, after)
					}
					requests := len(w.up.Requests())
					w.clock.Advance(time.Second)
					q.Resource.Method = http.MethodGet
					next, err := w.read(q)
					if err != nil || w.bytes(next) != "old bytes" {
						t.Fatal("unexpired hit", err)
					}
					if len(w.up.Requests()) != requests {
						t.Fatal("unexpired copy contacted upstream")
					}
				})
			}
		}
	}
}

func TestAccPolicyMissingOriginKeepsRefusalAndNegativeBoundaries(t *testing.T) {
	for _, mode := range []string{"cold", "fresh", "unique", "fresh-negative", "no-cache", "must-revalidate", "non-file"} {
		t.Run(mode, func(t *testing.T) {
			w := policyFixture(t)
			q := w.request("file.txt", source.ResourceKindArtifact)
			q.Resource.Mutable = mode != "unique"
			if mode == "non-file" {
				q.Resource.Kind = source.ResourceKindMetadata
				q.Resource.NoCacheFile = true
			}
			header := http.Header{}
			if mode == "no-cache" || mode == "must-revalidate" {
				header.Set("Cache-Control", mode)
			}
			var before model.RemoteCacheEntry
			key := w.p.CacheKey(q, "credential-1")
			if mode != "cold" {
				w.up.Set("/file.txt", fakeupstream.Resource{Body: []byte("old"), Header: header})
				w.mustRead(q, "old")
				if mode == "fresh-negative" {
					token, err := w.db.RemoteCache().Begin(t.Context(), key)
					if err != nil {
						t.Fatal(err)
					}
					if err = w.db.RemoteCache().Negative(t.Context(), token, w.clock.Now().Add(time.Hour)); err != nil {
						t.Fatal(err)
					}
				}
				var err error
				before, err = w.db.RemoteCache().Read(t.Context(), key)
				if err != nil {
					t.Fatal(err)
				}
			}
			if mode == "fresh" {
				w.clock.Advance(time.Second)
			} else {
				w.clock.Advance(time.Minute)
			}
			w.up.Set("/file.txt", fakeupstream.Resource{Status: 404})
			requests := len(w.up.Requests())
			result, err := w.read(q)
			if mode == "fresh" || mode == "unique" {
				if err != nil || w.bytes(result) != "old" || len(w.up.Requests()) != requests {
					t.Fatal("cache hit changed", err)
				}
				return
			}
			if !errors.Is(err, kernel.ErrNotFound) {
				t.Fatal("refusal/miss", err)
			}
			after, err := w.db.RemoteCache().Read(t.Context(), key)
			if err != nil {
				t.Fatal(err)
			}
			if mode == "no-cache" || mode == "must-revalidate" {
				if !after.NegativeUntil.IsZero() || !after.ValidatedAt.Equal(before.ValidatedAt) {
					t.Fatal("strict HTTP rule changed the retained copy", after)
				}
			} else if after.NegativeUntil.IsZero() {
				t.Fatal("genuine/fresh/non-file miss lost negative state")
			}
			if mode == "fresh-negative" && len(w.up.Requests()) != requests {
				t.Fatal("fresh negative contacted upstream")
			}
		})
	}
}
