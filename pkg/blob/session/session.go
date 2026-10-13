// Package session is the upload session shared by the object-store blob
// backends (s3, gcs, azure; decision 0027). Object stores cannot append, so
// each backend buffers appended bytes into parts and uploads a part whenever
// one is full; between HTTP requests the session must survive in the
// metadata store so a Docker chunked upload or a CLI multipart upload
// resumes in a later request or another process.
//
// A Session holds what every backend needs: the size, the declared digest,
// the saved blob.Hasher state and the pending tail shorter than one part,
// plus a backend-specific document D (S3's multipart upload ID and part
// list, GCS's part objects, Azure's block count). It is persisted through a
// Store after every append. InUploads keeps sessions in the uploads table
// under a per-store backend name such as "s3:hot", so two stores of one kind
// never share or misattribute sessions.
package session

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"

	"github.com/weaveplatform/artifactweave/pkg/blob"
)

// Session is one upload session. D is the backend's own state; it is stored
// as JSON, so its fields carry json tags and are the storage format.
type Session[D any] struct {
	// ID names the session for Resume.
	ID string
	// Size counts every appended byte, uploaded or pending.
	Size int64
	// Expected is the SHA-256 declared at BeginUpload, or "".
	Expected string
	// Pending is the tail not yet handed to the backend.
	Pending []byte
	// Data is the backend's state.
	Data D

	hasher *blob.Hasher
	store  Store
}

// record is the stored form of a session.
type record struct {
	Size     int64           `json:"size"`
	Expected string          `json:"expected,omitempty"`
	Hasher   []byte          `json:"hasher,omitempty"`
	Pending  []byte          `json:"pending,omitempty"`
	Data     json.RawMessage `json:"data,omitempty"`
}

// Begin starts a session with a new upload ID and saves it.
func Begin[D any](ctx context.Context, st Store, opts ...blob.UploadOption) (*Session[D], error) {
	if err := ctx.Err(); err != nil {
		return nil, fmt.Errorf("blob/session: begin: %w", err)
	}
	o, err := blob.ResolveOptions(opts...)
	if err != nil {
		return nil, err //nolint:wrapcheck // already a blob sentinel
	}
	id, err := blob.NewUploadID()
	if err != nil {
		return nil, err //nolint:wrapcheck // names itself
	}
	s := &Session[D]{ID: id, Expected: o.ExpectedSHA256, hasher: blob.NewHasher(), store: st}
	if err := s.Save(ctx); err != nil {
		return nil, err
	}
	return s, nil
}

// Resume loads a saved session. Unknown or malformed IDs, and sessions whose
// state cannot be restored, return an error wrapping blob.ErrUploadUnknown:
// there is no local copy of the uploaded parts to rehash, so a damaged
// session ends.
func Resume[D any](ctx context.Context, st Store, id string) (*Session[D], error) {
	if err := ctx.Err(); err != nil {
		return nil, fmt.Errorf("blob/session: resume: %w", err)
	}
	if !blob.ValidUploadID(id) {
		return nil, fmt.Errorf("%w: %q", blob.ErrUploadUnknown, id)
	}
	raw, err := st.Get(ctx, id)
	if err != nil {
		return nil, fmt.Errorf("blob/session: resume: %w", err)
	}
	var rec record
	if err := json.Unmarshal(raw, &rec); err != nil {
		return nil, fmt.Errorf("%w: %s: %w", blob.ErrUploadUnknown, id, err)
	}
	s := &Session[D]{
		ID: id, Size: rec.Size, Expected: rec.Expected, Pending: rec.Pending,
		hasher: blob.NewHasher(), store: st,
	}
	if err := s.hasher.UnmarshalBinary(rec.Hasher); err != nil {
		return nil, fmt.Errorf("%w: %s: %w", blob.ErrUploadUnknown, id, err)
	}
	if len(rec.Data) > 0 {
		if err := json.Unmarshal(rec.Data, &s.Data); err != nil {
			return nil, fmt.Errorf("%w: %s: %w", blob.ErrUploadUnknown, id, err)
		}
	}
	return s, nil
}

