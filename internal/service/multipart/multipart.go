// Package multipart serves the JFrog CLI's multipart upload protocol, as the
// pinned jfrog-client-go drives it (artifactory/services/utils/
// multipartupload.go): create a session, get a URL per part, PUT the parts
// concurrently, complete with the file's SHA-1, poll the status, and abort on
// failure. Once the status is FINISHED the CLI checksum-deploys the file, so a
// session only has to store the content and index it; the item, its
// properties and the package index come from that ordinary checksum deploy.
//
// Artifactory hands out presigned S3 URLs and the object store assembles the
// parts. artifactweave serves the part URLs itself and keeps one blob.Store
// upload session per multipart upload (decision 0004), so every backend,
// mirror included, takes multipart uploads: parts are staged on local disk as
// they arrive, in any order, and appended to the blob upload in order. Only
// the parts that arrive ahead of a missing one wait on disk, a few times the
// part size with the CLI's concurrency.
package multipart

import (
	"context"
	"crypto/hmac"
	"crypto/rand"
	"crypto/sha256"
	"crypto/subtle"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"io/fs"
	"os"
	"path/filepath"
	"regexp"
	"strconv"
	"strings"
	"sync"
	"time"

	"github.com/weaveplatform/artifactweave/pkg/blob"
	"github.com/weaveplatform/artifactweave/pkg/store"
	"github.com/weaveplatform/artifactweave/pkg/types"
	"github.com/weaveplatform/artifactweave/pkg/types/wire"
)

// Backend names multipart sessions in the uploads table, so the garbage
// collector hands them to Reap rather than to the blob store.
const Backend = "jfrog-multipart"

// Statuses the CLI's status poll understands.
const (
	StatusParts      = "PARTS"
	StatusProcessing = "PROCESSING"
	StatusFinished   = "FINISHED"
	StatusRetryable  = "RETRYABLE_ERROR"
	StatusFailed     = "NON_RETRYABLE_ERROR"
	StatusAborted    = "ABORTED"
)

const (
	// MaxPartSize bounds partSizeMB: S3's largest part, which Artifactory
	// passes the size to.
	MaxPartSize = 5 << 30
	// maxParts bounds part numbers: 5 TiB, the CLI's largest multipart file,
	// in 1 MiB parts.
	maxParts = 5 << 20
)

var (
	// ErrBadToken reports a token or part signature that does not verify.
	ErrBadToken = errors.New("multipart: invalid upload token")
	// ErrInvalid reports a malformed request.
	ErrInvalid = errors.New("multipart: invalid request")
	// ErrState reports a request the session's state does not allow, such as
	// a part after completion started.
	ErrState = errors.New("multipart: upload is not accepting this request")
	// ErrCorrupt reports a blob upload holding bytes its session cannot
	// account for.
	ErrCorrupt = errors.New("multipart: the blob upload and its session disagree")
)

// Status is the answer to a status poll.
type Status = wire.MultipartUploadStatus

// session is a multipart upload's state, kept as the uploads row's State.
type session struct {
	// Verifier is the SHA-256 of the token's secret half; the token itself
	// is never stored.
	Verifier string `json:"verifier"`
	// PartKey signs part URLs, which the CLI PUTs without credentials.
	PartKey  string `json:"part_key"`
	Repo     string `json:"repo"`
	Path     string `json:"path"`
	PartSize int64  `json:"part_size"`
	By       string `json:"by"`
	// Blob is the blob store upload the parts are appended to.
	Blob string `json:"blob,omitempty"`
	// Next is how many parts are appended; part Next+1 is the next one.
	Next int `json:"next"`
	// Max is the highest part number received.
	Max int `json:"max"`
	// Short is the first appended part smaller than PartSize, which must be
	// the last.
	Short  int    `json:"short,omitempty"`
	Status string `json:"status"`
	Error  string `json:"error,omitempty"`
	SHA1   string `json:"sha1,omitempty"`
	SHA256 string `json:"sha256,omitempty"`
}

