package mirror

import (
	"bytes"
	"context"
	"errors"
	"fmt"
	"io"
	"time"

	"github.com/weaveplatform/artifactweave/pkg/blob"
	"github.com/weaveplatform/artifactweave/pkg/blob/session"
	"github.com/weaveplatform/artifactweave/pkg/types"
)

// Target states in a session.
const (
	targetActive    = "active"
	targetFailed    = "failed"
	targetCommitted = "committed"
)

// mirrorState is the mirror's part of a session: which members it writes
// to and how each stands. Bytes are not kept here; each member buffers in
// its own session. The json tags are the storage format.
type mirrorState struct {
	Targets []target `json:"targets"`
	// Deferred names members that should hold a copy but were unhealthy at
	// the start; they get pending replica records at commit.
	Deferred []string `json:"deferred,omitempty"`
	// Digests are set once any member committed, so a retried Commit can
	// recognise members that committed before.
	Digests *digests `json:"digests,omitempty"`
}

type target struct {
	Member string `json:"member"`
	Upload string `json:"upload,omitempty"`
	State  string `json:"state"`
	Error  string `json:"error,omitempty"`
}

type digests struct {
	SHA256 string `json:"sha256"`
	SHA512 string `json:"sha512,omitempty"`
	SHA1   string `json:"sha1,omitempty"`
	MD5    string `json:"md5,omitempty"`
}

type upload struct {
	s  *Store
	ss *session.Session[mirrorState]
	// children holds the member upload of every active target.
	children map[string]blob.Upload
}

// BeginUpload starts a session on the members placement chooses.
func (s *Store) BeginUpload(ctx context.Context, opts ...blob.UploadOption) (blob.Upload, error) {
	ss, err := session.Begin[mirrorState](ctx, s.sessions, opts...)
	if err != nil {
		return nil, fmt.Errorf("blob/mirror: %w", err)
	}
	u := &upload{s: s, ss: ss, children: map[string]blob.Upload{}}
	targets, deferred := s.place()
	ups, errs := parallel(len(targets), func(i int) (blob.Upload, error) {
		start := s.now()
		up, err := targets[i].Store.BeginUpload(ctx, opts...)
		s.observe(targets[i], start, err)
		return up, err //nolint:wrapcheck // tagged with the member below
	})
	var failures []error
	for i, m := range targets {
		t := target{Member: m.Name, State: targetActive}
		if errs[i] != nil {
			t.State, t.Error = targetFailed, errs[i].Error()
			failures = append(failures, fmt.Errorf("%s: %w", m.Name, errs[i]))
		} else {
			t.Upload = ups[i].ID()
			u.children[m.Name] = ups[i]
		}
		ss.Data.Targets = append(ss.Data.Targets, t)
	}
	for _, m := range deferred {
		ss.Data.Deferred = append(ss.Data.Deferred, m.Name)
	}
	if len(u.children) < s.quorum {
		_ = u.abortChildren(ctx)
		_ = ss.Forget(ctx)
		return nil, fmt.Errorf("%w: %d of %d members began the upload: %w",
			ErrQuorum, len(u.children), s.quorum, errors.Join(failures...))
	}
	if err := ss.Save(ctx); err != nil {
		_ = u.abortChildren(ctx)
		return nil, fmt.Errorf("blob/mirror: %w", err)
	}
	return u, nil
}

