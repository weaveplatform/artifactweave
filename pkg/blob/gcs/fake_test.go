package gcs

import (
	"bytes"
	"context"
	"errors"
	"fmt"
	"hash/crc32"
	"io"
	"sort"
	"strings"
	"sync"
	"time"

	"github.com/weaveplatform/artifactweave/pkg/blob"
)

// fakeGCS is an in-memory bucket with the semantics of real Cloud Storage
// that the store relies on and that fake-gcs-server does not model:
// must-not-exist preconditions on writes and compose destinations, compose
// limited to 32 sources pinned by generation, DeleteSourceObjects deleting
// the sources, server-side CRC32C checks on writes, and generations that
// only increase. failOn makes the Nth and later calls of an operation fail,
// which is how the error paths are reached.
type fakeGCS struct {
	mu       sync.Mutex
	objects  map[string]fakeObject
	gen      int64
	calls    map[string]int
	fail     map[string]failure
	composes [][]Source
	// shortRead makes Read return one byte less than asked for, the shape of
	// a connection dropped mid-body.
	shortRead bool
	// badCompose makes Compose report a CRC32C other than the content's.
	badCompose bool
}

type fakeObject struct {
	data []byte
	gen  int64
}

type failure struct {
	after int
	err   error
}

func newFake() *fakeGCS {
	return &fakeGCS{
		objects: map[string]fakeObject{},
		calls:   map[string]int{},
		fail:    map[string]failure{},
	}
}

// failOn makes op fail with err from its (after+1)th call onwards.
func (f *fakeGCS) failOn(op string, after int, err error) {
	f.mu.Lock()
	defer f.mu.Unlock()
	f.fail[op] = failure{after: after, err: err}
	f.calls[op] = 0
}

func (f *fakeGCS) enter(op string) error {
	f.calls[op]++
	if fl, ok := f.fail[op]; ok && f.calls[op] > fl.after {
		return fl.err
	}
	return nil
}

func (f *fakeGCS) attrs(name string, o fakeObject) Attrs {
	return Attrs{
		Name:       name,
		Size:       int64(len(o.data)),
		CRC32C:     crc32.Checksum(o.data, castagnoli),
		Generation: o.gen,
	}
}

// put stores an object directly, as another writer would.
func (f *fakeGCS) put(name string, data []byte) {
	f.mu.Lock()
	defer f.mu.Unlock()
	f.gen++
	f.objects[name] = fakeObject{data: bytes.Clone(data), gen: f.gen}
}

func (f *fakeGCS) names(prefix string) []string {
	f.mu.Lock()
	defer f.mu.Unlock()
	var out []string
	for n := range f.objects {
		if strings.HasPrefix(n, prefix) {
			out = append(out, n)
		}
	}
	sort.Strings(out)
	return out
}

func (f *fakeGCS) Write(_ context.Context, name string, data []byte, crc uint32) (Attrs, error) {
	f.mu.Lock()
	defer f.mu.Unlock()
	if err := f.enter("Write"); err != nil {
		return Attrs{}, err
	}
	if _, ok := f.objects[name]; ok {
		return Attrs{}, fmt.Errorf("%w: %s exists", ErrPrecondition, name)
	}
	if crc32.Checksum(data, castagnoli) != crc {
		return Attrs{}, fmt.Errorf("fake: %s: crc32c mismatch", name)
	}
	f.gen++
	o := fakeObject{data: bytes.Clone(data), gen: f.gen}
	f.objects[name] = o
	return f.attrs(name, o), nil
}

func (f *fakeGCS) Attrs(_ context.Context, name string) (Attrs, error) {
	f.mu.Lock()
	defer f.mu.Unlock()
	if err := f.enter("Attrs"); err != nil {
		return Attrs{}, err
	}
	o, ok := f.objects[name]
	if !ok {
		return Attrs{}, fmt.Errorf("%w: %s", blob.ErrNotFound, name)
	}
	return f.attrs(name, o), nil
}

func (f *fakeGCS) Read(_ context.Context, name string, offset int64) (io.ReadCloser, error) {
	f.mu.Lock()
	defer f.mu.Unlock()
	if err := f.enter("Read"); err != nil {
		return nil, err
	}
	o, ok := f.objects[name]
	if !ok {
		return nil, fmt.Errorf("%w: %s", blob.ErrNotFound, name)
	}
	if offset >= int64(len(o.data)) {
		return nil, fmt.Errorf("fake: %s: range not satisfiable", name)
	}
	body := o.data[offset:]
	if f.shortRead {
		body = body[:len(body)-1]
	}
	return io.NopCloser(bytes.NewReader(bytes.Clone(body))), nil
}

func (f *fakeGCS) Compose(
	_ context.Context,
	dst string,
	srcs []Source,
	deleteSources bool,
) (Attrs, error) {
	f.mu.Lock()
	defer f.mu.Unlock()
	if err := f.enter("Compose"); err != nil {
		return Attrs{}, err
	}
	if len(srcs) == 0 || len(srcs) > maxSources {
		return Attrs{}, fmt.Errorf("fake: compose of %d sources", len(srcs))
	}
	if _, ok := f.objects[dst]; ok {
		return Attrs{}, fmt.Errorf("%w: %s exists", ErrPrecondition, dst)
	}
	var data []byte
	for _, s := range srcs {
		o, ok := f.objects[s.Name]
		if !ok || (s.Generation != 0 && o.gen != s.Generation) {
			return Attrs{}, fmt.Errorf("%w: source %s#%d", blob.ErrNotFound, s.Name, s.Generation)
		}
		data = append(data, o.data...)
	}
	f.composes = append(f.composes, srcs)
	f.gen++
	o := fakeObject{data: data, gen: f.gen}
	f.objects[dst] = o
	if deleteSources {
		for _, s := range srcs {
			delete(f.objects, s.Name)
		}
	}
	a := f.attrs(dst, o)
	if f.badCompose {
		a.CRC32C++
	}
	return a, nil
}

func (f *fakeGCS) Delete(_ context.Context, name string, generation int64) error {
	f.mu.Lock()
	defer f.mu.Unlock()
	if err := f.enter("Delete"); err != nil {
		return err
	}
	o, ok := f.objects[name]
	if !ok {
		return fmt.Errorf("%w: %s", blob.ErrNotFound, name)
	}
	if generation != 0 && o.gen != generation {
		return fmt.Errorf("%w: %s is generation %d", ErrPrecondition, name, o.gen)
	}
	delete(f.objects, name)
	return nil
}

func (f *fakeGCS) List(_ context.Context, prefix string, fn func(Attrs) error) error {
	f.mu.Lock()
	if err := f.enter("List"); err != nil {
		f.mu.Unlock()
		return err
	}
	var out []Attrs
	for n, o := range f.objects {
		if strings.HasPrefix(n, prefix) {
			out = append(out, f.attrs(n, o))
		}
	}
	f.mu.Unlock()
	sort.Slice(out, func(i, j int) bool { return out[i].Name < out[j].Name })
	for _, a := range out {
		if err := fn(a); err != nil {
			return err
		}
	}
	return nil
}

func (f *fakeGCS) SignedURL(name string, ttl time.Duration) (string, error) {
	f.mu.Lock()
	defer f.mu.Unlock()
	if err := f.enter("SignedURL"); err != nil {
		return "", err
	}
	return fmt.Sprintf(
		"https://storage.googleapis.com/b/%s?X-Goog-Expires=%d",
		name,
		int(ttl.Seconds()),
	), nil
}

var errInjected = errors.New("injected")
