package upstream

import (
	"context"
	"net/url"

	"github.com/weaveplatform/artifactweave/internal/artifacts/core/remote"
)

// matrixKey carries the matrix string Do appended to a request's path
// (remote.Request.Matrix) to the checks the credential layer and the
// destination policy make on the request and on every redirect: they see
// only the URL, and without it would refuse the escapes 7.x sends in a
// matrix value as they refuse them in the item path.
type matrixKey struct{}

func withMatrix(ctx context.Context, matrix string) context.Context {
	return context.WithValue(ctx, matrixKey{}, matrix)
}

// checkedURL checks u with remote.CheckedRequestURL against the matrix
// string ctx carries, if any: only a URL whose escaped path ends with that
// exact string has it judged by remote.CheckedMatrix, and the rest of the
// URL, a proxy's or another redirect target's included, by
// remote.CheckedURL.
func checkedURL(ctx context.Context, u *url.URL) error {
	matrix, _ := ctx.Value(matrixKey{}).(string)
	if _, err := remote.CheckedRequestURL(u.String(), matrix); err != nil {
		//nolint:wrapcheck // preserves upstream error identity
		return err
	}
	return nil
}