// ResumeUpload reopens a session and each active member's upload. A member
// whose upload cannot be resumed drops out of the session.
func (s *Store) ResumeUpload(ctx context.Context, id string) (blob.Upload, error) {
	ss, err := session.Resume[mirrorState](ctx, s.sessions, id)
	if err != nil {
		return nil, fmt.Errorf("blob/mirror: %w", err)
	}
	u := &upload{s: s, ss: ss, children: map[string]blob.Upload{}}
	active := u.indexes(targetActive)
	ups, errs := parallel(len(active), func(j int) (blob.Upload, error) {
		t := ss.Data.Targets[active[j]]
		m := s.byName[t.Member]
		if m == nil {
			return nil, fmt.Errorf("%w: member %q is no longer configured", ErrConfig, t.Member)
		}
		start := s.now()
		up, err := m.Store.ResumeUpload(ctx, t.Upload)
		s.observe(m, start, err)
		return up, err //nolint:wrapcheck // recorded on the target below
	})
	changed := false
	for j, i := range active {
		t := &ss.Data.Targets[i]
		switch {
		case errs[j] == nil:
			u.children[t.Member] = ups[j]
		case u.heldAfterLostCommit(ctx, t.Member, errs[j]):
			t.State, t.Error = targetCommitted, ""
			changed = true
		default:
			t.State, t.Error = targetFailed, errs[j].Error()
			changed = true
		}
	}
	if changed {
		if err := ss.Save(ctx); err != nil {
			return nil, fmt.Errorf("blob/mirror: %w", err)
		}
	}
	return u, nil
}

// heldAfterLostCommit reports whether a member whose session is gone holds
// the blob an earlier commit attempt recorded: its commit went through but
// the answer was lost, so it counts as committed.
func (u *upload) heldAfterLostCommit(ctx context.Context, name string, err error) bool {
	d := u.ss.Data.Digests
	m := u.s.byName[name]
	if d == nil || m == nil || !errors.Is(err, blob.ErrUploadUnknown) {
		return false
	}
	ok, serr := m.Store.Exists(ctx, d.SHA256)
	return serr == nil && ok
}

func (u *upload) ID() string  { return u.ss.ID }
func (u *upload) Size() int64 { return u.ss.Size }

// indexes returns the positions of the targets in state.
func (u *upload) indexes(state string) []int {
	var out []int
	for i, t := range u.ss.Data.Targets {
		if t.State == state {
			out = append(out, i)
		}
	}
	return out
}

// eject drops a target from the session.
func (u *upload) eject(i int, err error) {
	t := &u.ss.Data.Targets[i]
	t.State, t.Error = targetFailed, err.Error()
	delete(u.children, t.Member)
}

// Append reads r a chunk at a time and hands each chunk to every active
// member in parallel. A member that fails in a way that leaves it with a
// different size from the mirror's is ejected; the upload fails once fewer
// than WriteQuorum members remain.
func (u *upload) Append(ctx context.Context, r io.Reader) (int64, error) {
	if err := ctx.Err(); err != nil {
		return 0, fmt.Errorf("blob/mirror: append: %w", err)
	}
	if err := u.ss.Live(ctx); err != nil {
		return 0, fmt.Errorf("blob/mirror: %w", err)
	}
	buf := make([]byte, u.s.chunk)
	var total int64
	for {
		n, rerr := io.ReadFull(r, buf)
		if n > 0 {
			if err := u.fanOut(ctx, buf[:n]); err != nil {
				return total, errors.Join(err, u.save(ctx))
			}
			total += int64(n)
			u.ss.Size += int64(n)
		}
		if errors.Is(rerr, io.EOF) || errors.Is(rerr, io.ErrUnexpectedEOF) {
			return total, u.save(ctx)
		}
		if rerr != nil {
			return total, errors.Join(fmt.Errorf("blob/mirror: read: %w", rerr), u.save(ctx))
		}
	}
}