// Service runs multipart uploads over a metadata store and a blob store.
// Dir holds the staged parts, a directory per upload.
type Service struct {
	Store store.Store
	Blobs blob.Store
	Dir   string
	// Now supplies the time; nil means time.Now.
	Now func() time.Time

	// locks serialise the requests of one upload in this process, striped
	// by upload id so the set stays bounded however many uploads run.
	locks [64]sync.Mutex
	mu    sync.Mutex
	// running holds the progress (percent) of completions running in this
	// process. A PROCESSING session not here was interrupted by a restart.
	running map[string]int
}

func (s *Service) now() time.Time {
	if s.Now != nil {
		return s.Now()
	}
	return time.Now()
}

// lock takes the stripe of upload id's lock.
func (s *Service) lock(id string) func() {
	sum := sha256.Sum256([]byte(id))
	l := &s.locks[int(sum[0])%len(s.locks)]
	l.Lock()
	return l.Unlock
}

func (s *Service) dir(id string) string { return filepath.Join(s.Dir, id) }

func (s *Service) partFile(id string, n int) string {
	return filepath.Join(s.dir(id), fmt.Sprintf("part-%08d", n))
}

func random(n int) (string, error) {
	b := make([]byte, n)
	if _, err := rand.Read(b); err != nil {
		return "", fmt.Errorf("multipart: random: %w", err)
	}
	return hex.EncodeToString(b), nil
}

func hash(s string) string {
	sum := sha256.Sum256([]byte(s))
	return hex.EncodeToString(sum[:])
}

// Create starts an upload of repoPath in repo, whose caller by is already
// authorised to deploy there, and returns its token: "<id>.<secret>".
func (s *Service) Create(
	ctx context.Context,
	repo, repoPath string,
	partSizeMB int64,
	by string,
) (string, error) {
	if repo == "" || strings.Trim(repoPath, "/") == "" {
		return "", fmt.Errorf("%w: repoKey and repoPath are required", ErrInvalid)
	}
	if partSizeMB < 1 || partSizeMB<<20 > MaxPartSize {
		return "", fmt.Errorf("%w: partSizeMB %d must be between 1 and %d", ErrInvalid,
			partSizeMB, MaxPartSize>>20)
	}
	id, err := blob.NewUploadID()
	if err != nil {
		return "", fmt.Errorf("multipart: %w", err)
	}
	secret, err := random(32)
	if err != nil {
		return "", err
	}
	key, err := random(32)
	if err != nil {
		return "", err
	}
	if err := os.MkdirAll(s.dir(id), 0o700); err != nil {
		return "", fmt.Errorf("multipart: staging: %w", err)
	}
	ss := session{
		Verifier: hash(secret), PartKey: key, Repo: repo, Path: strings.Trim(repoPath, "/"),
		PartSize: partSizeMB << 20, By: by, Status: StatusParts,
	}
	if err := s.save(ctx, id, ss, s.now(), 0); err != nil {
		return "", err
	}
	return id + "." + secret, nil
}

func (s *Service) save(
	ctx context.Context,
	id string,
	ss session,
	started time.Time,
	bytes int64,
) error {
	state, err := json.Marshal(ss)
	if err != nil {
		return fmt.Errorf("multipart: encode session: %w", err)
	}
	if err := s.Store.Uploads().Put(ctx, store.Upload{
		ID: id, Backend: Backend, RepoKey: ss.Repo, Path: ss.Path, State: state,
		Bytes: bytes, Started: started, Updated: s.now(),
	}); err != nil {
		return fmt.Errorf("multipart: save session: %w", err)
	}
	return nil
}

// load returns the session with id. A row of another backend is not ours.
func (s *Service) load(ctx context.Context, id string) (store.Upload, session, error) {
	u, err := s.Store.Uploads().Get(ctx, id)
	if errors.Is(err, store.ErrNotFound) {
		return u, session{}, ErrBadToken
	}
	if err != nil {
		return u, session{}, fmt.Errorf("multipart: load session: %w", err)
	}
	var ss session
	if u.Backend != Backend || json.Unmarshal(u.State, &ss) != nil {
		return u, session{}, ErrBadToken
	}
	return u, ss, nil
}

