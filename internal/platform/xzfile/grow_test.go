package xzfile

import (
	"bytes"
	"context"
	"errors"
	"io"
	"math/rand/v2"
	"runtime"
	"sync/atomic"
	"testing"

	"github.com/ulikunitz/xz/lzma"
)

// filling is an xz file of one block whose header names 1.5 GiB, as
// hugeHeader's does, holding n bytes of content in uncompressed chunks of
// 64 KiB: its dictionary fills as it decodes, so it grows to hold all n
// bytes, whatever the header names.
func filling(n int) (file, content []byte) {
	content = bytes.Repeat([]byte("filling "), n/8+1)[:n]
	var chunks []byte
	for off := 0; off < n; off += 64 << 10 {
		part := content[off:min(off+64<<10, n)]
		kind := byte(0x02)
		if off == 0 {
			kind = 0x01
		}
		chunks = append(append(chunks, kind, byte((len(part)-1)>>8), byte(len(part)-1)), part...)
	}
	chunks = append(chunks, 0x00)
	return stream(
			checkNone,
			false,
			maxDictProperty,
			[][]byte{content},
			[][]byte{chunks},
		).bytes(),
		content
}

// dataStart is where the LZMA2 data of file's first block starts.
func dataStart(file []byte) int {
	return len(streamHeader(checkNone)) + int(file[12]+1)*4
}

// afterContent is where a filling file's LZMA2 data has given n bytes of
// content, n a multiple of 64 KiB or all of it: past n bytes and the
// header of each chunk that holds them.
func afterContent(file []byte, n int) int {
	return dataStart(file) + n + 3*((n+64<<10-1)/(64<<10))
}

// endOfContent is where a filling file of n bytes has its end-of-stream
// chunk: a gate there holds the decode once its dictionary holds all the
// content.
func endOfContent(file []byte, n int) int { return afterContent(file, n) }

// randomBytes is n bytes that no match reaches into.
func randomBytes(n int) []byte {
	rng := rand.New(rand.NewPCG(3, 4))
	b := make([]byte, n)
	for i := range b {
		b[i] = byte(rng.Uint32())
	}
	return b
}

// gatedAt is file with gates[i] at offset at[i].
func gatedAt(file []byte, at []int, gates ...gate) io.Reader {
	var parts []io.Reader
	from := 0
	for i, cut := range at {
		parts = append(parts, bytes.NewReader(file[from:cut]), gates[i])
		from = cut
	}
	return io.MultiReader(append(parts, bytes.NewReader(file[from:]))...)
}

// A file of 52 bytes whose header names 1.5 GiB decodes with 64 KiB of
// dictionary, the most the budget holds for it, at the limits of Nix and
// Debian's data.tar.xz and with none: before, it allocated all 1.5 GiB
// before it decoded a byte (#1413).
func TestUnitAShortFileTakesLittle(t *testing.T) {
	file := hugeHeader()
	if len(file) != 52 {
		t.Fatalf("the file is %d bytes", len(file))
	}
	withBudget(t, DefaultBudget)
	for _, limit := range []int64{0, 2 << 30, 3<<29 + 1} {
		var before, after runtime.MemStats
		runtime.ReadMemStats(&before)
		r, err := NewReader(t.Context(), bytes.NewReader(file), limit)
		if err != nil {
			t.Fatal(err)
		}
		var b [1]byte
		if _, err = io.ReadFull(r, b[:]); err != nil {
			t.Fatal(err)
		}
		if used, _ := budgetState(); used != firstDict {
			t.Errorf("limit %d: the budget holds %d KiB, want 64", limit, used>>10)
		}
		rest, err := io.ReadAll(r)
		_ = r.Close()
		runtime.ReadMemStats(&after)
		if err != nil || string(b[:])+string(rest) != string(hugeHeaderContent) {
			t.Fatalf("limit %d: %q %v", limit, rest, err)
		}
		if n := after.TotalAlloc - before.TotalAlloc; n >= 1<<20 {
			t.Errorf("limit %d: allocated %d KiB, want less than 1 MiB", limit, n>>10)
		}
	}
}

// The budget holds what a block's dictionary has grown to, not what its
// header names: 512 KiB once 300 KiB of content are decoded. A short file
// beside it decodes at once, where before it waited for the whole of its
// header's 1.5 GiB.
func TestUnitTheBudgetHoldsWhatTheDictionaryHolds(t *testing.T) {
	withBudget(t, 1<<20)
	file, content := filling(320 << 10)
	var arrived atomic.Int64
	g := newGate(&arrived)
	holding := decode(t.Context(), gatedAt(file, []int{endOfContent(file, len(content))}, g), 0)
	eventually(t, "the decode to fill its dictionary", func() bool { return arrived.Load() == 1 })
	waitFor(t, 512<<10, 0)
	wantContent(t, "a short file beside it", decode(t.Context(), bytes.NewReader(hugeHeader()), 0))
	close(g.open)
	wantRead(t, "the decode that filled 512 KiB", holding, content)
	waitFor(t, 0, 0)
}

