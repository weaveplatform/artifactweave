package remote_test

import (
	"errors"
	"testing"
	"time"

	"github.com/weaveplatform/artifactweave/migration/artifacts/adapters/upstream/fakeupstream"
	"github.com/weaveplatform/artifactweave/migration/artifacts/api/repo"
	"github.com/weaveplatform/artifactweave/migration/artifacts/core/model"
	"github.com/weaveplatform/artifactweave/migration/artifacts/core/remote/source"
	"github.com/weaveplatform/artifactweave/migration/artifacts/ecosystem"
	"github.com/weaveplatform/artifactweave/migration/kernel"
)

func TestAccPolicyMutableAndDerivedClassification(t *testing.T) {
	w := policyFixture(t)
	w.p.Items.Formats = ecosystem.For
	q := w.request("file.txt", source.ResourceKindArtifact)
	q.Resource.Mutable = true
	w.up.Set("/file.txt", fakeupstream.Resource{Body: []byte("one")})
	w.mustRead(q, "one")
	w.up.Set("/file.txt", fakeupstream.Resource{Body: []byte("two")})
	w.mustRead(q, "one")
	w.clock.Advance(30 * time.Second)
	w.mustRead(q, "two")
	e, err := w.db.RemoteCache().Read(t.Context(), w.p.CacheKey(q, "credential-1"))
	if err != nil || e.Kind != model.RemoteMutable {
		t.Fatal(e, err)
	}
	q.Repo.PackageType = repo.Maven
	q.Resource.Path = "maven-metadata.xml"
	q.Resource.Mutable = false
	if w.p.Kind(q) != model.RemoteDerived {
		t.Fatal("format classification not applied")
	}
}

func TestAccPolicyDerivedNeverFetches(t *testing.T) {
	w := policyFixture(t)
	w.p.Items.Formats = ecosystem.For
	q := w.request("maven-metadata.xml", source.ResourceKindArtifact)
	q.Repo.PackageType = repo.Maven
	if _, err := w.read(q); !errors.Is(err, kernel.ErrNotFound) {
		t.Fatal(err)
	}
	if w.up.Count("", "") != 0 {
		t.Fatal("derived index fetched upstream")
	}
}
