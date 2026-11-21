package remote

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"io"
	"strings"

	"github.com/weaveplatform/artifactweave/internal/artifacts/core/item"
	"github.com/weaveplatform/artifactweave/internal/artifacts/core/model"
	"github.com/weaveplatform/artifactweave/internal/artifacts/core/remote/source"
	"github.com/weaveplatform/artifactweave/internal/kernel"
)

func mergeSelectedChecksums(base, selected kernel.Checksums) kernel.Checksums {
	if base.SHA256 == "" {
		base.SHA256 = selected.SHA256
	}
	if base.SHA512 == "" {
		base.SHA512 = selected.SHA512
	}
	if base.SHA1 == "" {
		base.SHA1 = selected.SHA1
	}
	if base.MD5 == "" {
		base.MD5 = selected.MD5
	}
	return base
}

func selectedValidator(selected source.ContentExpectation, original func(context.Context, io.ReadSeeker, int64) error) func(context.Context, io.ReadSeeker, int64) error {
	return func(ctx context.Context, body io.ReadSeeker, size int64) error {
		if size != selected.Size {
			return item.ErrRemoteLength
		}
		if original != nil {
			return original(ctx, body, size)
		}
		return nil
	}
}

func checkSelectedResult(q source.Request, result source.Result) error {
	selected := q.Resource.SelectedContent
	if selected == nil {
		return nil
	}
	if result.Item != nil && selected.Matches(result.Item.Size, result.Item.Actual) ||
		result.Stream != nil && selected.Matches(result.Stream.Size, result.Stream.Checksums) {
		return nil
	}
	return source.ErrInvalidResult
}

func selectedAnswer(q source.Request, result source.Result, readErr error) (source.Result, error) {
	if readErr != nil {
		return result, readErr
	}
	if err := checkSelectedResult(q, result); err != nil {
		if result.Stream != nil && result.Stream.Body != nil {
			_ = result.Stream.Body.Close()
		}
		return source.Result{}, err
	}
	return result, nil
}

func (p *Policy) selectedFlightKey(q source.Request, key model.RemoteCacheKey, identity model.SourceIdentity) string {
	base := p.FlightKey(key, identity)
	if q.Resource.SelectedContent == nil {
		return base
	}
	// Distinct selected contents cannot borrow another operation's validation.
	// The stored cache key stays identical to the normal/native reader's.
	selected := *q.Resource.SelectedContent
	selected.Checksums.SHA256 = strings.ToLower(selected.Checksums.SHA256)
	selected.Checksums.SHA512 = strings.ToLower(selected.Checksums.SHA512)
	selected.Checksums.SHA1 = strings.ToLower(selected.Checksums.SHA1)
	selected.Checksums.MD5 = strings.ToLower(selected.Checksums.MD5)
	raw, _ := json.Marshal([]any{selected, q.SelectedProperties})
	sum := sha256.Sum256(append(append([]byte(base), '\x00'), raw...))
	return hex.EncodeToString(sum[:])
}