// A dictionary that must grow past what the budget has left waits, holding
// what it has, and grows once another decode gives its dictionary back.
func TestUnitAGrowingDictionaryWaitsForRoom(t *testing.T) {
	withBudget(t, 768<<10)
	var arrived atomic.Int64
	first, firstContent := filling(320 << 10)
	g := newGate(&arrived)
	holding := decode(
		t.Context(),
		gatedAt(first, []int{endOfContent(first, len(firstContent))}, g),
		0,
	)
	eventually(
		t,
		"the first decode to fill its dictionary",
		func() bool { return arrived.Load() == 1 },
	)
	waitFor(t, 512<<10, 0)
	second, secondContent := filling(320 << 10)
	growing := decode(t.Context(), bytes.NewReader(second), 0)
	// 64, 128 and 256 KiB fit beside the first decode's 512; 512 does not.
	waitFor(t, 768<<10, 1)
	if !pending(growing) {
		t.Fatal("the second decode grew past the budget")
	}
	close(g.open)
	wantRead(t, "the first decode", holding, firstContent)
	wantRead(t, "the second decode", growing, secondContent)
	waitFor(t, 0, 0)
}

// Two decodes that each hold half the budget and must both grow would
// each wait for the other for ever. The one that waited first grows alone,
// past the budget, while the other waits; once it is done the other grows.
func TestUnitStalledDecodesGrowOneAtATime(t *testing.T) {
	withBudget(t, 512<<10)
	file, content := filling(320 << 10)
	at := []int{afterContent(file, 192<<10), endOfContent(file, len(content))}
	var arrived atomic.Int64
	a := []gate{newGate(&arrived), newGate(&arrived)}
	b := []gate{newGate(&arrived), newGate(&arrived)}
	first := decode(t.Context(), gatedAt(file, at, a...), 0)
	second := decode(t.Context(), gatedAt(file, at, b...), 0)
	// 192 KiB of content fills a 256 KiB dictionary in each.
	eventually(t, "both decodes to hold 256 KiB", func() bool { return arrived.Load() == 2 })
	waitFor(t, 512<<10, 0)
	close(a[0].open)
	close(b[0].open)
	// Each needs 512 KiB; one grows to it alone, the other waits.
	eventually(t, "one decode to grow alone", func() bool { return arrived.Load() == 3 })
	waitFor(t, 768<<10, 1)
	close(a[1].open)
	close(b[1].open)
	wantRead(t, "the first decode", first, content)
	wantRead(t, "the second decode", second, content)
	waitFor(t, 0, 0)
}

// A wait to grow that its context ends fails the read with the context's
// error, and gives back what the dictionary held.
func TestUnitACancelledGrowthGivesBackItsDictionary(t *testing.T) {
	withBudget(t, 640<<10)
	var arrived atomic.Int64
	first, firstContent := filling(320 << 10)
	g := newGate(&arrived)
	holding := decode(
		t.Context(),
		gatedAt(first, []int{endOfContent(first, len(firstContent))}, g),
		0,
	)
	eventually(
		t,
		"the first decode to fill its dictionary",
		func() bool { return arrived.Load() == 1 },
	)
	second, _ := filling(320 << 10)
	ctx, cancel := context.WithCancel(t.Context())
	growing := decode(ctx, bytes.NewReader(second), 0)
	// 64 and 128 KiB fit beside 512; 256 does not.
	waitFor(t, 640<<10, 1)
	cancel()
	if r := <-growing; !errors.Is(r.err, context.Canceled) || len(r.got) > 128<<10 {
		t.Fatalf("the cancelled decode: %d bytes %v, want context.Canceled", len(r.got), r.err)
	}
	waitFor(t, 512<<10, 0)
	close(g.open)
	wantRead(t, "the first decode", holding, firstContent)
}

// A block whose matches reach back the whole dictionary its header names
// decodes as ulikunitz/xz's reader decodes it, though the dictionary grew
// through three sizes to reach it, and one whose header names less than
// they reach is refused in that reader's words.
func TestUnitMatchesReachTheWholeDictionary(t *testing.T) {
	data := bytes.Repeat(randomBytes(256<<10), 2)
	compressed := packLZMA2(t, 1<<20, data)
	for _, c := range []struct {
		dict int64
		ok   bool
	}{{256 << 10, true}, {192 << 10, false}} {
		file := stream(
			checkCRC64,
			false,
			lzma.EncodeDictCap(c.dict),
			[][]byte{data},
			[][]byte{compressed},
		).bytes()
		got, err := readXZ(file)
		before, beforeErr := readUlikunitz(file)
		switch {
		case c.ok && (err != nil || !bytes.Equal(got, data) || !bytes.Equal(before, data)):
			t.Errorf("a header of %d KiB: %d bytes %v", c.dict>>10, len(got), err)
		case !c.ok && (err == nil || beforeErr == nil || err.Error() != beforeErr.Error()):
			t.Errorf("a header of %d KiB: %v, ulikunitz/xz said %v", c.dict>>10, err, beforeErr)
		}
	}
}
