package remote_test

import (
	"errors"
	"testing"
	"time"

	"github.com/weaveplatform/artifactweave/internal/fakeupstream"
	"github.com/weaveplatform/artifactweave/internal/remote"
	"github.com/weaveplatform/artifactweave/internal/source"
	"github.com/weaveplatform/artifactweave/migration/artifacts/api/repo"
	"github.com/weaveplatform/artifactweave/migration/artifacts/ecosystem"
	"github.com/weaveplatform/artifactweave/pkg/store"
	"github.com/weaveplatform/artifactweave/pkg/types"
)

func TestAccPolicyMutableAndDerivedClassification(t *testing.T) {
	w := remote.PolicyFixture(t)
	w.Policy().Items.Formats = ecosystem.For
	q := w.Request("file.txt", source.ResourceKindArtifact)
	q.Resource.Mutable = true
	w.Upstream().Set("/file.txt", fakeupstream.Resource{Body: []byte("one")})
	w.MustRead(q, "one")
	w.Upstream().Set("/file.txt", fakeupstream.Resource{Body: []byte("two")})
	w.MustRead(q, "one")
	w.Clock().Advance(30 * time.Second)
	w.MustRead(q, "two")
	e, err := w.DB().RemoteCache().Read(t.Context(), w.Policy().CacheKey(q, "credential-1"))
	if err != nil || e.Kind != types.RemoteMutable {
		t.Fatal(e, err)
	}
	q.Repo.PackageType = repo.Maven
	q.Resource.Path = "maven-metadata.xml"
	q.Resource.Mutable = false
	if w.Policy().Kind(q) != types.RemoteDerived {
		t.Fatal("format classification not applied")
	}
}

func TestAccPolicyDerivedNeverFetches(t *testing.T) {
	w := remote.PolicyFixture(t)
	w.Policy().Items.Formats = ecosystem.For
	q := w.Request("maven-metadata.xml", source.ResourceKindArtifact)
	q.Repo.PackageType = repo.Maven
	if _, err := w.Read(q); !errors.Is(err, store.ErrNotFound) {
		t.Fatal(err)
	}
	if w.Upstream().Count("", "") != 0 {
		t.Fatal("derived index fetched upstream")
	}
}
