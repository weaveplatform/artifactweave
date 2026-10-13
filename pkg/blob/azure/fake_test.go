package azure

import (
	"bytes"
	"context"
	"crypto/md5" //nolint:gosec // the content MD5 Azure stores, not a security primitive
	"encoding/base64"
	"errors"
	"fmt"
	"io"
	"sort"
	"strings"
	"sync"
	"time"

	"github.com/weaveplatform/artifactweave/pkg/blob"
)

// errBadRequest models the 400s Azure returns for malformed block IDs and
// ranges.
var errBadRequest = errors.New("fake azure: 400 bad request")

// fakeAzure models the Azure semantics the store depends on, including the
// ones Azurite does not enforce (research 18):
//   - block IDs are standard base64 of at most 64 bytes, all one length per
//     blob;
//   - a blob with only uncommitted blocks does not exist (404);
//   - Put Block List and Put Blob discard uncommitted blocks they do not
//     name; a block list naming an unknown block fails;
//   - If-None-Match: * fails on a committed blob;
//   - Put Block From URL needs an authorised source with the expected ETag;
//   - the uncommitted block count is limited.
//
// failOn makes the Nth and later calls of an operation fail.
type fakeAzure struct {
	mu        sync.Mutex
	blobs     map[string]*fakeBlob
	etags     int
	calls     map[string]int
	fail      map[string]failure
	once      map[string]error
	pageLen   int
	noSource  bool // Put Block From URL sources are not authorised
	noSign    bool // SignRead is not configured
	container bool // the container is missing
}

type failure struct {
	after int
	err   error
}

type fakeBlob struct {
	committed   []byte
	exists      bool
	etag        string
	md5         []byte
	blocks      map[string][]byte // committed blocks by ID
	uncommitted map[string][]byte
	idLen       int
}

// maxUncommitted is Azure's 100,000 uncommitted blocks, lowered for tests.
var maxUncommitted = 100_000

func newFake() *fakeAzure {
	return &fakeAzure{
		blobs: map[string]*fakeBlob{}, calls: map[string]int{}, fail: map[string]failure{},
		once: map[string]error{}, pageLen: 2,
	}
}

func (f *fakeAzure) failOn(op string, after int, err error) {
	f.mu.Lock()
	defer f.mu.Unlock()
	f.fail[op] = failure{after: after, err: err}
	f.calls[op] = 0
}

// failOnce makes only the next call of op fail.
func (f *fakeAzure) failOnce(op string, err error) {
	f.mu.Lock()
	defer f.mu.Unlock()
	f.once[op] = err
}

func (f *fakeAzure) clear(op string) {
	f.mu.Lock()
	defer f.mu.Unlock()
	delete(f.fail, op)
}

func (f *fakeAzure) count(op string) int {
	f.mu.Lock()
	defer f.mu.Unlock()
	return f.calls[op]
}

func (f *fakeAzure) enter(op string) error {
	f.calls[op]++
	if f.container {
		return fmt.Errorf("%w: container not found", ErrConfig)
	}
	if err, ok := f.once[op]; ok {
		delete(f.once, op)
		return err
	}
	if fl, ok := f.fail[op]; ok && f.calls[op] > fl.after {
		return fl.err
	}
	return nil
}

func (f *fakeAzure) blob(name string) *fakeBlob {
	b, ok := f.blobs[name]
	if !ok {
		b = &fakeBlob{blocks: map[string][]byte{}, uncommitted: map[string][]byte{}}
		f.blobs[name] = b
	}
	return b
}

func (f *fakeAzure) nextETag() string {
	f.etags++
	return fmt.Sprintf("\"0x%04d\"", f.etags)
}

