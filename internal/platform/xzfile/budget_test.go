package xzfile

import (
	"bytes"
	"context"
	"errors"
	"io"
	"runtime"
	"sync/atomic"
	"testing"
	"time"
)

// withBudget sizes the process's budget for one test, and checks when the
// test ends that every dictionary it took was given back.
func withBudget(t *testing.T, n int64) {
	t.Helper()
	SetBudget(n)
	t.Cleanup(func() {
		SetBudget(DefaultBudget)
		if used, waiting := budgetState(); used != 0 || waiting != 0 {
			t.Errorf("the test left %d bytes held and %d decodes waiting", used, waiting)
		}
	})
}

func budgetState() (used int64, waiting int) {
	b := dictionaries
	b.mu.Lock()
	defer b.mu.Unlock()
	return b.used, b.waiting.Len()
}

// eventually fails t unless ok holds within ten seconds.
func eventually(t *testing.T, what string, ok func() bool) {
	t.Helper()
	for deadline := time.Now().Add(10 * time.Second); !ok(); {
		if time.Now().After(deadline) {
			t.Fatalf("timed out waiting for %s", what)
		}
		time.Sleep(time.Millisecond)
	}
}

// waitFor fails t unless the budget reaches used bytes held and waiting
// decodes queued.
func waitFor(t *testing.T, used int64, waiting int) {
	t.Helper()
	eventually(t, "the budget's state", func() bool {
		u, w := budgetState()
		return u == used && w == waiting
	})
}

// firstDict is the dictionary a block starts with, lzma2's startCap: a
// dictionary of up to 64 KiB is taken from the budget whole as the block
// starts, and a larger one 64 KiB at a time, as it fills (#1413).
const firstDict = 64 << 10

// hugeHeader is an xz file of 52 bytes holding content, whose block header
// names a 1.5 GiB dictionary (property 37), as the files of #1354 and
// #1413 do. The tests of the queue lower it with the caller's limit below
// firstDict, so the block takes all of it as it starts.
var hugeHeaderContent = []byte("abc")

func hugeHeader() []byte {
	chunk := append(
		append([]byte{0x01, 0x00, byte(len(hugeHeaderContent) - 1)}, hugeHeaderContent...),
		0x00,
	)
	return stream(
		checkNone,
		false,
		maxDictProperty,
		[][]byte{hugeHeaderContent},
		[][]byte{chunk},
	).bytes()
}

// gate holds a decode inside its block until it is opened: the reader has
// taken and allocated what the block's dictionary holds so far, and is
// reading the LZMA2 data behind the gate.
type gate struct {
	open    chan struct{}
	arrived *atomic.Int64
}

func newGate(arrived *atomic.Int64) gate {
	return gate{open: make(chan struct{}), arrived: arrived}
}

func (g gate) Read([]byte) (int, error) {
	g.arrived.Add(1)
	<-g.open
	return 0, io.EOF
}

// gated is file with g between its block header and its LZMA2 data.
func gated(file []byte, g gate) io.Reader {
	cut := len(streamHeader(checkNone)) + int(file[12]+1)*4
	return io.MultiReader(bytes.NewReader(file[:cut]), g, bytes.NewReader(file[cut:]))
}

// decode reads in whole in a goroutine, and sends what it read.
type result struct {
	got []byte
	err error
}

func decode(ctx context.Context, in io.Reader, limit int64) <-chan result {
	done := make(chan result, 1)
	go func() {
		r, err := NewReader(ctx, in, limit)
		if err != nil {
			done <- result{err: err}
			return
		}
		defer func() { _ = r.Close() }()
		got, err := io.ReadAll(r)
		done <- result{got, err}
	}()
	return done
}

// decodeAgainst reads in in a goroutine and compares it with want as it
// goes, holding no more of it than a 4 KiB buffer, and sends a mismatch as
// its error.
func decodeAgainst(ctx context.Context, in io.Reader, limit int64, want []byte) <-chan result {
	done := make(chan result, 1)
	go func() {
		r, err := NewReader(ctx, in, limit)
		if err != nil {
			done <- result{err: err}
			return
		}
		defer func() { _ = r.Close() }()
		c := &comparer{want: want}
		if _, err = io.CopyBuffer(c, r, make([]byte, 4<<10)); err == nil && len(c.want) > 0 {
			err = errors.New("the content ended early")
		}
		done <- result{err: err}
	}()
	return done
}

// comparer is a writer that fails as soon as what it is written stops
// matching want.
type comparer struct{ want []byte }

func (c *comparer) Write(p []byte) (int, error) {
	if !bytes.HasPrefix(c.want, p) {
		return 0, errors.New("the content differs")
	}
	c.want = c.want[len(p):]
	return len(p), nil
}

