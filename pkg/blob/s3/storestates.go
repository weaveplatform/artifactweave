package s3

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"time"

	"github.com/weaveplatform/weaveplatform-artifact-registry/pkg/blob"
	"github.com/weaveplatform/weaveplatform-artifact-registry/pkg/store"
)

// backendName marks the upload sessions this backend owns in the shared
// uploads table, so the reaper knows which blob store aborts them.
const backendName = "s3"

// StoreStates keeps upload sessions in the metadata store's uploads table
// (decision 0004), so a Docker chunked upload or a CLI multipart upload
// resumes across requests and survives a restart, unlike MemoryStates.
func StoreStates(us store.UploadStore) UploadStateStore {
	return storeStates{us: us, now: time.Now}
}

type storeStates struct {
	us  store.UploadStore
	now func() time.Time
}

// storedState is the session's storage format: tagged here rather than on
// UploadState so renaming a Go field cannot orphan the sessions in flight.
type storedState struct {
	Key         string       `json:"key,omitempty"`
	MultipartID string       `json:"multipart_id,omitempty"`
	Parts       []storedPart `json:"parts,omitempty"`
	Size        int64        `json:"size"`
	Hasher      []byte       `json:"hasher,omitempty"`
	Pending     []byte       `json:"pending,omitempty"`
	Expected    string       `json:"expected,omitempty"`
}

type storedPart struct {
	Number int32  `json:"number"`
	ETag   string `json:"etag"`
}

// Put stores st, keeping the session's original start time.
func (s storeStates) Put(ctx context.Context, st UploadState) error {
	doc := storedState{
		Key: st.Key, MultipartID: st.MultipartID, Size: st.Size,
		Hasher: st.Hasher, Pending: st.Pending, Expected: st.Expected,
	}
	for _, p := range st.Parts {
		doc.Parts = append(doc.Parts, storedPart(p))
	}
	b, err := json.Marshal(doc)
	if err != nil {
		return fmt.Errorf("blob/s3: encode upload %s: %w", st.ID, err)
	}
	now := s.now()
	started := now
	cur, err := s.us.Get(ctx, st.ID)
	switch {
	case err == nil:
		started = cur.Started
	case !errors.Is(err, store.ErrNotFound):
		return fmt.Errorf("blob/s3: load upload %s: %w", st.ID, err)
	}
	if err := s.us.Put(ctx, store.Upload{
		ID: st.ID, Backend: backendName, State: b, Bytes: st.Size, Started: started, Updated: now,
	}); err != nil {
		return fmt.Errorf("blob/s3: store upload %s: %w", st.ID, err)
	}
	return nil
}

// Get returns the session, or an error wrapping blob.ErrUploadUnknown when
// the store has none under id or holds one this backend cannot read.
func (s storeStates) Get(ctx context.Context, id string) (UploadState, error) {
	u, err := s.us.Get(ctx, id)
	if errors.Is(err, store.ErrNotFound) {
		return UploadState{}, fmt.Errorf("%w: %s", blob.ErrUploadUnknown, id)
	}
	if err != nil {
		return UploadState{}, fmt.Errorf("blob/s3: load upload %s: %w", id, err)
	}
	var doc storedState
	if u.Backend != backendName {
		return UploadState{}, fmt.Errorf(
			"%w: %s belongs to the %q backend",
			blob.ErrUploadUnknown,
			id,
			u.Backend,
		)
	}
	if err := json.Unmarshal(u.State, &doc); err != nil {
		return UploadState{}, fmt.Errorf("%w: %s: %w", blob.ErrUploadUnknown, id, err)
	}
	st := UploadState{
		ID: id, Key: doc.Key, MultipartID: doc.MultipartID, Size: doc.Size,
		Hasher: doc.Hasher, Pending: doc.Pending, Expected: doc.Expected,
	}
	for _, p := range doc.Parts {
		st.Parts = append(st.Parts, Part(p))
	}
	return st, nil
}

// Delete forgets a session; forgetting one that is already gone is not an
// error, as with MemoryStates.
func (s storeStates) Delete(ctx context.Context, id string) error {
	if err := s.us.Delete(ctx, id); err != nil && !errors.Is(err, store.ErrNotFound) {
		return fmt.Errorf("blob/s3: forget upload %s: %w", id, err)
	}
	return nil
}