func checkBlockID(b *fakeBlob, id string) error {
	raw, err := base64.StdEncoding.DecodeString(id)
	if err != nil || base64.StdEncoding.EncodeToString(raw) != id {
		return fmt.Errorf("%w: InvalidQueryParameterValue %q", errBadRequest, id)
	}
	if len(raw) > 64 {
		return fmt.Errorf("%w: OutOfRangeInput %q", errBadRequest, id)
	}
	if b.idLen != 0 && len(raw) != b.idLen && len(b.uncommitted) > 0 {
		return fmt.Errorf(
			"%w: InvalidBlobOrBlock: id length %d, want %d",
			errBadRequest,
			len(raw),
			b.idLen,
		)
	}
	b.idLen = len(raw)
	return nil
}

func (f *fakeAzure) stage(b *fakeBlob, id string, data []byte) error {
	if err := checkBlockID(b, id); err != nil {
		return err
	}
	if _, ok := b.uncommitted[id]; !ok && len(b.uncommitted) >= maxUncommitted {
		return fmt.Errorf("fake azure: 409 BlockCountExceedsLimit")
	}
	b.uncommitted[id] = bytes.Clone(data)
	return nil
}

func (f *fakeAzure) StageBlock(_ context.Context, name, id string, data []byte) error {
	f.mu.Lock()
	defer f.mu.Unlock()
	if err := f.enter("StageBlock"); err != nil {
		return err
	}
	return f.stage(f.blob(name), id, data)
}

func (f *fakeAzure) StageBlockFromURL(
	_ context.Context, dst, id, src string, offset, count int64, srcETag string,
) error {
	f.mu.Lock()
	defer f.mu.Unlock()
	if err := f.enter("StageBlockFromURL"); err != nil {
		return err
	}
	if f.noSource {
		return errors.New("fake azure: 403 CannotVerifyCopySource")
	}
	sb, ok := f.blobs[src]
	if !ok || !sb.exists {
		return fmt.Errorf("source %s: %w", src, blob.ErrNotFound)
	}
	if sb.etag != srcETag {
		return errors.New("fake azure: 412 SourceConditionNotMet")
	}
	if offset < 0 || count <= 0 || offset+count > int64(len(sb.committed)) {
		return fmt.Errorf("%w: range %d+%d of %d", errBadRequest, offset, count, len(sb.committed))
	}
	return f.stage(f.blob(dst), id, sb.committed[offset:offset+count])
}

func (f *fakeAzure) CommitBlockList(
	_ context.Context, name string, ids []string, sum []byte, ifNoneMatch bool,
) (string, error) {
	f.mu.Lock()
	defer f.mu.Unlock()
	if err := f.enter("CommitBlockList"); err != nil {
		return "", err
	}
	b := f.blob(name)
	if ifNoneMatch && b.exists {
		return "", fmt.Errorf("%w: 409 BlobAlreadyExists", ErrPrecondition)
	}
	if len(ids) > maxBlocks {
		return "", errors.New("fake azure: 409 BlockCountExceedsLimit")
	}
	var data []byte
	blocks := map[string][]byte{}
	for _, id := range ids {
		part, ok := b.uncommitted[id] // Latest: uncommitted first, then committed
		if !ok {
			part, ok = b.blocks[id]
		}
		if !ok {
			return "", fmt.Errorf("%w: block %s", ErrBlockList, id)
		}
		data = append(data, part...)
		blocks[id] = part
	}
	b.committed, b.exists, b.md5, b.blocks = data, true, bytes.Clone(sum), blocks
	b.uncommitted = map[string][]byte{}
	b.etag = f.nextETag()
	return b.etag, nil
}

func (f *fakeAzure) Upload(_ context.Context, name string, data []byte, ifNoneMatch bool) error {
	f.mu.Lock()
	defer f.mu.Unlock()
	if err := f.enter("Upload"); err != nil {
		return err
	}
	b := f.blob(name)
	if ifNoneMatch && b.exists {
		return fmt.Errorf("%w: 409 BlobAlreadyExists", ErrPrecondition)
	}
	sum := md5.Sum(data) //nolint:gosec // Azure's content MD5
	b.committed, b.exists, b.md5 = bytes.Clone(data), true, sum[:]
	b.blocks, b.uncommitted = map[string][]byte{}, map[string][]byte{}
	b.etag = f.nextETag()
	return nil
}

