package virtual

import (
	"context"

	"github.com/weaveplatform/artifactweave/internal/artifacts/core/model"
)

// Copy is a virtual repository's transformed representation of a member
// file. Item describes the copy's own bytes and transform time. The member
// and source digest fence reuse after membership, content or policy changes.
type Copy struct {
	Virtual      string     `json:"virtual"`
	Path         string     `json:"path"`
	Member       string     `json:"member"`
	SourceSHA256 string     `json:"source_sha256"`
	Policy       string     `json:"policy"`
	Item         model.Item `json:"item"`
}

// CopyStore persists virtual-owned copies separately from member items.
// They are hidden from item/storage listings and searches. Put owns one
// blob reference per copy; replacement and repository deletion release it.
type CopyStore interface {
	Get(ctx context.Context, virtual, path string) (Copy, error)
	Put(ctx context.Context, copy Copy) error
	DeleteRepo(ctx context.Context, virtual string) error
}