// authenticate returns the session a token opens.
func (s *Service) authenticate(ctx context.Context, token string) (store.Upload, session, error) {
	id, secret, ok := strings.Cut(token, ".")
	if !ok || id == "" || secret == "" {
		return store.Upload{}, session{}, ErrBadToken
	}
	u, ss, err := s.load(ctx, id)
	if err != nil {
		return u, ss, err
	}
	if subtle.ConstantTimeCompare([]byte(hash(secret)), []byte(ss.Verifier)) != 1 {
		return u, ss, ErrBadToken
	}
	return u, ss, nil
}

func signature(key, id string, part int) string {
	k, _ := hex.DecodeString(key)
	m := hmac.New(sha256.New, k)
	_, _ = m.Write([]byte(id + "/" + strconv.Itoa(part)))
	return hex.EncodeToString(m.Sum(nil))
}

// PartURL returns the path and query the CLI PUTs part to, relative to the
// server's base URL. The CLI sends that request without credentials, so the
// URL carries a signature for that upload and part only.
func (s *Service) PartURL(ctx context.Context, token string, part int) (string, error) {
	u, ss, err := s.authenticate(ctx, token)
	if err != nil {
		return "", err
	}
	if part < 1 || part > maxParts {
		return "", fmt.Errorf("%w: partNumber %d", ErrInvalid, part)
	}
	if ss.Status != StatusParts {
		return "", fmt.Errorf("%w: status %s", ErrState, ss.Status)
	}
	return fmt.Sprintf("/artifactory/api/v1/uploads/part?upload=%s&partNumber=%d&signature=%s",
		u.ID, part, signature(ss.PartKey, u.ID, part)), nil
}

// PutPart stages part of upload id from body and appends every part that is
// now next in line to the blob upload.
func (s *Service) PutPart(
	ctx context.Context,
	id string,
	part int,
	sig string,
	body io.Reader,
) error {
	_, ss, err := s.load(ctx, id)
	if err != nil {
		return err
	}
	if part < 1 || part > maxParts ||
		subtle.ConstantTimeCompare([]byte(sig), []byte(signature(ss.PartKey, id, part))) != 1 {
		return ErrBadToken
	}
	if ss.Status != StatusParts {
		return fmt.Errorf("%w: status %s", ErrState, ss.Status)
	}
	if part <= ss.Next {
		// A retry of a part already appended: its bytes are in the upload.
		_, _ = io.Copy(io.Discard, body)
		return nil
	}
	if err := s.stage(id, part, ss.PartSize, body); err != nil {
		return err
	}
	unlock := s.lock(id)
	defer unlock()
	u, ss, err := s.load(ctx, id)
	if err != nil {
		return err
	}
	ss.Max = max(ss.Max, part)
	return s.drain(ctx, &u, &ss)
}

// stage writes a part to its file through a temporary name, so a part is
// either whole or absent.
func (s *Service) stage(id string, part int, size int64, body io.Reader) error {
	if err := os.MkdirAll(s.dir(id), 0o700); err != nil {
		return fmt.Errorf("multipart: staging: %w", err)
	}
	f, err := os.CreateTemp(s.dir(id), fmt.Sprintf("part-%08d-*.tmp", part))
	if err != nil {
		return fmt.Errorf("multipart: staging: %w", err)
	}
	n, err := io.Copy(f, io.LimitReader(body, size+1))
	if cerr := f.Close(); err == nil {
		err = cerr
	}
	switch {
	case err != nil:
		_ = os.Remove(f.Name())
		return fmt.Errorf("multipart: part %d: %w", part, err)
	case n > size:
		_ = os.Remove(f.Name())
		return fmt.Errorf("%w: part %d is larger than the part size %d", ErrInvalid, part, size)
	case n == 0:
		_ = os.Remove(f.Name())
		return fmt.Errorf("%w: part %d is empty", ErrInvalid, part)
	}
	if err := os.Rename(f.Name(), s.partFile(id, part)); err != nil {
		return fmt.Errorf("multipart: staging: %w", err)
	}
	return nil
}