func (f *fakeAzure) Properties(_ context.Context, name string) (Props, error) {
	f.mu.Lock()
	defer f.mu.Unlock()
	if err := f.enter("Properties"); err != nil {
		return Props{}, err
	}
	b, ok := f.blobs[name]
	if !ok || !b.exists {
		return Props{}, fmt.Errorf("%s: %w", name, blob.ErrNotFound)
	}
	return Props{Size: int64(len(b.committed)), ETag: b.etag, MD5: bytes.Clone(b.md5)}, nil
}

func (f *fakeAzure) Download(
	_ context.Context,
	name string,
	offset, count int64,
	etag string,
) (io.ReadCloser, error) {
	f.mu.Lock()
	defer f.mu.Unlock()
	if err := f.enter("Download"); err != nil {
		return nil, err
	}
	b, ok := f.blobs[name]
	if !ok || !b.exists {
		return nil, fmt.Errorf("%s: %w", name, blob.ErrNotFound)
	}
	if etag != "" && etag != b.etag {
		return nil, fmt.Errorf("%w: 412 ConditionNotMet", ErrPrecondition)
	}
	if offset >= int64(len(b.committed)) {
		return nil, errors.New("fake azure: 416 InvalidRange")
	}
	end := int64(len(b.committed))
	if count > 0 {
		end = min(end, offset+count)
	}
	return io.NopCloser(bytes.NewReader(bytes.Clone(b.committed[offset:end]))), nil
}

func (f *fakeAzure) Delete(_ context.Context, name string) error {
	f.mu.Lock()
	defer f.mu.Unlock()
	if err := f.enter("Delete"); err != nil {
		return err
	}
	if _, ok := f.blobs[name]; !ok {
		return fmt.Errorf("%s: %w", name, blob.ErrNotFound)
	}
	delete(f.blobs, name)
	return nil
}

func (f *fakeAzure) List(_ context.Context, prefix, marker string) ([]Item, string, error) {
	f.mu.Lock()
	defer f.mu.Unlock()
	if err := f.enter("List"); err != nil {
		return nil, "", err
	}
	var names []string
	for name, b := range f.blobs {
		if b.exists && strings.HasPrefix(name, prefix) && name > marker {
			names = append(names, name)
		}
	}
	sort.Strings(names)
	next := ""
	if len(names) > f.pageLen {
		names = names[:f.pageLen]
		next = names[len(names)-1]
	}
	items := make([]Item, 0, len(names))
	for _, n := range names {
		items = append(items, Item{Name: n, Size: int64(len(f.blobs[n].committed))})
	}
	return items, next, nil
}

func (f *fakeAzure) SignRead(_ context.Context, name string, ttl time.Duration) (string, error) {
	f.mu.Lock()
	defer f.mu.Unlock()
	if err := f.enter("SignRead"); err != nil {
		return "", err
	}
	if f.noSign {
		return "", fmt.Errorf("%w: no signing credential", ErrConfig)
	}
	return fmt.Sprintf(
		"https://fake.blob.core.windows.net/c/%s?se=%d&sig=x",
		name,
		int(ttl.Seconds()),
	), nil
}

// staged reports the uncommitted block count of name.
func (f *fakeAzure) staged(name string) int {
	f.mu.Lock()
	defer f.mu.Unlock()
	if b, ok := f.blobs[name]; ok {
		return len(b.uncommitted)
	}
	return 0
}

// committedNames lists the committed blobs.
func (f *fakeAzure) committedNames() []string {
	f.mu.Lock()
	defer f.mu.Unlock()
	var out []string
	for n, b := range f.blobs {
		if b.exists {
			out = append(out, n)
		}
	}
	sort.Strings(out)
	return out
}
