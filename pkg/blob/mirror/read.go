package mirror

import (
	"context"
	"errors"
	"fmt"
	"io"
	"time"

	"github.com/weaveplatform/artifactweave/pkg/blob"
	"github.com/weaveplatform/artifactweave/pkg/store"
	"github.com/weaveplatform/artifactweave/pkg/types"
)

// ErrSeek reports an invalid Seek.
var ErrSeek = errors.New("blob/mirror: invalid seek")

// candidates returns the members to read sha from, in read order, and
// whether they come from probing rather than the index:
//   - members with a present replica record, when there are any;
//   - none, with blob.ErrNotFound, when the only records are tombstones of
//     a delete that some member has not finished;
//   - every member otherwise, which covers blobs written before the mirror
//     existed and blobs whose copies are all still pending.
func (s *Store) candidates(ctx context.Context, sha string) ([]*member, bool, error) {
	recs, err := s.replicas.ForBlob(ctx, sha)
	if err != nil {
		return nil, false, fmt.Errorf("blob/mirror: replicas of %s: %w", sha, err)
	}
	var present []*member
	deleting := false
	for _, r := range recs {
		switch r.State {
		case types.ReplicaPresent:
			if m := s.byName[r.Member]; m != nil {
				present = append(present, m)
			}
		case types.ReplicaDeleting:
			deleting = true
		}
	}
	switch {
	case len(present) > 0:
		return s.readOrder(present), false, nil
	case deleting:
		return nil, false, fmt.Errorf("%w: %s was deleted", blob.ErrNotFound, sha)
	default:
		return s.readOrder(s.members), true, nil
	}
}

// each calls f on members in order until one succeeds. A member answering
// not-found is skipped; the result is blob.ErrNotFound only when every
// member said so, and the joined member errors otherwise, because a member
// that failed might hold the blob.
func (s *Store) each(
	ctx context.Context,
	ms []*member,
	op, sha string,
	f func(m *member) error,
) error {
	var failures []error
	for _, m := range ms {
		start := s.now()
		err := f(m)
		s.observe(m, start, err)
		switch {
		case err == nil:
			return nil
		case errors.Is(err, blob.ErrNotFound):
		case ctx.Err() != nil:
			return fmt.Errorf("blob/mirror: %s %s: %w", op, sha, ctx.Err())
		default:
			failures = append(failures, fmt.Errorf("%s: %w", m.Name, err))
		}
	}
	if len(failures) == 0 {
		return fmt.Errorf("blob/mirror: %s %s: %w", op, sha, blob.ErrNotFound)
	}
	return fmt.Errorf("blob/mirror: %s %s: %w", op, sha, errors.Join(failures...))
}

func (s *Store) check(ctx context.Context, op, sha string) error {
	if err := ctx.Err(); err != nil {
		return fmt.Errorf("blob/mirror: %s: %w", op, err)
	}
	return blob.ValidateSHA256(sha) //nolint:wrapcheck // already a blob sentinel
}

// Stat returns the blob's Info from the first member that holds it.
func (s *Store) Stat(ctx context.Context, sha string) (blob.Info, error) {
	if err := s.check(ctx, "stat", sha); err != nil {
		return blob.Info{}, err
	}
	ms, _, err := s.candidates(ctx, sha)
	if err != nil {
		return blob.Info{}, err
	}
	var info blob.Info
	err = s.each(ctx, ms, "stat", sha, func(m *member) error {
		var err error
		info, err = m.Store.Stat(ctx, sha)
		return err //nolint:wrapcheck // tagged by each
	})
	return info, err
}

// Exists reports whether any member holds the blob.
func (s *Store) Exists(ctx context.Context, sha string) (bool, error) {
	_, err := s.Stat(ctx, sha)
	if errors.Is(err, blob.ErrNotFound) {
		return false, nil
	}
	return err == nil, err
}

