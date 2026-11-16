package itemkit

import (
	"errors"
	"io"
	"strings"
	"sync"
	"testing"
)

// entryOpens is an entry's content that counts the reads opened on it.
type entryOpens struct {
	content string
	opened  int
}

func (e *entryOpens) next() io.ReadCloser {
	e.opened++
	return io.NopCloser(strings.NewReader(e.content))
}

// entryContent serves http.ServeContent's seeks and reads: forward by
// skipping, backward by reading the entry again from its start, and it
// opens nothing until a byte is read.
func TestUnitEntryContentSeeksByReadingAgain(t *testing.T) {
	e := &entryOpens{content: "0123456789"}
	c := &entryContent{open: e.next, size: 10}
	read := func(at int64, n int) string {
		t.Helper()
		if _, err := c.Seek(at, io.SeekStart); err != nil {
			t.Fatal(err)
		}
		b := make([]byte, n)
		if _, err := io.ReadFull(c, b); err != nil {
			t.Fatal(err)
		}
		return string(b)
	}
	if end, err := c.Seek(0, io.SeekEnd); end != 10 || err != nil || e.opened != 0 {
		t.Fatalf("seek to the end: %d %v, %d opened", end, err, e.opened)
	}
	if got := read(3, 2); got != "34" || e.opened != 1 {
		t.Fatalf("forward: %q, %d opened", got, e.opened)
	}
	if got := read(7, 2); got != "78" || e.opened != 1 {
		t.Fatalf("further forward: %q, %d opened", got, e.opened)
	}
	if got := read(0, 2); got != "01" || e.opened != 2 {
		t.Fatalf("backward: %q, %d opened", got, e.opened)
	}
	if at, err := c.Seek(-1, io.SeekCurrent); at != 1 || err != nil {
		t.Fatalf("seek back one: %d %v", at, err)
	}
	if _, err := c.Seek(-5, io.SeekStart); !errors.Is(err, errEntrySeek) {
		t.Fatalf("seek before the start: %v", err)
	}
	if err := c.Close(); err != nil || c.failure() != nil {
		t.Fatalf("close: %v, failure %v", err, c.failure())
	}
	// A read after Close, as ServeContent's goroutine may make, opens
	// nothing that would be left open.
	if _, err := c.Read(make([]byte, 1)); !errors.Is(err, errEntryClosed) || e.opened != 2 {
		t.Fatalf("read after close: %v, %d opened", err, e.opened)
	}
}

// An entry that ends before its length, or fails a read, is kept as the
// failure ServeContent cannot return; a read that Close ended is not.
func TestUnitEntryContentKeepsAFailedRead(t *testing.T) {
	short := &entryContent{size: 10, open: func() io.ReadCloser {
		return io.NopCloser(strings.NewReader("012"))
	}}
	if _, err := short.Seek(5, io.SeekStart); err != nil {
		t.Fatal(err)
	}
	if _, err := short.Read(make([]byte, 2)); !errors.Is(err, io.ErrUnexpectedEOF) ||
		!errors.Is(short.failure(), io.ErrUnexpectedEOF) {
		t.Fatalf("short entry: %v, failure %v", err, short.failure())
	}
	broken := errors.New("broken")
	failing := &entryContent{size: 10, open: func() io.ReadCloser {
		pr, pw := io.Pipe()
		_ = pw.CloseWithError(broken)
		return pr
	}}
	if _, err := failing.Read(
		make([]byte, 2),
	); !errors.Is(err, broken) ||
		!errors.Is(failing.failure(), broken) {
		t.Fatalf("failing entry: %v, failure %v", err, failing.failure())
	}
	// Close ends a read another goroutine is waiting on, and that read's
	// failure is not the entry's.
	pr, pw := io.Pipe()
	defer func() { _ = pw.Close() }()
	waiting := &entryContent{size: 10, open: func() io.ReadCloser { return pr }}
	var wg sync.WaitGroup
	wg.Add(1)
	started := make(chan struct{})
	go func() {
		defer wg.Done()
		r, err := waiting.reader()
		close(started)
		if err != nil {
			return
		}
		_, err = r.Read(make([]byte, 1))
		if err != nil {
			_ = waiting.fail(err)
		}
	}()
	<-started
	if err := waiting.Close(); err != nil {
		t.Fatal(err)
	}
	wg.Wait()
	if waiting.failure() != nil {
		t.Fatalf("a read Close ended failed the entry: %v", waiting.failure())
	}
}
