package storetest

import (
	"context"
	"testing"

	"github.com/weaveplatform/artifactweave/internal/kernel"
	"github.com/weaveplatform/artifactweave/internal/platform/blob"
	"github.com/weaveplatform/artifactweave/internal/platform/testkit/storekit"
)

// runUploadDigests: a Put is read back by its SHA-256 alone, a second Put
// of the same SHA-256 keeps the first row, inside a unit of work too, a row
// missing a digest is refused and an unknown SHA-256 is not found.
func runUploadDigests(t *testing.T, newStore Factory) {
	t.Run("UploadDigests", func(t *testing.T) {
		s := newStore(t)
		ctx := t.Context()
		first := blob.Digests{
			SHA256: "8f434346648f6b96df89dda901c5176b10a6d83961dd3c1ac88b59b2dc327aa4",
			SHA1:   "86f7e437faa5a7fce15d1ddcb9eaeaea377667b8",
			MD5:    "0cc175b9c0f1b6a831c399e269772661",
		}
		storekit.Must(t, s.UploadDigests().Put(ctx, first))
		storekit.Same(t, "recorded digests", first,
			storekit.Get[blob.Digests](t)(s.UploadDigests().Get(ctx, first.SHA256)))
		again := first
		again.SHA1, again.MD5 = "da39a3ee5e6b4b0d3255bfef95601890afd80709", "d41d8cd98f00b204e9800998ecf8427e"
		storekit.Must(t, s.Tx(ctx, func(ctx context.Context) error {
			return s.UploadDigests().Put(ctx, again)
		}))
		storekit.Same(t, "first row kept", first,
			storekit.Get[blob.Digests](t)(s.UploadDigests().Get(ctx, first.SHA256)))
		for _, d := range []blob.Digests{
			{SHA1: first.SHA1, MD5: first.MD5},
			{SHA256: first.SHA256, MD5: first.MD5},
			{SHA256: first.SHA256, SHA1: first.SHA1},
		} {
			storekit.WantErr(t, s.UploadDigests().Put(ctx, d), kernel.ErrInvalid)
		}
		_, err := s.UploadDigests().
			Get(ctx, "0000000000000000000000000000000000000000000000000000000000000000")
		storekit.WantErr(t, err, kernel.ErrNotFound)
	})
}
