// Package registry runs the Docker registry's blob uploads (decision 0031).
// A session spans requests and survives a restart: it is a row in the
// uploads table under Backend, naming the blob store upload it appends to,
// and its repository, image and user, which alone may continue it. A
// finished upload, whether chunked, monolithic or mounted from another
// image, becomes the item <image>/_uploads/sha256__<hex> through the item
// service, where a manifest push finds it. The garbage collector hands
// stale sessions to Reap and asks ExpireUploads for finished uploads no
// manifest took in time.
package registry

import (
	"context"
	"crypto/sha256"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"sync"
	"time"

	"github.com/weaveplatform/artifactweave/internal/format"
	layout "github.com/weaveplatform/artifactweave/internal/format/docker"
	"github.com/weaveplatform/artifactweave/internal/service/item"
	"github.com/weaveplatform/artifactweave/pkg/aql"
	"github.com/weaveplatform/artifactweave/pkg/blob"
	"github.com/weaveplatform/artifactweave/pkg/repo"
	"github.com/weaveplatform/artifactweave/pkg/store"
	"github.com/weaveplatform/artifactweave/pkg/types"
)

// Backend names registry sessions in the uploads table, so the garbage
// collector hands them to Reap rather than to the blob store.
const Backend = "docker"

var (
	// ErrUnknown is a session that never existed, has ended, or belongs to
	// another repository, image or user: BLOB_UPLOAD_UNKNOWN.
	ErrUnknown = errors.New("registry: unknown upload session")
	// ErrOffset is a chunk that does not start where the session ends.
	// Nothing was read; the client resumes from the session's size.
	ErrOffset = errors.New("registry: chunk out of order")
	// ErrDigest is content whose SHA-256 is not the declared digest. The
	// session, if any, is discarded.
	ErrDigest = errors.New("registry: digest mismatch")
)

// Service runs registry uploads over a metadata store and its blob store.
type Service struct {
	Store store.Store
	Blobs blob.Store
	// Now supplies the time; nil means time.Now.
	Now func() time.Time
	// Formats and Work reach the item service's format hooks (decision
	// 0032); nil means formats.For and the store's queue.
	Formats func(repo.PackageType) format.Handler
	Work    item.Enqueuer

	// locks serialise one session's requests in this process, striped by
	// session id so the set stays bounded however many uploads run. Two
	// instances serving one session at once is the client's doing, as with
	// any blob store upload (pkg/blob).
	locks [64]sync.Mutex
}

// Owner is who a session belongs to.
type Owner struct {
	Repo repo.Config
	// Name is the image inside Repo.
	Name string
	// By is the user that opened it.
	By string
}

// state is a session's uploads row State; the row's RepoKey holds the
// repository.
type state struct {
	Name string `json:"name"`
	By   string `json:"by"`
	// Blob is the blob store upload the session appends to.
	Blob string `json:"blob"`
}

func (s *Service) now() time.Time {
	if s.Now == nil {
		return time.Now().UTC()
	}
	return s.Now().UTC()
}

func (s *Service) items() *item.Service {
	return &item.Service{
		Store: s.Store, Blobs: s.Blobs, Now: s.Now, Formats: s.Formats, Work: s.Work,
	}
}

// lock takes the stripe of session id's lock.
func (s *Service) lock(id string) func() {
	sum := sha256.Sum256([]byte(id))
	l := &s.locks[int(sum[0])%len(s.locks)]
	l.Lock()
	return l.Unlock
}

// Begin opens a session for o and returns its id.
func (s *Service) Begin(ctx context.Context, o Owner) (string, error) {
	up, err := s.Blobs.BeginUpload(ctx)
	if err != nil {
		return "", fmt.Errorf("registry: begin upload: %w", err)
	}
	id, err := blob.NewUploadID()
	if err == nil {
		now := s.now()
		err = s.save(ctx, store.Upload{
			ID: id, Backend: Backend, RepoKey: o.Repo.Key, Path: o.Name,
			Started: now, Updated: now,
		}, state{Name: o.Name, By: o.By, Blob: up.ID()})
	}
	if err != nil {
		_ = up.Abort(context.WithoutCancel(ctx))
		return "", err
	}
	return id, nil
}

func (s *Service) save(ctx context.Context, u store.Upload, st state) error {
	raw, err := json.Marshal(st)
	if err != nil {
		return fmt.Errorf("registry: encode session: %w", err)
	}
	u.State = raw
	if err := s.Store.Uploads().Put(ctx, u); err != nil {
		return fmt.Errorf("registry: save session %s: %w", u.ID, err)
	}
	return nil
}