// drain appends the staged parts that follow the appended ones, in order,
// and saves the session. The caller holds the upload's lock.
//
// A part's file is removed only after the session records the part, and the
// blob upload's own size says how much of the next part an earlier append
// stored before failing or before the process stopped. That much of the file
// is skipped, so a part is never appended twice.
func (s *Service) drain(ctx context.Context, u *store.Upload, ss *session) error {
	var up blob.Upload
	for ss.Status != StatusFailed {
		name := s.partFile(u.ID, ss.Next+1)
		f, err := os.Open(name) //nolint:gosec // a path under Dir built from our own id
		if errors.Is(err, fs.ErrNotExist) {
			break
		}
		if err != nil {
			return fmt.Errorf("multipart: open part: %w", err)
		}
		n, err := s.appendPart(ctx, u, ss, &up, f)
		_ = f.Close()
		if err != nil {
			return err
		}
		if ss.Short != 0 {
			ss.Status, ss.Error = StatusFailed, fmt.Sprintf(
				"part %d is smaller than the part size but is not the last", ss.Short)
		}
		ss.Next++
		u.Bytes += n
		if n < ss.PartSize && ss.Short == 0 {
			ss.Short = ss.Next
		}
		if err := s.save(ctx, u.ID, *ss, u.Started, u.Bytes); err != nil {
			return err
		}
		_ = os.Remove(name)
		s.progress(u.ID, ss)
	}
	return s.save(ctx, u.ID, *ss, u.Started, u.Bytes)
}

// appendPart appends the rest of part file f to the blob upload, beginning
// or resuming it in *up, and returns the part's size.
func (s *Service) appendPart(
	ctx context.Context,
	u *store.Upload,
	ss *session,
	up *blob.Upload,
	f *os.File,
) (int64, error) {
	info, err := f.Stat()
	if err != nil {
		return 0, fmt.Errorf("multipart: part size: %w", err)
	}
	if *up == nil {
		begun := ss.Blob == ""
		if *up, err = s.blobUpload(ctx, ss); err != nil {
			return 0, err
		}
		if begun {
			// Saved before any byte goes in, so a failed append cannot leave
			// a blob upload no session names.
			if err := s.save(ctx, u.ID, *ss, u.Started, u.Bytes); err != nil {
				return 0, err
			}
		}
	}
	done := (*up).Size() - u.Bytes
	if done < 0 || done > info.Size() {
		return 0, fmt.Errorf("%w: the blob upload holds %d bytes, the session %d",
			ErrCorrupt, (*up).Size(), u.Bytes)
	}
	if _, err := f.Seek(done, io.SeekStart); err != nil {
		return 0, fmt.Errorf("multipart: seek part: %w", err)
	}
	if _, err := (*up).Append(ctx, f); err != nil {
		return 0, fmt.Errorf("multipart: append part %d: %w", ss.Next+1, err)
	}
	return info.Size(), nil
}

// progress records, for a completion running in this process, the share of
// the received parts appended so far.
func (s *Service) progress(id string, ss *session) {
	s.mu.Lock()
	defer s.mu.Unlock()
	if _, ok := s.running[id]; ok && ss.Max > 0 {
		s.running[id] = min(99, ss.Next*100/ss.Max)
	}
}

// blobUpload resumes the session's blob upload, or begins one.
func (s *Service) blobUpload(ctx context.Context, ss *session) (blob.Upload, error) {
	if ss.Blob != "" {
		up, err := s.Blobs.ResumeUpload(ctx, ss.Blob)
		if err != nil {
			return nil, fmt.Errorf("multipart: resume blob upload: %w", err)
		}
		return up, nil
	}
	up, err := s.Blobs.BeginUpload(ctx)
	if err != nil {
		return nil, fmt.Errorf("multipart: begin blob upload: %w", err)
	}
	ss.Blob = up.ID()
	return up, nil
}