// Open returns a reader that serves the blob from the first member that
// holds it and moves to the next one, at the same offset, if a read fails.
func (s *Store) Open(ctx context.Context, sha string) (io.ReadSeekCloser, blob.Info, error) {
	if err := s.check(ctx, "open", sha); err != nil {
		return nil, blob.Info{}, err
	}
	ms, _, err := s.candidates(ctx, sha)
	if err != nil {
		return nil, blob.Info{}, err
	}
	r := &reader{ctx: ctx, s: s, sha: sha}
	var info blob.Info
	err = s.each(ctx, ms, "open", sha, func(m *member) error {
		rc, i, err := m.Store.Open(ctx, sha)
		if err != nil {
			return err //nolint:wrapcheck // tagged by each
		}
		r.rc, r.cur, info = rc, m, i
		return nil
	})
	if err != nil {
		return nil, blob.Info{}, err
	}
	r.size = info.Size
	for _, m := range ms {
		if m != r.cur {
			r.rest = append(r.rest, m)
		}
	}
	return r, info, nil
}

// reader reads from one member and fails over to the next on a read error.
// Blobs are immutable, so any holder serves the same bytes at any offset.
type reader struct {
	ctx  context.Context //nolint:containedctx // io.Reader has no context parameter
	s    *Store
	sha  string
	size int64
	off  int64
	rc   io.ReadSeekCloser
	cur  *member
	rest []*member
	last error
}

func (r *reader) Read(p []byte) (int, error) {
	if r.off >= r.size {
		return 0, io.EOF
	}
	for {
		if r.rc == nil {
			if err := r.failover(); err != nil {
				return 0, err
			}
		}
		n, err := r.rc.Read(p)
		r.off += int64(n)
		switch {
		case err == nil:
			return n, nil
		case errors.Is(err, io.EOF) && r.off >= r.size:
			return n, io.EOF
		case errors.Is(err, io.EOF):
			err = fmt.Errorf("%w at %d of %d", io.ErrUnexpectedEOF, r.off, r.size)
		}
		// The member failed part-way; drop it and continue from r.off on
		// the next one.
		r.s.observe(r.cur, r.s.now(), err)
		r.last = fmt.Errorf("%s: %w", r.cur.Name, err)
		_ = r.rc.Close()
		r.rc = nil
		if n > 0 {
			return n, nil
		}
	}
}

// failover opens the next member and seeks it to the current offset.
func (r *reader) failover() error {
	for len(r.rest) > 0 {
		m := r.rest[0]
		r.rest = r.rest[1:]
		start := r.s.now()
		rc, _, err := m.Store.Open(r.ctx, r.sha)
		if err == nil {
			if _, err = rc.Seek(r.off, io.SeekStart); err != nil {
				_ = rc.Close()
			}
		}
		r.s.observe(m, start, err)
		if err != nil {
			r.last = fmt.Errorf("%s: %w", m.Name, err)
			continue
		}
		r.rc, r.cur = rc, m
		return nil
	}
	return fmt.Errorf("blob/mirror: read %s at %d: no member left: %w", r.sha, r.off, r.last)
}

func (r *reader) Seek(offset int64, whence int) (int64, error) {
	var pos int64
	switch whence {
	case io.SeekStart:
		pos = offset
	case io.SeekCurrent:
		pos = r.off + offset
	case io.SeekEnd:
		pos = r.size + offset
	default:
		return r.off, fmt.Errorf("%w: whence %d", ErrSeek, whence)
	}
	if pos < 0 {
		return r.off, fmt.Errorf("%w: negative position %d", ErrSeek, pos)
	}
	if r.rc != nil {
		if _, err := r.rc.Seek(pos, io.SeekStart); err != nil {
			// Reopen elsewhere on the next read rather than fail the seek.
			r.last = fmt.Errorf("%s: %w", r.cur.Name, err)
			_ = r.rc.Close()
			r.rc = nil
		}
	}
	r.off = pos
	return pos, nil
}

func (r *reader) Close() error {
	if r.rc == nil {
		return nil
	}
	err := r.rc.Close()
	r.rc = nil
	if err != nil {
		return fmt.Errorf("blob/mirror: close %s: %w", r.sha, err)
	}
	return nil
}