// open loads session id for o with its blob store upload. A session the
// blob store no longer knows (aborted, committed, or reaped) has ended,
// and its row goes.
func (s *Service) open(
	ctx context.Context,
	o Owner,
	id string,
) (store.Upload, blob.Upload, error) {
	if !blob.ValidUploadID(id) {
		return store.Upload{}, nil, ErrUnknown
	}
	u, err := s.Store.Uploads().Get(ctx, id)
	if errors.Is(err, store.ErrNotFound) {
		return u, nil, ErrUnknown
	}
	if err != nil {
		return u, nil, fmt.Errorf("registry: load session %s: %w", id, err)
	}
	var st state
	if u.Backend != Backend || json.Unmarshal(u.State, &st) != nil ||
		u.RepoKey != o.Repo.Key || st.Name != o.Name || st.By != o.By {
		return u, nil, ErrUnknown
	}
	up, err := s.Blobs.ResumeUpload(ctx, st.Blob)
	if errors.Is(err, blob.ErrUploadUnknown) {
		_ = s.forget(ctx, id)
		return u, nil, ErrUnknown
	}
	if err != nil {
		return u, nil, fmt.Errorf("registry: resume session %s: %w", id, err)
	}
	return u, up, nil
}

func (s *Service) forget(ctx context.Context, id string) error {
	err := s.Store.Uploads().Delete(ctx, id)
	if err != nil && !errors.Is(err, store.ErrNotFound) {
		return fmt.Errorf("registry: drop session %s: %w", id, err)
	}
	return nil
}

// Status returns how many bytes session id holds.
func (s *Service) Status(ctx context.Context, o Owner, id string) (int64, error) {
	defer s.lock(id)()
	_, up, err := s.open(ctx, o, id)
	if err != nil {
		return 0, err
	}
	return up.Size(), nil
}

// Append adds body to session id and returns the session's size. offset
// is where the client says the chunk starts, or negative when it sent no
// range; a chunk that does not start at the session's end is refused with
// ErrOffset before any of it is read. The body streams through to the
// blob store, so a layer is never held in memory.
func (s *Service) Append(
	ctx context.Context,
	o Owner,
	id string,
	offset int64,
	body io.Reader,
) (int64, error) {
	defer s.lock(id)()
	u, up, err := s.open(ctx, o, id)
	if err != nil {
		return 0, err
	}
	if err := s.appendTo(ctx, u, up, offset, body); err != nil {
		return up.Size(), err
	}
	return up.Size(), nil
}

// appendTo appends body to up at offset and records the session's new
// size and the time, which keeps a session that is still sending from
// being reaped.
func (s *Service) appendTo(
	ctx context.Context,
	u store.Upload,
	up blob.Upload,
	offset int64,
	body io.Reader,
) error {
	if offset >= 0 && offset != up.Size() {
		return ErrOffset
	}
	if _, err := up.Append(ctx, body); err != nil {
		return fmt.Errorf("registry: append to session %s: %w", u.ID, err)
	}
	u.Bytes, u.Updated = up.Size(), s.now()
	if err := s.Store.Uploads().Put(ctx, u); err != nil {
		return fmt.Errorf("registry: save session %s: %w", u.ID, err)
	}
	return nil
}

// Finish appends the last chunk, if body holds one, verifies the content
// against the SHA-256 hex, and stores it as the image's finished upload. A
// mismatch discards the session (ErrDigest).
func (s *Service) Finish(
	ctx context.Context,
	o Owner,
	id string,
	offset int64,
	body io.Reader,
	hex string,
) (types.Item, error) {
	defer s.lock(id)()
	u, up, err := s.open(ctx, o, id)
	if err != nil {
		return types.Item{}, err
	}
	if err := s.appendTo(ctx, u, up, offset, body); err != nil {
		return types.Item{}, err
	}
	info, err := up.Commit(ctx, blob.Digests{SHA256: hex})
	if errors.Is(err, blob.ErrDigestMismatch) {
		// The blob store aborted the upload; the session goes with it.
		return types.Item{}, errors.Join(ErrDigest, s.forget(ctx, id))
	}
	if err != nil {
		return types.Item{}, fmt.Errorf("registry: commit session %s: %w", id, err)
	}
	// The upload is committed and cannot be resumed, so the session ends
	// whether or not the item is stored; content left unreferenced is
	// garbage collection's.
	stored, err := s.store(ctx, o, hex, info)
	return stored, errors.Join(err, s.forget(ctx, id))
}

// Cancel discards session id.
func (s *Service) Cancel(ctx context.Context, o Owner, id string) error {
	defer s.lock(id)()
	_, up, err := s.open(ctx, o, id)
	if err != nil {
		return err
	}
	if err := up.Abort(ctx); err != nil {
		return fmt.Errorf("registry: abort session %s: %w", id, err)
	}
	return s.forget(ctx, id)
}

// Upload stores body as a finished upload in one request, a monolithic
// upload, verified against the SHA-256 hex.
func (s *Service) Upload(
	ctx context.Context,
	o Owner,
	body io.Reader,
	hex string,
) (types.Item, error) {
	up, err := s.Blobs.BeginUpload(ctx, blob.WithExpectedSHA256(hex))
	if err != nil {
		return types.Item{}, fmt.Errorf("registry: begin upload: %w", err)
	}
	info, err := commit(ctx, up, body, hex)
	if err != nil {
		return types.Item{}, err
	}
	return s.store(ctx, o, hex, info)
}