// fanOut appends one chunk to every active member. The chunk is not touched
// again until every member has returned, so all of them may read it at once.
func (u *upload) fanOut(ctx context.Context, chunk []byte) error {
	active := u.indexes(targetActive)
	want := u.ss.Size + int64(len(chunk))
	children := make([]blob.Upload, len(active))
	members := make([]*member, len(active))
	for j, i := range active {
		name := u.ss.Data.Targets[i].Member
		children[j], members[j] = u.children[name], u.s.byName[name]
	}
	_, errs := parallel(len(active), func(j int) (struct{}, error) {
		start := u.s.now()
		_, err := children[j].Append(ctx, bytes.NewReader(chunk))
		u.s.observe(members[j], start, err)
		return struct{}{}, err //nolint:wrapcheck // recorded on the target below
	})
	var failures []error
	for j, i := range active {
		name := u.ss.Data.Targets[i].Member
		switch got := children[j].Size(); {
		case got != want:
			// A member that kept the bytes despite an error (s3 keeps a
			// failed part pending) can continue; one that lost them would
			// write different content, so it leaves the session.
			err := errs[j]
			if err == nil {
				err = fmt.Errorf("stored %d bytes, the mirror %d", got, want)
			}
			u.eject(i, err)
			failures = append(failures, fmt.Errorf("%s: %w", name, err))
		case errs[j] != nil:
			failures = append(failures, fmt.Errorf("%s: %w", name, errs[j]))
		}
	}
	if n := len(u.children); n < u.s.quorum {
		return fmt.Errorf("%w: %d of %d members hold the upload: %w",
			ErrQuorum, n, u.s.quorum, errors.Join(failures...))
	}
	return nil
}

func (u *upload) save(ctx context.Context) error {
	if err := u.ss.Save(ctx); err != nil {
		return fmt.Errorf("blob/mirror: %w", err)
	}
	return nil
}

// memberRun is one member commit with everything it needs captured, so the
// goroutine running it never reads session state the commit loop changes.
type memberRun struct {
	i     int
	name  string
	m     *member
	child blob.Upload
	// known are the digests to report when the member's session is gone but
	// it holds the blob.
	known blob.Digests
}

// result is one member's commit outcome.
type result struct {
	i    int
	info blob.Info
	err  error
}

// Commit commits every active member in parallel and returns once
// WriteQuorum members hold the blob. Members still committing get
// MemberTimeout to finish in the background; every member's outcome is
// recorded in the replica index. Fewer successes leave the session
// resumable so the client may retry the commit.
func (u *upload) Commit(ctx context.Context, want blob.Digests) (blob.Info, error) {
	if err := ctx.Err(); err != nil {
		return blob.Info{}, fmt.Errorf("blob/mirror: commit: %w", err)
	}
	if err := blob.ValidateDigests(want); err != nil {
		return blob.Info{}, err //nolint:wrapcheck // already a blob sentinel
	}
	if err := u.ss.Live(ctx); err != nil {
		return blob.Info{}, fmt.Errorf("blob/mirror: %w", err)
	}
	if want.SHA256 == "" {
		want.SHA256 = u.ss.Expected
	}
	need := u.s.quorum - len(u.indexes(targetCommitted))
	info := u.previousInfo()
	known := info.Digests.SHA256
	if want.SHA256 != "" {
		known = want.SHA256
	}
	runs := make([]memberRun, 0, len(u.children))
	for _, i := range u.indexes(targetActive) {
		name := u.ss.Data.Targets[i].Member
		runs = append(runs, memberRun{
			i: i, name: name, m: u.s.byName[name], child: u.children[name],
			known: u.knownDigests(known),
		})
	}
	results := make(chan result, len(runs))
	cancels := map[int]context.CancelFunc{}
	for _, mr := range runs {
		// Member commits outlive the request: once the quorum is met the
		// rest finish in the background rather than being cancelled.
		mctx, cancel := context.WithCancel(context.WithoutCancel(ctx))
		cancels[mr.i] = cancel
		go func() {
			mi, err := u.s.commitMember(mctx, mr, want)
			results <- result{i: mr.i, info: mi, err: err}
		}()
	}
	outcomes := map[int]error{}
	var failures []error
	for need > 0 && len(outcomes) < len(runs) {
		select {
		case r := <-results:
			cancels[r.i]()
			outcomes[r.i] = r.err
			switch {
			case r.err == nil:
				need--
				u.markCommitted(r.i, r.info)
				if info.Digests.SHA256 == "" {
					info = r.info
				}
			case errors.Is(r.err, blob.ErrDigestMismatch):
				cancelAll(cancels)
				drain(results, len(runs)-len(outcomes))
				_ = u.abortChildren(ctx)
				_ = u.ss.Forget(ctx)
				return blob.Info{}, fmt.Errorf("blob/mirror: %w", r.err)
			default:
				failures = append(
					failures,
					fmt.Errorf("%s: %w", u.ss.Data.Targets[r.i].Member, r.err),
				)
			}
		case <-ctx.Done():
			cancelAll(cancels)
			for _, r := range drain(results, len(runs)-len(outcomes)) {
				if r.err == nil {
					u.markCommitted(r.i, r.info)
				}
			}
			return blob.Info{}, errors.Join(
				fmt.Errorf("blob/mirror: commit: %w", ctx.Err()),
				u.save(ctx),
			)
		}
	}
	if need > 0 {
		// Every member answered and too few succeeded. Members that
		// committed are marked so a retry counts them; what is stored so
		// far is recorded.
		if info.Digests.SHA256 != "" {
			if err := u.record(ctx, info, outcomes, nil); err != nil {
				failures = append(failures, err)
			}
		}
		failures = append(failures, u.save(ctx))
		return blob.Info{}, fmt.Errorf("%w: %d of %d members committed: %w",
			ErrQuorum, u.s.quorum-need, u.s.quorum, errors.Join(failures...))
	}
	var running []memberRun
	for _, mr := range runs {
		if _, done := outcomes[mr.i]; !done {
			running = append(running, mr)
		}
	}
	if err := u.record(ctx, info, outcomes, running); err != nil {
		return blob.Info{}, errors.Join(err, u.save(ctx))
	}
	if err := u.ss.Forget(ctx); err != nil {
		return blob.Info{}, fmt.Errorf("blob/mirror: %w", err)
	}
	if len(running) > 0 {
		done := u.s.track(info.Digests.SHA256)
		go func() {
			defer done()
			u.s.finishStragglers(context.WithoutCancel(ctx), info, results, cancels, running)
		}()
	} else {
		cancelAll(cancels)
	}
	return info, nil
}

