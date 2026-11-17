package remote_test

import (
	"context"
	"errors"
	"testing"
	"time"

	"github.com/weaveplatform/artifactweave/internal/artifacts/adapters/upstream/fakeupstream"
	"github.com/weaveplatform/artifactweave/internal/artifacts/api/repo"
	"github.com/weaveplatform/artifactweave/internal/artifacts/core/remote/source"
	"github.com/weaveplatform/artifactweave/internal/kernel"
)

func TestAccMetadataRefreshNotifiesAfterPublication(t *testing.T) {
	w := policyFixture(t)
	changes := 0
	w.p.MetadataChanged = func(ctx context.Context, c repo.Config, path string) {
		if ctx.Err() != nil || c.Key != w.c.Key || path != "index.yaml" {
			t.Error(c.Key, path, ctx.Err())
		}
		changes++
	}
	w.up.Set("/index.yaml", fakeupstream.Resource{Body: []byte("entries: {}"), ETag: `"one"`})
	q := w.request("index.yaml", source.ResourceKindMetadata)
	w.mustRead(q, "entries: {}")
	if changes != 1 {
		t.Fatal(changes)
	}
	w.mustRead(q, "entries: {}")
	if changes != 1 {
		t.Fatal("cache hit invalidated", changes)
	}
	w.clock.Advance(time.Minute)
	w.mustRead(q, "entries: {}")
	if changes != 2 {
		t.Fatal("304 refresh did not notify", changes)
	}
	w.up.Set("/index.yaml", fakeupstream.Resource{Status: 404})
	w.clock.Advance(time.Minute)
	if _, err := w.read(q); !errors.Is(err, kernel.ErrNotFound) {
		t.Fatal(err)
	}
	if changes != 3 {
		t.Fatal("missing index did not notify", changes)
	}
	w.up.Set("/chart.tgz", fakeupstream.Resource{Body: []byte("chart")})
	w.mustRead(w.request("chart.tgz", source.ResourceKindArtifact), "chart")
	if changes != 3 {
		t.Fatal("artifact invalidated metadata", changes)
	}
}