// commit appends body to up and commits it against hex, aborting up on any
// failure.
func commit(ctx context.Context, up blob.Upload, body io.Reader, hex string) (blob.Info, error) {
	_, err := up.Append(ctx, body)
	var info blob.Info
	if err == nil {
		info, err = up.Commit(ctx, blob.Digests{SHA256: hex})
		if errors.Is(err, blob.ErrDigestMismatch) {
			return info, ErrDigest
		}
	}
	if err != nil {
		_ = up.Abort(context.WithoutCancel(ctx))
		return info, fmt.Errorf("registry: upload: %w", err)
	}
	return info, nil
}

// store records committed content as o's finished upload of hex.
func (s *Service) store(
	ctx context.Context,
	o Owner,
	hex string,
	info blob.Info,
) (types.Item, error) {
	it, err := s.items().DeployCommitted(ctx, uploadDeploy(o, hex), info)
	if err != nil {
		return types.Item{}, fmt.Errorf("registry: store upload: %w", err)
	}
	return it, nil
}

func uploadDeploy(o Owner, hex string) item.Deploy {
	return item.Deploy{
		Target:   item.Target{Repo: o.Repo, Path: layout.UploadPath(o.Name, hex)},
		Declared: types.Checksums{SHA256: hex}, By: o.By,
	}
}

// Mount links the blob hex from the image srcName in src into o's image as
// a finished upload, by reference to the stored content: no bytes are
// copied. ok is false when the source image does not hold the blob, or its
// content went meanwhile, and the registry then opens a session instead.
// The caller checks that o may read the source.
func (s *Service) Mount(
	ctx context.Context,
	o Owner,
	src repo.Config,
	srcName, hex string,
) (types.Item, bool, error) {
	_, err := layout.FindBlob(ctx, s.Store.Items(), src.Key, srcName, hex)
	if errors.Is(err, store.ErrNotFound) {
		return types.Item{}, false, nil
	}
	if err != nil {
		return types.Item{}, false, err //nolint:wrapcheck // the layout names itself
	}
	it, err := s.items().DeployByChecksum(ctx, uploadDeploy(o, hex))
	if errors.Is(err, item.ErrNoBlob) {
		return types.Item{}, false, nil
	}
	if err != nil {
		return types.Item{}, false, fmt.Errorf("registry: mount: %w", err)
	}
	return it, true, nil
}

// Reap discards a stale session for the garbage collector: its blob store
// upload, when that still exists, then its row.
func (s *Service) Reap(ctx context.Context, u store.Upload) error {
	var st state
	if json.Unmarshal(u.State, &st) == nil && st.Blob != "" {
		if up, err := s.Blobs.ResumeUpload(ctx, st.Blob); err == nil {
			if err := up.Abort(ctx); err != nil {
				return fmt.Errorf("registry: abort session %s: %w", u.ID, err)
			}
		}
	}
	return s.forget(ctx, u.ID)
}

// expirePage is how many finished uploads ExpireUploads reads at a time.
const expirePage = 500

// ExpireUploads removes the finished uploads last written before before
// from every repository that holds images, and returns how many it
// removed. A manifest push links the blobs it references into its own
// folder, so an upload no manifest took by then was abandoned (decision
// 0031). They bypass the trash can: nobody deployed them as files.
func (s *Service) ExpireUploads(ctx context.Context, before time.Time) (int, error) {
	all, err := s.Store.Repos().List(ctx, store.RepoFilter{})
	if err != nil {
		return 0, fmt.Errorf("registry: list repositories: %w", err)
	}
	configs := map[string]repo.Config{}
	var refs []aql.RepoRef
	for _, c := range all {
		if layout.Serves(c.PackageType) {
			configs[c.Key] = c
			refs = append(refs, aql.RepoRef{Key: c.Key})
		}
	}
	if len(refs) == 0 {
		return 0, nil
	}
	q := aql.Query{Criteria: aql.And{
		aql.RepoSet{Refs: refs},
		&aql.FieldCriterion{Field: aql.FieldType, Op: aql.OpEq, Text: "file"},
		&aql.FieldCriterion{Field: aql.FieldName, Op: aql.OpMatch, Text: layout.BlobFile("*")},
		&aql.FieldCriterion{
			Field: aql.FieldPath,
			Op:    aql.OpMatch,
			Text:  "*/" + layout.UploadsFolder,
		},
		&aql.FieldCriterion{Field: aql.FieldModified, Op: aql.OpLt, Time: before},
	}, Limit: expirePage, HasLimit: true}
	removed := 0
	for {
		hits, err := s.Store.Items().Search(ctx, &q, aql.Options{Now: s.now()})
		if err != nil {
			return removed, fmt.Errorf("registry: find expired uploads: %w", err)
		}
		// Each page is removed, so the next query starts over.
		for _, h := range hits {
			it := h.Item
			_, err := s.items().
				Purge(ctx, item.Target{Repo: configs[it.RepoKey], Path: it.FullPath()})
			if err != nil && !errors.Is(err, store.ErrNotFound) {
				return removed, fmt.Errorf("registry: expire %s: %w", it.FullPath(), err)
			}
			removed++
		}
		if len(hits) < expirePage {
			return removed, nil
		}
	}
}
