package s3

import (
	"context"
	"errors"
	"testing"
	"time"

	"github.com/google/go-cmp/cmp"

	"github.com/weaveplatform/artifactweave/pkg/blob"
	"github.com/weaveplatform/artifactweave/pkg/blob/blobtest"
	"github.com/weaveplatform/artifactweave/pkg/store"
	"github.com/weaveplatform/artifactweave/pkg/store/inmem"
)

// TestContractWithStoreStates runs the blob contract with sessions kept in
// the metadata store, so resume goes through a serialised session.
func TestContractWithStoreStates(t *testing.T) {
	small(t)
	blobtest.Run(t, func(t *testing.T) blob.Store {
		return newFakeStore(t, newFake(), StoreStates(inmem.New().Uploads()))
	}, blobtest.Options{LargeSize: 4*testPart + 1234})
}

func TestStoreStatesRoundTrip(t *testing.T) {
	ctx := context.Background()
	uploads := inmem.New().Uploads()
	clock := time.Date(2026, 10, 13, 12, 0, 0, 0, time.UTC)
	s := storeStates{us: uploads, now: func() time.Time { return clock }}
	st := UploadState{
		ID: "u1", Key: "art/_uploads/u1", MultipartID: "mp", Size: 42,
		Parts:  []Part{{Number: 1, ETag: `"e1"`}, {Number: 2, ETag: `"e2"`}},
		Hasher: []byte{1, 2, 3}, Pending: []byte("tail"), Expected: "abc",
	}
	if err := s.Put(ctx, st); err != nil {
		t.Fatal(err)
	}
	clock = clock.Add(time.Minute)
	st.Size = 50
	if err := s.Put(ctx, st); err != nil {
		t.Fatal(err)
	}
	got, err := s.Get(ctx, "u1")
	if err != nil {
		t.Fatal(err)
	}
	if diff := cmp.Diff(st, got); diff != "" {
		t.Fatalf("Get (-want +got):\n%s", diff)
	}
	row, err := uploads.Get(ctx, "u1")
	if err != nil {
		t.Fatal(err)
	}
	if row.Backend != backendName || row.Bytes != 50 ||
		!row.Started.Equal(clock.Add(-time.Minute)) || !row.Updated.Equal(clock) {
		t.Fatalf("stored row = %+v", row)
	}
	if err := s.Delete(ctx, "u1"); err != nil {
		t.Fatal(err)
	}
	if err := s.Delete(ctx, "u1"); err != nil {
		t.Fatalf("deleting a forgotten session: %v", err)
	}
	if _, err := s.Get(ctx, "u1"); !errors.Is(err, blob.ErrUploadUnknown) {
		t.Fatalf("Get after Delete = %v", err)
	}
}

func TestStoreStatesForeignOrDamaged(t *testing.T) {
	ctx := context.Background()
	uploads := inmem.New().Uploads()
	for _, u := range []store.Upload{
		{ID: "fs1", Backend: "fs", State: []byte("{}")},
		{ID: "bad", Backend: backendName, State: []byte("not json")},
	} {
		if err := uploads.Put(ctx, u); err != nil {
			t.Fatal(err)
		}
	}
	s := StoreStates(uploads)
	for _, id := range []string{"fs1", "bad"} {
		if _, err := s.Get(ctx, id); !errors.Is(err, blob.ErrUploadUnknown) {
			t.Errorf("Get(%s) = %v, want ErrUploadUnknown", id, err)
		}
	}
}

// failingUploads is an upload store whose every call fails.
type failingUploads struct{ store.UploadStore }

func (failingUploads) Get(context.Context, string) (store.Upload, error) {
	return store.Upload{}, errInjected
}
func (failingUploads) Put(context.Context, store.Upload) error { return errInjected }
func (failingUploads) Delete(context.Context, string) error    { return errInjected }

// putFails reads fine but cannot write.
type putFails struct{ store.UploadStore }

func (putFails) Put(context.Context, store.Upload) error { return errInjected }

func TestStoreStatesErrors(t *testing.T) {
	ctx := context.Background()
	s := StoreStates(failingUploads{})
	if err := s.Put(ctx, UploadState{ID: "u"}); !errors.Is(err, errInjected) {
		t.Errorf("Put = %v", err)
	}
	if _, err := s.Get(
		ctx,
		"u",
	); !errors.Is(err, errInjected) ||
		errors.Is(err, blob.ErrUploadUnknown) {
		t.Errorf("Get = %v", err)
	}
	if err := s.Delete(ctx, "u"); !errors.Is(err, errInjected) {
		t.Errorf("Delete = %v", err)
	}
	s = StoreStates(putFails{inmem.New().Uploads()})
	if err := s.Put(ctx, UploadState{ID: "u"}); !errors.Is(err, errInjected) {
		t.Errorf("Put through a failing store = %v", err)
	}
}