// Save persists the session.
func (s *Session[D]) Save(ctx context.Context) error {
	hs, err := s.hasher.MarshalBinary()
	if err != nil {
		return err //nolint:wrapcheck // names itself
	}
	data, err := json.Marshal(s.Data)
	if err != nil {
		return fmt.Errorf("blob/session: encode %s: %w", s.ID, err)
	}
	raw, err := json.Marshal(record{
		Size: s.Size, Expected: s.Expected, Hasher: hs, Pending: s.Pending, Data: data,
	})
	if err != nil {
		return fmt.Errorf("blob/session: encode %s: %w", s.ID, err)
	}
	if err := s.store.Put(ctx, s.ID, raw, s.Size); err != nil {
		return fmt.Errorf("blob/session: save %s: %w", s.ID, err)
	}
	return nil
}

// Live reports, with an error wrapping blob.ErrUploadUnknown, a session that
// was committed or aborted since this handle was resumed.
func (s *Session[D]) Live(ctx context.Context) error {
	if _, err := s.store.Get(ctx, s.ID); err != nil {
		return fmt.Errorf("blob/session: %w", err)
	}
	return nil
}

// Forget removes the saved session.
func (s *Session[D]) Forget(ctx context.Context) error {
	if err := s.store.Delete(ctx, s.ID); err != nil {
		return fmt.Errorf("blob/session: forget %s: %w", s.ID, err)
	}
	return nil
}

// FlushFunc hands one part to the backend. On success the session drops the
// part from Pending; on error the bytes stay pending (and hashed), so the
// next Append or Commit retries the same part.
type FlushFunc func(ctx context.Context, part []byte) error

// Append reads r into Pending, calling flush whenever partSize bytes are
// buffered, and saves the session. Bytes read are hashed and counted even
// when flush or the read fails, so the session stays consistent and the
// client may continue from Size.
func (s *Session[D]) Append(
	ctx context.Context,
	r io.Reader,
	partSize int64,
	flush FlushFunc,
) (int64, error) {
	if err := ctx.Err(); err != nil {
		return 0, fmt.Errorf("blob/session: append: %w", err)
	}
	if err := s.Live(ctx); err != nil {
		return 0, err
	}
	var total int64
	buf := make([]byte, 256<<10)
	var appendErr error
	for appendErr == nil {
		room := max(int(partSize)-len(s.Pending), 0)
		n, rerr := r.Read(buf[:min(len(buf), room)])
		if n > 0 {
			s.Pending = append(s.Pending, buf[:n]...)
			_, _ = s.hasher.Write(buf[:n])
			s.Size += int64(n)
			total += int64(n)
		}
		if int64(len(s.Pending)) >= partSize {
			appendErr = s.Flush(ctx, flush)
		}
		switch {
		case errors.Is(rerr, io.EOF):
			if appendErr == nil {
				return total, s.Save(ctx)
			}
		case rerr != nil && appendErr == nil:
			appendErr = fmt.Errorf("blob/session: read: %w", rerr)
		}
	}
	if err := s.Save(ctx); err != nil {
		return total, errors.Join(appendErr, err)
	}
	return total, appendErr
}

// Flush hands the pending bytes to flush when there are any and drops them
// on success.
func (s *Session[D]) Flush(ctx context.Context, flush FlushFunc) error {
	if len(s.Pending) == 0 {
		return nil
	}
	if err := flush(ctx, s.Pending); err != nil {
		return err
	}
	s.Pending = s.Pending[:0]
	return nil
}

// Verify compares every non-empty field of want, with SHA256 defaulting to
// the declared digest, against the digests of everything appended, and
// returns those digests. A mismatch wraps blob.ErrDigestMismatch; the caller
// aborts the session.
func (s *Session[D]) Verify(want blob.Digests) (blob.Digests, error) {
	if want.SHA256 == "" {
		want.SHA256 = s.Expected
	}
	got := s.hasher.Sum()
	return got, blob.Verify(want, got) //nolint:wrapcheck // already a blob sentinel
}