// Delete removes the blob from every member. A member that cannot be
// reached keeps a deleting record, a tombstone the repair worker acts on
// later; reads honour it, so the blob is gone for every reader as soon as
// Delete returns.
func (s *Store) Delete(ctx context.Context, sha string) error {
	if err := s.check(ctx, "delete", sha); err != nil {
		return err
	}
	if err := s.settle(ctx, sha); err != nil {
		return err
	}
	_, errs := parallel(len(s.members), func(i int) (struct{}, error) {
		m := s.members[i]
		start := s.now()
		err := m.Store.Delete(ctx, sha)
		s.observe(m, start, err)
		return struct{}{}, err //nolint:wrapcheck // recorded below
	})
	var failed []int
	for i, err := range errs {
		if err != nil {
			failed = append(failed, i)
		}
	}
	if len(failed) == 0 {
		if _, err := s.replicas.DeleteBlob(ctx, sha); err != nil {
			return fmt.Errorf("blob/mirror: delete %s: %w", sha, err)
		}
		return nil
	}
	var out []error
	for _, i := range failed {
		r := types.BlobReplica{
			SHA256: sha, Member: s.members[i].Name, State: types.ReplicaDeleting,
			Updated: s.now(), LastError: errs[i].Error(),
		}
		if err := s.replicas.Put(ctx, r); err != nil {
			out = append(out, fmt.Errorf("blob/mirror: tombstone %s on %s: %w", sha, r.Member, err))
		}
	}
	for i, m := range s.members {
		if errs[i] == nil {
			if err := s.replicas.Delete(ctx, sha, m.Name); err != nil && !isNotFound(err) {
				out = append(
					out,
					fmt.Errorf("blob/mirror: delete %s record on %s: %w", sha, m.Name, err),
				)
			}
		}
	}
	return errors.Join(out...)
}

// Walk visits every blob any member holds, once, and stops at the first
// error fn or a member returns. A member that cannot be listed fails the
// walk rather than shortening it, because garbage collection must never act
// on a partial listing.
func (s *Store) Walk(ctx context.Context, fn func(blob.Info) error) error {
	if err := ctx.Err(); err != nil {
		return fmt.Errorf("blob/mirror: walk: %w", err)
	}
	seen := map[string]bool{}
	for _, m := range s.members {
		err := m.Store.Walk(ctx, func(info blob.Info) error {
			if seen[info.Digests.SHA256] {
				return nil
			}
			seen[info.Digests.SHA256] = true
			return fn(info)
		})
		if err != nil {
			return fmt.Errorf("blob/mirror: walk %s: %w", m.Name, err)
		}
	}
	return nil
}

// PresignGet returns a time-limited URL from the first member, in read
// order, that holds the blob and can presign.
func (s *Store) PresignGet(ctx context.Context, sha string, ttl time.Duration) (string, error) {
	if err := s.check(ctx, "presign", sha); err != nil {
		return "", err
	}
	ms, probing, err := s.candidates(ctx, sha)
	if err != nil {
		return "", err
	}
	capable := false
	var url string
	err = s.each(ctx, ms, "presign", sha, func(m *member) error {
		p, ok := m.Store.(blob.Presigner)
		if !ok {
			return fmt.Errorf("presigning: %w", blob.ErrNotFound)
		}
		capable = true
		if probing {
			// Without an index record, only a member that holds the blob
			// may hand out a URL to it.
			if ok, err := m.Store.Exists(ctx, sha); err != nil || !ok {
				return errors.Join(err, blob.ErrNotFound)
			}
		}
		var err error
		url, err = p.PresignGet(ctx, sha, ttl)
		return err //nolint:wrapcheck // tagged by each
	})
	if !capable {
		return "", fmt.Errorf("%w: no member can presign", ErrConfig)
	}
	return url, err
}

func isNotFound(err error) bool { return errors.Is(err, store.ErrNotFound) }