// commitMember commits one member. A member whose session is gone but which
// holds the blob committed in an earlier attempt that did not reach the
// quorum; it counts as a success.
func (s *Store) commitMember(
	ctx context.Context,
	mr memberRun,
	want blob.Digests,
) (blob.Info, error) {
	start := s.now()
	info, err := mr.child.Commit(ctx, want)
	s.observe(mr.m, start, err)
	if errors.Is(err, blob.ErrUploadUnknown) && mr.known.SHA256 != "" {
		if st, serr := mr.m.Store.Stat(ctx, mr.known.SHA256); serr == nil {
			return blob.Info{Size: st.Size, Digests: mr.known}, nil
		}
	}
	return info, err //nolint:wrapcheck // tagged with the member by the caller
}

// knownDigests returns the digests an earlier commit recorded for sha, or
// just sha.
func (u *upload) knownDigests(sha string) blob.Digests {
	if d := u.ss.Data.Digests; d != nil && d.SHA256 == sha {
		return blob.Digests{SHA256: d.SHA256, SHA512: d.SHA512, SHA1: d.SHA1, MD5: d.MD5}
	}
	return blob.Digests{SHA256: sha}
}

// previousInfo is the blob an earlier attempt committed, or the zero Info.
func (u *upload) previousInfo() blob.Info {
	d := u.ss.Data.Digests
	if d == nil {
		return blob.Info{}
	}
	return blob.Info{
		Size:    u.ss.Size,
		Digests: blob.Digests{SHA256: d.SHA256, SHA512: d.SHA512, SHA1: d.SHA1, MD5: d.MD5},
	}
}

func (u *upload) markCommitted(i int, info blob.Info) {
	t := &u.ss.Data.Targets[i]
	t.State, t.Error = targetCommitted, ""
	delete(u.children, t.Member)
	if d := u.ss.Data.Digests; d == nil || d.SHA1 == "" {
		u.ss.Data.Digests = &digests{
			SHA256: info.Digests.SHA256,
			SHA512: info.Digests.SHA512,
			SHA1:   info.Digests.SHA1,
			MD5:    info.Digests.MD5,
		}
	}
}