var sha1Hex = regexp.MustCompile(`^[0-9a-fA-F]{40}$`)

// Complete starts assembling the upload, checked against sha1, and returns
// at once: the CLI polls Status. Completing an upload that is already
// finished or being completed in this process changes nothing.
func (s *Service) Complete(ctx context.Context, token, sha1 string) error {
	u, ss, err := s.authenticate(ctx, token)
	if err != nil {
		return err
	}
	if !sha1Hex.MatchString(sha1) {
		return fmt.Errorf("%w: sha1 %q", ErrInvalid, sha1)
	}
	unlock := s.lock(u.ID)
	defer unlock()
	if u, ss, err = s.load(ctx, u.ID); err != nil {
		return err
	}
	s.mu.Lock()
	_, running := s.running[u.ID]
	s.mu.Unlock()
	switch {
	case ss.Status == StatusFinished, running:
		return nil
	case ss.Status == StatusFailed, ss.Status == StatusAborted:
		return fmt.Errorf("%w: status %s", ErrState, ss.Status)
	}
	ss.Status, ss.Error, ss.SHA1 = StatusProcessing, "", strings.ToLower(sha1)
	if err := s.save(ctx, u.ID, ss, u.Started, u.Bytes); err != nil {
		return err
	}
	s.mu.Lock()
	if s.running == nil {
		s.running = map[string]int{}
	}
	s.running[u.ID] = 0
	s.mu.Unlock()
	go s.finish(context.WithoutCancel(ctx), u.ID)
	return nil
}

// finish appends what is left, commits the blob upload and indexes the
// content, recording the outcome in the session.
func (s *Service) finish(ctx context.Context, id string) {
	defer func() {
		s.mu.Lock()
		delete(s.running, id)
		s.mu.Unlock()
	}()
	unlock := s.lock(id)
	defer unlock()
	u, ss, err := s.load(ctx, id)
	if err != nil || ss.Status != StatusProcessing {
		// Aborted, or its session reaped, since Complete started it.
		return
	}
	status, msg := s.assemble(ctx, &u, &ss)
	ss.Status, ss.Error = status, msg
	if status == StatusFinished {
		_ = os.RemoveAll(s.dir(id))
	}
	_ = s.save(ctx, id, ss, u.Started, u.Bytes)
}

// assemble returns the session's next status and its error message.
func (s *Service) assemble(ctx context.Context, u *store.Upload, ss *session) (string, string) {
	if err := s.drain(ctx, u, ss); err != nil {
		return StatusRetryable, err.Error()
	}
	if ss.Status == StatusFailed {
		return StatusFailed, ss.Error
	}
	switch {
	case ss.Next == 0:
		return StatusFailed, "no parts were uploaded"
	case ss.Max > ss.Next:
		return StatusFailed, fmt.Sprintf("part %d of %d is missing", ss.Next+1, ss.Max)
	}
	up, err := s.Blobs.ResumeUpload(ctx, ss.Blob)
	if err != nil {
		return StatusFailed, "the stored parts are gone: " + err.Error()
	}
	info, err := up.Commit(ctx, blob.Digests{SHA1: ss.SHA1})
	if errors.Is(err, blob.ErrDigestMismatch) {
		_ = up.Abort(ctx)
		return StatusFailed, "the uploaded parts do not match the SHA-1 " + ss.SHA1
	}
	if err != nil {
		return StatusRetryable, err.Error()
	}
	// Indexed with no references: the CLI's checksum deploy, which follows
	// at once, adds the item's. Until then the content is as any other
	// unreferenced blob, kept for the collector's grace period.
	_, err = s.Store.Blobs().Ref(ctx, types.Blob{
		Checksums: types.Checksums{
			SHA256: info.Digests.SHA256, SHA512: info.Digests.SHA512,
			SHA1: info.Digests.SHA1, MD5: info.Digests.MD5,
		},
		Size: info.Size, Created: s.now(),
	}, 0)
	if err != nil {
		return StatusFailed, "index the uploaded content: " + err.Error()
	}
	ss.SHA256 = info.Digests.SHA256
	return StatusFinished, ""
}

