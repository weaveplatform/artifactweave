package source_test

import (
	"context"
	"errors"
	"io"
	"strings"
	"testing"
	"time"

	"github.com/weaveplatform/artifactweave/internal/artifacts/core/model"
	"github.com/weaveplatform/artifactweave/internal/artifacts/core/remote/source"
	"github.com/weaveplatform/artifactweave/internal/kernel"
)

func selectedPublication(t *testing.T) source.Request {
	t.Helper()
	q := request(t, "")
	q.Resource.SelectedContent = &source.ContentExpectation{Size: 4, Checksums: kernel.Checksums{SHA256: strings.Repeat("a", 64)}}
	q.Publication = &source.Publication{
		Check: func(context.Context) error { return nil },
		Fence: func(context.Context) error { return nil },
	}
	return q
}

func TestUnitExecutionPublicationNeedsSelectedContentAndCompleteFences(t *testing.T) {
	for _, test := range []struct {
		name string
		edit func(*source.Request)
	}{
		{"unselected", func(q *source.Request) { q.Resource.SelectedContent = nil }},
		{"missing-current-check", func(q *source.Request) { q.Publication.Check = nil }},
		{"missing-lease-fence", func(q *source.Request) { q.Publication.Fence = nil }},
		{"properties-without-execution", func(q *source.Request) {
			properties := model.Properties{"quality": {"verified"}}
			q.SelectedProperties, q.Publication = &properties, nil
		}},
		{"property-clock-without-properties", func(q *source.Request) { q.SelectedPropertiesStarted = time.Now() }},
	} {
		t.Run(test.name, func(t *testing.T) {
			q := selectedPublication(t)
			test.edit(&q)
			assertRequestRejectedBeforeResolution(t, q)
		})
	}
}

func TestUnitExecutionPublicationCannotBorrowLookupOrDeliveryAuthority(t *testing.T) {
	q := selectedPublication(t)
	current, calls, fences := true, 0, 0
	q.Publication.Check = func(context.Context) error {
		if !current {
			return source.ErrDenied
		}
		return nil
	}
	q.Publication.Fence = func(context.Context) error { fences++; return nil }
	s := source.Service{Resolve: identity, HTTP: backend(func(context.Context, source.Request, source.Permit) (source.Result, error) {
		calls++
		return stream(io.NopCloser(strings.NewReader("data"))), nil
	})}
	out, err := s.Read(t.Context(), q)
	if err != nil || out.Stream == nil {
		t.Fatal("current execution was refused", err)
	}
	defer out.Stream.Body.Close()
	current = false
	if err = out.Revalidate(t.Context()); !errors.Is(err, source.ErrDenied) {
		t.Fatal("delivered bytes retained revoked execution authority", err)
	}
	if _, err = s.Read(t.Context(), q); !errors.Is(err, source.ErrDenied) || calls != 1 || fences != 0 {
		t.Fatalf("revoked lookup reached backend: %v calls=%d fences=%d", err, calls, fences)
	}
	// Malformed direct calls also fail closed, independently of Service's
	// complete request validation.
	q.Publication.Check = nil
	if err = q.CheckPublication(t.Context()); !errors.Is(err, source.ErrInvalidRequest) {
		t.Fatal(err)
	}
	q.Publication.Fence = nil
	if err = q.FencePublication(t.Context()); !errors.Is(err, source.ErrInvalidRequest) {
		t.Fatal(err)
	}
}