func cancelAll(cancels map[int]context.CancelFunc) {
	for _, c := range cancels {
		c()
	}
}

// drain waits for n more results.
func drain(results <-chan result, n int) []result {
	out := make([]result, 0, n)
	for range n {
		out = append(out, <-results)
	}
	return out
}

// record writes the replica index for a committed blob: present for every
// member that holds it, pending (with the reason) for members that failed,
// were ejected, were deferred or are still committing.
func (u *upload) record(
	ctx context.Context,
	info blob.Info,
	outcomes map[int]error,
	running []memberRun,
) error {
	sha := info.Digests.SHA256
	var errs []error
	put := func(name string, state types.ReplicaState, reason string) {
		r := types.BlobReplica{
			SHA256:    sha,
			Member:    name,
			State:     state,
			Size:      info.Size,
			Updated:   u.s.now(),
			LastError: reason,
		}
		if err := u.s.replicas.Put(ctx, r); err != nil {
			errs = append(errs, fmt.Errorf("blob/mirror: record %s on %s: %w", sha, name, err))
		}
	}
	for i, t := range u.ss.Data.Targets {
		switch {
		case t.State == targetCommitted:
			put(t.Member, types.ReplicaPresent, "")
		case t.State == targetFailed:
			put(t.Member, types.ReplicaPending, t.Error)
		case outcomes[i] != nil:
			put(t.Member, types.ReplicaPending, outcomes[i].Error())
		}
	}
	for _, mr := range running {
		put(mr.name, types.ReplicaPending, "still committing")
	}
	for _, name := range u.ss.Data.Deferred {
		put(name, types.ReplicaPending, "member unavailable at upload")
	}
	return errors.Join(errs...)
}

// finishStragglers waits up to MemberTimeout for members still committing
// after the quorum and records each one's outcome; those that miss it stay
// pending for the repair worker.
func (s *Store) finishStragglers(
	ctx context.Context, info blob.Info, results <-chan result,
	cancels map[int]context.CancelFunc, running []memberRun,
) {
	defer cancelAll(cancels)
	names := map[int]string{}
	for _, mr := range running {
		names[mr.i] = mr.name
	}
	timer := time.NewTimer(s.memberTimeout)
	defer timer.Stop()
	for left := len(running); left > 0; {
		select {
		case r := <-results:
			left--
			rec := types.BlobReplica{
				SHA256: info.Digests.SHA256, Member: names[r.i], State: types.ReplicaPresent,
				Size: info.Size, Updated: s.now(),
			}
			if r.err != nil {
				rec.State, rec.LastError = types.ReplicaPending, r.err.Error()
			}
			// Best effort: the record already says pending, which is what a
			// failed write here leaves behind.
			_ = s.replicas.Put(ctx, rec)
		case <-timer.C:
			// Cancel whoever is left; their results still arrive and are
			// recorded as pending with the cancellation as the reason.
			cancelAll(cancels)
		}
	}
}

// Abort aborts every active member's upload and forgets the session.
func (u *upload) Abort(ctx context.Context) error {
	err := u.abortChildren(ctx)
	if ferr := u.ss.Forget(ctx); ferr != nil {
		err = errors.Join(err, fmt.Errorf("blob/mirror: %w", ferr))
	}
	return err
}

func (u *upload) abortChildren(ctx context.Context) error {
	names := make([]string, 0, len(u.children))
	for name := range u.children {
		names = append(names, name)
	}
	_, errs := parallel(len(names), func(i int) (struct{}, error) {
		return struct{}{}, u.children[names[i]].Abort(ctx) //nolint:wrapcheck // tagged below
	})
	var out []error
	for i, err := range errs {
		if err != nil {
			out = append(out, fmt.Errorf("blob/mirror: abort on %s: %w", names[i], err))
		}
	}
	return errors.Join(out...)
}