// Status reports an upload's state. A completion this process is not
// running was interrupted by a restart: the CLI is told to complete again.
func (s *Service) Status(ctx context.Context, token string) (Status, error) {
	id, _, _ := strings.Cut(token, ".")
	// Read before the session: a completion that ends in between has
	// already saved its outcome, which the session then shows.
	s.mu.Lock()
	progress, running := s.running[id]
	s.mu.Unlock()
	u, ss, err := s.authenticate(ctx, token)
	if err != nil {
		return Status{}, err
	}
	out := Status{Status: ss.Status, Error: ss.Error}
	switch {
	case ss.Status == StatusFinished:
		done := 100
		out.Progress, out.ChecksumToken = &done, u.ID
	case ss.Status != StatusProcessing:
	case running:
		out.Progress = &progress
	default:
		out.Status, out.Error = StatusRetryable, "the completion was interrupted"
	}
	return out, nil
}

// Abort discards an upload: its staged parts, its blob upload and its
// session. Content already completed stays stored.
func (s *Service) Abort(ctx context.Context, token string) error {
	u, _, err := s.authenticate(ctx, token)
	if err != nil {
		return err
	}
	unlock := s.lock(u.ID)
	defer unlock()
	return s.discard(ctx, u)
}

func (s *Service) discard(ctx context.Context, u store.Upload) error {
	var ss session
	_ = json.Unmarshal(u.State, &ss)
	if ss.Blob != "" && ss.Status != StatusFinished {
		if up, err := s.Blobs.ResumeUpload(ctx, ss.Blob); err == nil {
			_ = up.Abort(ctx)
		}
	}
	if err := os.RemoveAll(s.dir(u.ID)); err != nil {
		return fmt.Errorf("multipart: remove staged parts: %w", err)
	}
	if err := s.Store.Uploads().
		Delete(ctx, u.ID); err != nil &&
		!errors.Is(err, store.ErrNotFound) {
		return fmt.Errorf("multipart: drop session: %w", err)
	}
	return nil
}

// Reap discards a stale session for the garbage collector, which found it
// in the uploads table under Backend.
func (s *Service) Reap(ctx context.Context, u store.Upload) error {
	unlock := s.lock(u.ID)
	defer unlock()
	return s.discard(ctx, u)
}

// Sweep removes staging directories whose session is gone, which a crash
// between dropping a session and its parts leaves behind. The runtime runs
// it at startup.
func (s *Service) Sweep(ctx context.Context) error {
	// Stat first: on Windows, reading a regular file as a directory reports
	// "not found", which would pass a misconfigured staging path silently.
	info, err := os.Stat(s.Dir)
	if errors.Is(err, fs.ErrNotExist) {
		return nil
	}
	if err == nil && !info.IsDir() {
		err = fmt.Errorf("%w: %s is not a directory", fs.ErrInvalid, s.Dir)
	}
	if err != nil {
		return fmt.Errorf("multipart: sweep: %w", err)
	}
	entries, err := os.ReadDir(s.Dir)
	if err != nil {
		return fmt.Errorf("multipart: sweep: %w", err)
	}
	for _, e := range entries {
		_, err := s.Store.Uploads().Get(ctx, e.Name())
		switch {
		case errors.Is(err, store.ErrNotFound):
			if err := os.RemoveAll(filepath.Join(s.Dir, e.Name())); err != nil {
				return fmt.Errorf("multipart: sweep %s: %w", e.Name(), err)
			}
		case err != nil:
			return fmt.Errorf("multipart: sweep %s: %w", e.Name(), err)
		}
	}
	return nil
}