func wantContent(t *testing.T, what string, done <-chan result) {
	t.Helper()
	wantRead(t, what, done, hugeHeaderContent)
}

// wantRead fails t unless the decode reads content whole within ten
// seconds.
func wantRead(t *testing.T, what string, done <-chan result, content []byte) {
	t.Helper()
	select {
	case r := <-done:
		if r.err != nil || !bytes.Equal(r.got, content) {
			t.Fatalf("%s: %d bytes %v", what, len(r.got), r.err)
		}
	case <-time.After(10 * time.Second):
		t.Fatalf("%s did not finish", what)
	}
}

// pending reports whether the decode is still running.
func pending(done <-chan result) bool {
	select {
	case <-done:
		return false
	default:
		return true
	}
}

// Sixteen decodes at once of files whose header names 1.5 GiB, each with a
// 1 MiB limit and content that fills it, hold no more dictionary than a
// 2 MiB budget and the one dictionary let past it: the rest wait to start
// or to grow, having allocated no more than they hold. With the budget off
// all sixteen hold theirs, which shows the files do allocate what is
// measured.
func TestUnitTheBudgetBoundsConcurrentDecodes(t *testing.T) {
	const decodes, limit = 16, 1 << 20
	live := func(t *testing.T, budget int64) (heap uint64, arrivedAll bool) {
		t.Helper()
		withBudget(t, budget)
		var arrived atomic.Int64
		gates, done := make([]gate, decodes), make([]<-chan result, decodes)
		file, content := filling(limit)
		var before, during runtime.MemStats
		runtime.GC()
		runtime.ReadMemStats(&before)
		for i := range gates {
			gates[i] = newGate(&arrived)
			done[i] = decodeAgainst(
				t.Context(),
				gatedAt(file, []int{endOfContent(file, len(content))}, gates[i]),
				limit,
				content,
			)
		}
		eventually(t, "each decode to hold its dictionary or wait", func() bool {
			_, w := budgetState()
			return arrived.Load()+int64(w) == decodes
		})
		if used, _ := budgetState(); budget > 0 && used > budget+limit {
			t.Errorf("the decodes hold %d KiB of a %d KiB budget", used>>10, budget>>10)
		}
		runtime.GC()
		runtime.ReadMemStats(&during)
		all := arrived.Load() == decodes
		for _, g := range gates {
			close(g.open)
		}
		for _, d := range done {
			wantRead(t, "a decode", d, nil)
		}
		return during.HeapAlloc - min(before.HeapAlloc, during.HeapAlloc), all
	}
	on, onAll := live(t, 2*limit)
	off, offAll := live(t, 0)
	t.Logf("the decodes held %d KiB with the budget and %d KiB without", on>>10, off>>10)
	// Besides its dictionary each decode holds 45 to 70 KiB: its 32 KiB
	// input buffer, its output buffer and its coder's state. The bound
	// allows 128 KiB each because under the race detector the stalled
	// decode is let past the budget more often and the heap measures up
	// to 4.1 MiB; that is still far below the 16 MiB with the budget off.
	if on > 2*limit+limit+decodes*128<<10 || onAll {
		t.Errorf(
			"with a 2 MiB budget the decodes held %d KiB, all at once %v; want at most 5 MiB",
			on>>10,
			onAll,
		)
	}
	if off < decodes*limit || !offAll {
		t.Errorf(
			"with the budget off the decodes held %d KiB, all at once %v; want at least 16 MiB",
			off>>10,
			offAll,
		)
	}
}

// A decode whose dictionary does not fit waits, and decodes whole once the
// decode ahead of it gives its dictionary back.
func TestUnitADecodeThatDoesNotFitWaits(t *testing.T) {
	withBudget(t, 64<<10)
	var arrived atomic.Int64
	g := newGate(&arrived)
	first := decode(t.Context(), gated(hugeHeader(), g), 64<<10)
	waitFor(t, 64<<10, 0)
	second := decode(t.Context(), bytes.NewReader(hugeHeader()), 32<<10)
	waitFor(t, 64<<10, 1)
	if !pending(second) {
		t.Fatal("the second decode did not wait")
	}
	close(g.open)
	wantContent(t, "the first decode", first)
	wantContent(t, "the second decode", second)
}

