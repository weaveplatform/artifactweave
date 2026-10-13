package format

import (
	"context"
	"testing"

	"github.com/weaveplatform/artifactweave/pkg/repo"
	"github.com/weaveplatform/artifactweave/pkg/types"
)

func TestGenericKeepsNoIndex(t *testing.T) {
	h := For(repo.Generic)
	ctx := context.Background()
	if err := h.Deployed(ctx, nil, repo.Config{}, types.Item{}); err != nil {
		t.Fatal(err)
	}
	if err := h.Deleting(ctx, nil, repo.Config{}, types.Item{}); err != nil {
		t.Fatal(err)
	}
}
