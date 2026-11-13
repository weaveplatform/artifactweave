package source_test

import (
	"context"
	"errors"
	"fmt"
	"testing"

	"github.com/weaveplatform/artifactweave/internal/artifacts/core/format"
	"github.com/weaveplatform/artifactweave/internal/artifacts/core/remote/source"
)

// A failed read of the server's own copy under check leaves the source
// boundary as it is, the store's error included, so a router answers it
// as a server fault, never as the ErrUnavailable (502) an unknown failure
// becomes or the timeout the store's error may wrap.
func TestUnitReadPassesAStorageFault(t *testing.T) {
	errDropped := errors.New("the store dropped the connection")
	for name, read := range map[string]error{
		"dropped":  errDropped,
		"deadline": fmt.Errorf("%w: %w", errDropped, context.DeadlineExceeded),
	} {
		fault := fmt.Errorf("cache check: %w: %w", format.ErrStorageFault, read)
		svc := &source.Service{
			Resolve: identity,
			HTTP: backend(
				func(context.Context, source.Request, source.Permit) (source.Result, error) {
					return source.Result{}, fault
				},
			),
		}
		_, err := svc.Read(t.Context(), request(t, ""))
		if err != fault || !errors.Is(err, errDropped) {
			t.Fatalf("%s: %v", name, err)
		}
	}
	svc := &source.Service{
		Resolve: identity,
		HTTP: backend(func(context.Context, source.Request, source.Permit) (source.Result, error) {
			return source.Result{}, errors.New("unknown")
		}),
	}
	if _, err := svc.Read(t.Context(), request(t, "")); err != source.ErrUnavailable {
		t.Fatalf("an unknown failure: %v", err)
	}
}