// A dictionary larger than the whole budget decodes alone: it waits until
// nothing else holds the budget, and the decodes behind it wait until it
// is done, though they would fit.
func TestUnitADictionaryLargerThanTheBudgetDecodesAlone(t *testing.T) {
	withBudget(t, 32<<10)
	var arrived atomic.Int64
	small, large := newGate(&arrived), newGate(&arrived)
	first := decode(t.Context(), gated(hugeHeader(), small), 16<<10)
	waitFor(t, 16<<10, 0)
	alone := decode(t.Context(), gated(hugeHeader(), large), 128<<10)
	waitFor(t, 16<<10, 1)
	behind := decode(t.Context(), bytes.NewReader(hugeHeader()), 4<<10)
	waitFor(t, 16<<10, 2)
	close(small.open)
	wantContent(t, "the first decode", first)
	waitFor(t, firstDict, 1)
	if !pending(behind) {
		t.Fatal("a decode went in beside one larger than the budget")
	}
	close(large.open)
	wantContent(t, "the large decode", alone)
	wantContent(t, "the decode behind it", behind)
}

// A wait that its context ends fails the read with the context's error,
// takes nothing and lets in the waiters it held up. A decode that fits
// goes ahead whatever its context says, as it did without a budget.
func TestUnitACancelledWaitTakesNothing(t *testing.T) {
	withBudget(t, 64<<10)
	var arrived atomic.Int64
	g := newGate(&arrived)
	holding := decode(t.Context(), gated(hugeHeader(), g), 48<<10)
	waitFor(t, 48<<10, 0)
	ctx, cancel := context.WithCancel(t.Context())
	cancelled := decode(ctx, bytes.NewReader(hugeHeader()), 32<<10)
	waitFor(t, 48<<10, 1)
	// It fits, but arrived after the waiter ahead of it.
	behind := decode(t.Context(), bytes.NewReader(hugeHeader()), 8<<10)
	waitFor(t, 48<<10, 2)
	cancel()
	select {
	case r := <-cancelled:
		if !errors.Is(r.err, context.Canceled) || len(r.got) != 0 {
			t.Fatalf("the cancelled decode: %q %v, want context.Canceled", r.got, r.err)
		}
	case <-time.After(10 * time.Second):
		t.Fatal("the cancelled decode went on waiting")
	}
	wantContent(t, "the decode behind it", behind)
	waitFor(t, 48<<10, 0)
	done, stop := context.WithCancel(t.Context())
	stop()
	wantContent(
		t,
		"a decode that fits, cancelled",
		decode(done, bytes.NewReader(hugeHeader()), 8<<10),
	)
	close(g.open)
	wantContent(t, "the decode that held the budget", holding)
}

// One request that reads a second xz file while its first is open does
// not deadlock: its readers are one holder, which passes a decode queued
// for the first one's dictionary and goes in while it alone holds the
// budget. Readers of no request each hold for themselves, and the second
// one waits behind that decode, which waits for the first: the deadlock.
func TestUnitOneRequestReadsTwoStreams(t *testing.T) {
	withBudget(t, 64<<10)
	for _, request := range []bool{true, false} {
		ctx := t.Context()
		if request {
			ctx = WithRequest(ctx)
		}
		first, err := NewReader(ctx, bytes.NewReader(hugeHeader()), 32<<10)
		if err != nil {
			t.Fatal(err)
		}
		var b [1]byte
		if _, err = io.ReadFull(first, b[:]); err != nil {
			t.Fatal(err)
		}
		waitFor(t, 32<<10, 0)
		other := decode(t.Context(), bytes.NewReader(hugeHeader()), 64<<10)
		waitFor(t, 32<<10, 1)
		secondCtx, cancel := context.WithCancel(ctx)
		second := decode(secondCtx, bytes.NewReader(hugeHeader()), 48<<10)
		if request {
			wantContent(t, "the request's second stream", second)
		} else {
			waitFor(t, 32<<10, 2)
			cancel()
			if r := <-second; !errors.Is(r.err, context.Canceled) {
				t.Fatalf(
					"a second reader of no request: %v, want it to wait until cancelled",
					r.err,
				)
			}
		}
		cancel()
		if rest, err := io.ReadAll(first); err != nil || string(b[:])+string(rest) != "abc" {
			t.Fatalf("the request's first stream: %q %v", rest, err)
		}
		_ = first.Close()
		wantContent(t, "the other request", other)
	}
}

