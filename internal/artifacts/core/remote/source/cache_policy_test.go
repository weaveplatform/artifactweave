package source_test

import (
	"context"
	"errors"
	"io"
	"strings"
	"testing"

	"github.com/weaveplatform/artifactweave/internal/artifacts/core/remote/source"
)

func TestUnitCachePolicyMintedOnlyForAuthorizedArtifacts(t *testing.T) {
	if !(source.Permit{}).CachePolicy().Empty() {
		t.Fatal("caller-created permit acquired a policy override")
	}
	for _, mode := range []string{"artifact", "canonical", "metadata", "blackout-only", "denied", "malformed"} {
		t.Run(mode, func(t *testing.T) {
			q := request(t, "")
			q.Resource.PolicyPath = "files/a.pkg"
			var captured source.Permit
			calls := 0
			svc := &source.Service{Resolve: identity, HTTP: backend(func(_ context.Context, _ source.Request, permit source.Permit) (source.Result, error) {
				calls++
				captured = permit
				return stream(io.NopCloser(strings.NewReader("body"))), nil
			})}
			switch mode {
			case "canonical":
				q.Resource.PolicyPath = ""
			case "metadata":
				q.Resource.Kind = source.ResourceKindMetadata
			case "blackout-only":
				q.Resource.BlackoutOnly = true
			case "denied":
				q.Access = nil
			case "malformed":
				q.Resource.PolicyPath = "../escape"
			}
			out, err := svc.Read(t.Context(), q)
			if mode == "denied" || mode == "malformed" {
				if err == nil || calls != 0 || !captured.CachePolicy().Empty() {
					t.Fatal("refused source minted authority", calls, err)
				}
				if mode == "denied" && !errors.Is(err, source.ErrDenied) {
					t.Fatal(err)
				}
				return
			}
			if err != nil || calls != 1 {
				t.Fatal(calls, err)
			}
			_ = out.Stream.Body.Close()
			if captured.CachePolicy().Empty() != (mode != "artifact") {
				t.Fatal("unexpected cache binding", mode)
			}
			if mode == "artifact" {
				if err := captured.CachePolicy().Validate(q.Repo, captured.Identity, q.Resource.Path); err != nil {
					t.Fatal(err)
				}
			}
		})
	}
}
