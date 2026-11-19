package itemkit

import (
	"context"
	"errors"
	"fmt"
	"strings"
	"testing"

	"github.com/weaveplatform/artifactweave/internal/artifacts/adapters/http/pkgroute"
	"github.com/weaveplatform/artifactweave/internal/artifacts/core/format"
	"github.com/weaveplatform/artifactweave/internal/kernel"
)

// The phase wrapper must retain identity, not only an error's printable text:
// the shared HTTP mapper uses the storage mark before a driver deadline.
func TestUnitListingSourceErrorPreservesLiveCause(t *testing.T) {
	driver := errors.New("original driver query failure")
	failure := errors.Join(fmt.Errorf("current source: %w: %w", kernel.ErrStore, driver), context.DeadlineExceeded)
	got := listingReadError(t.Context(), failure)
	for _, cause := range []error{driver, kernel.ErrStore, format.ErrStorageFault, context.DeadlineExceeded} {
		if !errors.Is(got, cause) {
			t.Fatalf("source phase erased %v: %v", cause, got)
		}
	}
	if !strings.Contains(got.Error(), driver.Error()) {
		t.Fatalf("original cause missing from log error: %v", got)
	}
	if mapped := pkgroute.SourceError(got, false); mapped != got {
		t.Fatalf("source mapper replaced a live storage fault with %v", mapped)
	}
}

func TestUnitListingSourceErrorHonorsEndedParent(t *testing.T) {
	ctx, cancel := context.WithCancel(t.Context())
	cancel()
	failure := fmt.Errorf("current source: %w", kernel.ErrStore)
	got := listingReadError(ctx, failure)
	if !errors.Is(got, context.Canceled) || errors.Is(got, format.ErrStorageFault) {
		t.Fatalf("ended request was promoted to a store fault: %v", got)
	}
}