// A reader gives its dictionary back when its block ends, at io.EOF, when
// it fails, on Close, and, dropped unclosed in the middle of a block, once
// the garbage collector finds it.
func TestUnitTheDictionaryIsGivenBack(t *testing.T) {
	withBudget(t, 64<<10)
	open := func(t *testing.T, file []byte) io.ReadCloser {
		t.Helper()
		r, err := NewReader(t.Context(), bytes.NewReader(file), 32<<10)
		if err != nil {
			t.Fatal(err)
		}
		var b [1]byte
		if _, err = io.ReadFull(r, b[:]); err != nil {
			t.Fatal(err)
		}
		waitFor(t, 32<<10, 0)
		return r
	}
	t.Run("Close", func(t *testing.T) {
		r := open(t, hugeHeader())
		for range 2 {
			if err := r.Close(); err != nil {
				t.Fatal(err)
			}
		}
		waitFor(t, 0, 0)
		if _, err := r.Read(make([]byte, 1)); !errors.Is(err, errClosed) {
			t.Fatalf("a read after Close: %v", err)
		}
	})
	t.Run("io.EOF", func(t *testing.T) {
		r := open(t, hugeHeader())
		if _, err := io.ReadAll(r); err != nil {
			t.Fatal(err)
		}
		waitFor(t, 0, 0)
	})
	t.Run("an error inside the block", func(t *testing.T) {
		// The header declares two bytes of content and the data holds four,
		// so the block fails before its data ends.
		p := stream(checkNone, false, maxDictProperty, [][]byte{[]byte("abcd")},
			[][]byte{{0x01, 0x00, 0x03, 'a', 'b', 'c', 'd', 0x00}})
		p.blocks[0].header = blockHeader(-1, 2, filterLZMA2, 1, maxDictProperty)
		r := open(t, p.bytes())
		if _, err := io.ReadAll(r); !errors.Is(err, errUncompressed) {
			t.Fatalf("%v, want %v", err, errUncompressed)
		}
		waitFor(t, 0, 0)
	})
	t.Run("between blocks", func(t *testing.T) {
		data := [][]byte{[]byte("abcd"), []byte("efgh")}
		chunks := [][]byte{
			{0x01, 0x00, 0x03, 'a', 'b', 'c', 'd', 0x00},
			{0x01, 0x00, 0x03, 'e', 'f', 'g', 'h', 0x00},
		}
		r := open(t, stream(checkNone, false, maxDictProperty, data, chunks).bytes())
		// The rest of the first block, then the second, which takes the
		// first one's place rather than adding to it.
		var b [4]byte
		if n, err := io.ReadFull(r, b[:]); err != nil || string(b[:n]) != "bcde" {
			t.Fatalf("%q %v", b[:n], err)
		}
		waitFor(t, 32<<10, 0)
		_ = r.Close()
	})
	t.Run("dropped", func(t *testing.T) {
		func() { _ = open(t, hugeHeader()) }()
		eventually(t, "the garbage collector to give the dictionary back", func() bool {
			runtime.GC()
			used, _ := budgetState()
			return used == 0
		})
	})
}

// Turning the budget off lets every waiting decode in; the dictionaries
// already held are still given back.
func TestUnitTurningTheBudgetOffLetsWaitersIn(t *testing.T) {
	withBudget(t, 32<<10)
	var arrived atomic.Int64
	g := newGate(&arrived)
	holding := decode(t.Context(), gated(hugeHeader(), g), 32<<10)
	waitFor(t, 32<<10, 0)
	waiting := decode(t.Context(), bytes.NewReader(hugeHeader()), 32<<10)
	waitFor(t, 32<<10, 1)
	SetBudget(0)
	wantContent(t, "the waiting decode", waiting)
	waitFor(t, 32<<10, 0)
	close(g.open)
	wantContent(t, "the holding decode", holding)
}

// The budget changes no answer: every early end of the fixture, and every
// byte of it changed, decodes or is refused the same with a budget smaller
// than every block's dictionary, so each decodes alone, as with none.
func TestUnitSameAnswersWithTheBudgetOnAndOff(t *testing.T) {
	data, _ := fixture(t)
	var inputs [][]byte
	for cut := range len(data) {
		inputs = append(inputs, data[:len(data)-cut])
	}
	for i := range data {
		broken := append([]byte{}, data...)
		broken[i] ^= 0x41
		inputs = append(inputs, broken)
	}
	answers := func(budget int64) []result {
		withBudget(t, budget)
		out := make([]result, 0, len(inputs))
		for _, in := range inputs {
			got, err := readXZ(in)
			out = append(out, result{got, err})
		}
		return out
	}
	on, off := answers(4<<10), answers(0)
	for i := range inputs {
		if !bytes.Equal(on[i].got, off[i].got) || (on[i].err == nil) != (off[i].err == nil) ||
			on[i].err != nil && on[i].err.Error() != off[i].err.Error() {
			t.Errorf(
				"input %d: %q %v with the budget, %q %v without",
				i,
				on[i].got,
				on[i].err,
				off[i].got,
				off[i].err,
			)
		}
	}
}
