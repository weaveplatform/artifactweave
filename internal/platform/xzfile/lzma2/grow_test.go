package lzma2

import (
	"bytes"
	"errors"
	"io"
	"math/rand/v2"
	"runtime"
	"slices"
	"testing"

	"github.com/ulikunitz/xz/lzma"
)

// fullBuffer is the buffer as upstream builds it: all of its size at
// once, so it never grows.
func fullBuffer(size int) *buffer {
	return &buffer{data: make([]byte, size+1), size: size + 1}
}

// A buffer that grows answers every write, read and room check as one of
// its full size does, through its growth and, once grown, its wraps, for
// sizes below, at and above the size it starts with.
func TestUnitAGrowingBufferAnswersAsAFullOne(t *testing.T) {
	for _, size := range []int{MinDictCap, startCap - 1, startCap, startCap + 1, 3*startCap + 7, 8 * startCap} {
		rng := rand.New(rand.NewPCG(uint64(size), 7))
		var grew []int
		got, err := newBuffer(size, func(c int) error { grew = append(grew, c); return nil })
		if err != nil {
			t.Fatal(err)
		}
		want := fullBuffer(size)
		written, last := 0, 0
		for written < 4*size {
			switch op := rng.IntN(3); op {
			case 0:
				p := make([]byte, rng.IntN(size/2+300))
				for i := range p {
					p[i] = byte(written + i)
				}
				n, err := got.Write(p)
				wn, werr := want.Write(p)
				if n != wn || !errors.Is(err, werr) {
					t.Fatalf("size %d: Write %d: %d %v, want %d %v", size, len(p), n, err, wn, werr)
				}
				written += n
			case 1:
				err := got.WriteByte(byte(written))
				if werr := want.WriteByte(byte(written)); !errors.Is(err, werr) {
					t.Fatalf("size %d: WriteByte: %v, want %v", size, err, werr)
				}
				if err == nil {
					written++
				}
			default:
				p := make([]byte, rng.IntN(size))
				q := make([]byte, len(p))
				n, _ := got.Read(p)
				wn, _ := want.Read(q)
				if n != wn || !bytes.Equal(p[:n], q[:wn]) {
					t.Fatalf("size %d: Read %d: %d bytes, want %d", size, len(p), n, wn)
				}
			}
			if got.Buffered() != want.Buffered() || got.Available() != want.Available() ||
				got.front != want.front || got.rear != want.rear {
				t.Fatalf(
					"size %d: buffered %d available %d front %d rear %d, want %d %d %d %d",
					size,
					got.Buffered(),
					got.Available(),
					got.front,
					got.rear,
					want.Buffered(),
					want.Available(),
					want.front,
					want.rear,
				)
			}
			if len(grew) > 0 {
				last = grew[len(grew)-1]
			}
		}
		if len(got.data) != size+1 || last != size || grew[0] != min(size, startCap) {
			t.Errorf("size %d: grew %v to %d bytes", size, grew, len(got.data))
		}
		for i := 1; i < len(grew); i++ {
			if grew[i] < min(2*grew[i-1], size) {
				t.Errorf("size %d: grew %v, by less than twice", size, grew)
			}
		}
	}
}

// pack compresses data as an LZMA2 chunk sequence with upstream's Writer2
// and a dictionary of dictCap.
func pack(t testing.TB, dictCap int, data []byte) []byte {
	t.Helper()
	out, err := packed(dictCap, data)
	if err != nil {
		t.Fatal(err)
	}
	return out
}

func packed(dictCap int, data []byte) ([]byte, error) {
	var b bytes.Buffer
	w, err := lzma.Writer2Config{DictCap: dictCap}.NewWriter2(&b)
	if err != nil {
		return nil, err
	}
	if _, err = w.Write(data); err != nil {
		return nil, err
	}
	if err = w.Close(); err != nil {
		return nil, err
	}
	return b.Bytes(), nil
}

// raw is an uncompressed chunk of data, which resets the dictionary if
// reset is set.
func raw(reset bool, data []byte) []byte {
	h := byte(hU)
	if reset {
		h = hUD
	}
	return append([]byte{h, byte((len(data) - 1) >> 8), byte(len(data) - 1)}, data...)
}

func random(seed uint64, n int) []byte {
	rng := rand.New(rand.NewPCG(seed, 1))
	b := make([]byte, n)
	for i := range b {
		b[i] = byte(rng.Uint32())
	}
	return b
}

// repeated is random bytes followed by the same bytes again: every match
// in the second half reaches back len(random) bytes.
func repeated(seed uint64, n int) []byte {
	b := random(seed, n)
	return append(b, b...)
}

// Grow is called with each capacity before the dictionary is allocated:
// the 64 KiB it starts with, or less if the dictionary is smaller, then
// each doubling up to DictCap, in compressed and in uncompressed chunks.
func TestUnitGrowCapacities(t *testing.T) {
	big := text(3, 600<<10)
	for _, c := range []struct {
		name    string
		dictCap int
		stream  []byte
		want    []int
	}{
		{"text, 1 MiB", 1 << 20, pack(t, 1<<20, big), []int{64 << 10, 128 << 10, 256 << 10, 512 << 10, 1 << 20}},
		{"text, 100000 bytes", 100000, pack(t, 64<<10, big[:150000]), []int{64 << 10, 100000}},
		{"text, 4 KiB", MinDictCap, pack(t, MinDictCap, big[:20000]), []int{MinDictCap}},
		{"three bytes, 1.5 GiB", 3 << 29, pack(t, MinDictCap, []byte("abc")), []int{64 << 10}},
		{"uncompressed, 1 MiB", 1 << 20, slices.Concat(
			raw(true, big[:60000]), raw(false, big[60000:120000]), raw(false, big[120000:180000]), []byte{0},
		), []int{64 << 10, 128 << 10, 256 << 10}},
	} {
		var grew []int
		r, err := Reader2Config{DictCap: c.dictCap, Grow: func(n int) error {
			grew = append(grew, n)
			return nil
		}}.NewReader2(bytes.NewReader(c.stream))
		if err != nil {
			t.Fatal(err)
		}
		if _, err = io.ReadAll(r); err != nil {
			t.Fatalf("%s: %v", c.name, err)
		}
		if !slices.Equal(grew, c.want) {
			t.Errorf("%s: grew %v, want %v", c.name, grew, c.want)
		}
	}
}

// A refusal from Grow ends NewReader2 at the start, and the Read that
// needed the room, compressed or uncompressed, with Grow's error, and
// every Read after it.
func TestUnitGrowRefusal(t *testing.T) {
	refused := errors.New("no room")
	refuseAfter := func(n int) func(int) error {
		return func(int) error {
			if n == 0 {
				return refused
			}
			n--
			return nil
		}
	}
	if _, err := (Reader2Config{DictCap: 1 << 20, Grow: refuseAfter(0)}).NewReader2(
		bytes.NewReader(pack(t, 1<<20, []byte("abc"))),
	); !errors.Is(err, refused) {
		t.Fatalf("at the start: %v, want %v", err, refused)
	}
	data := text(5, 200<<10)
	for name, stream := range map[string][]byte{
		"compressed":   pack(t, 1<<20, data),
		"uncompressed": slices.Concat(raw(true, data[:60000]), raw(false, data[60000:120000]), []byte{0}),
	} {
		r, err := Reader2Config{
			DictCap: 1 << 20,
			Grow:    refuseAfter(1),
		}.NewReader2(
			bytes.NewReader(stream),
		)
		if err != nil {
			t.Fatal(err)
		}
		got, err := io.ReadAll(r)
		if !errors.Is(err, refused) || !bytes.Equal(got, data[:len(got)]) || len(got) > 64<<10 {
			t.Errorf("%s: %d bytes, %v; want at most 64 KiB, then %v", name, len(got), err, refused)
		}
		if n, err := r.Read(make([]byte, 1)); n != 0 || !errors.Is(err, refused) {
			t.Errorf("%s: a read after the refusal: %d %v", name, n, err)
		}
	}
}

// A 52-byte stream whose dictionary is 1.5 GiB, the most an xz block
// header may name, decodes with 64 KiB of dictionary: ulikunitz/xz's
// Reader2 allocates all 1.5 GiB first (#1413).
func TestUnitAShortStreamAllocatesLittle(t *testing.T) {
	stream := pack(t, MinDictCap, []byte("a short file"))
	var before, after runtime.MemStats
	runtime.ReadMemStats(&before)
	r, err := Reader2Config{DictCap: 3 << 29}.NewReader2(bytes.NewReader(stream))
	if err != nil {
		t.Fatal(err)
	}
	got, err := io.ReadAll(r)
	runtime.ReadMemStats(&after)
	if err != nil || string(got) != "a short file" {
		t.Fatalf("%q %v", got, err)
	}
	if n := after.TotalAlloc - before.TotalAlloc; n >= 1<<20 {
		t.Errorf("allocated %d KiB, want less than 1 MiB", n>>10)
	}
	if n := len(r.dict.buf.data); n > startCap+1 {
		t.Errorf("the dictionary grew to %d bytes", n)
	}
}

// readResult is what one Read gave.
type readResult struct {
	data []byte
	err  string
}

// reads reads r to its end or its first error, each Read into the next of
// sizes in turn, and returns what each gave.
func reads(r io.Reader, sizes []int) []readResult {
	var out []readResult
	for i := 0; ; i++ {
		p := make([]byte, sizes[i%len(sizes)])
		n, err := r.Read(p)
		res := readResult{data: p[:n]}
		if err != nil {
			res.err = err.Error()
		}
		out = append(out, res)
		if err != nil || i > 1<<20 {
			return out
		}
	}
}

// sameAnswers fails t unless this Reader2 and ulikunitz/xz's answer every
// Read of stream alike, for a dictionary of dictCap.
func sameAnswers(t *testing.T, name string, dictCap int, stream []byte, sizes []int) bool {
	t.Helper()
	ours, err := Reader2Config{DictCap: dictCap}.NewReader2(bytes.NewReader(stream))
	theirs, theirErr := lzma.Reader2Config{DictCap: dictCap}.NewReader2(bytes.NewReader(stream))
	if err != nil || theirErr != nil {
		if err == nil || theirErr == nil || err.Error() != theirErr.Error() {
			t.Errorf("%s: NewReader2 %v, ulikunitz/xz %v", name, err, theirErr)
			return false
		}
		return true
	}
	got, want := reads(ours, sizes), reads(theirs, sizes)
	if !slices.EqualFunc(got, want, func(a, b readResult) bool {
		return bytes.Equal(a.data, b.data) && a.err == b.err
	}) {
		t.Errorf("%s, dictionary %d: %d reads ending %q, ulikunitz/xz %d ending %q",
			name, dictCap, len(got), got[len(got)-1].err, len(want), want[len(want)-1].err)
		return false
	}
	return true
}

// This Reader2 answers as ulikunitz/xz's does, read by read: streams of
// text, of random bytes, of matches that reach back across the growth, to
// the whole dictionary and past it, of uncompressed chunks, of dictionary
// resets and of a chunk that keeps the dictionary where the encoder reset
// it; with dictionaries smaller than, equal to and larger than what they
// reach; and every one of them cut short and with bytes changed.
func TestUnitSameAnswersAsUlikunitz(t *testing.T) {
	keep := pack(t, 1<<20, text(9, 70000))
	keep[0] = hLRN | keep[0]&^hLRND
	streams := map[string][]byte{
		"text":                 pack(t, 1<<20, text(7, 200<<10)),
		"random":               pack(t, 1<<20, random(8, 100<<10)),
		"matches 96 KiB back":  pack(t, 1<<20, repeated(10, 96<<10)),
		"matches 160 KiB back": pack(t, 1<<20, repeated(11, 160<<10)),
		"empty":                pack(t, MinDictCap, nil),
		"uncompressed and a reset": slices.Concat(
			raw(true, text(12, 50000)),
			raw(false, text(13, 40000)),
			pack(t, 1<<20, repeated(14, 70000)),
		),
		"a kept dictionary": slices.Concat(raw(true, text(15, 65000)), keep),
	}
	sizes := [][]int{{1 << 20}, {1, 7, 4096, 65536, 300}}
	for name, stream := range streams {
		for _, dictCap := range []int{MinDictCap, 96<<10 - 1, 96 << 10, 160 << 10, 1 << 20} {
			for _, s := range sizes {
				sameAnswers(t, name, dictCap, stream, s)
			}
		}
		step := max(1, len(stream)/24)
		for cut := 1; cut < len(stream); cut += step {
			sameAnswers(t, name+" cut", 160<<10, stream[:cut], sizes[1])
		}
		for i := 0; i < len(stream); i += step {
			broken := slices.Clone(stream)
			broken[i] ^= 0x41
			sameAnswers(t, name+" changed", 160<<10, broken, sizes[1])
		}
	}
}

// Matches that reach back the whole dictionary decode as they do with
// ulikunitz/xz's, and one that reaches a byte further is refused in its
// words, though the dictionary has grown through four sizes to reach it.
func TestUnitMatchesReachTheWholeDictionary(t *testing.T) {
	data := repeated(16, 256<<10)
	stream := pack(t, 1<<20, data)
	whole, err := Reader2Config{DictCap: 256 << 10}.NewReader2(bytes.NewReader(stream))
	if err != nil {
		t.Fatal(err)
	}
	if got, err := io.ReadAll(whole); err != nil || !bytes.Equal(got, data) {
		t.Fatalf("a dictionary of 256 KiB: %d bytes, %v", len(got), err)
	}
	short, err := Reader2Config{DictCap: 256<<10 - 1}.NewReader2(bytes.NewReader(stream))
	if err != nil {
		t.Fatal(err)
	}
	if _, err := io.ReadAll(
		short,
	); err == nil ||
		err.Error() != "writeMatch: distance out of range" {
		t.Fatalf("a dictionary a byte short: %v", err)
	}
	sameAnswers(t, "the whole dictionary", 256<<10, stream, []int{1 << 20})
	sameAnswers(t, "a byte past it", 256<<10-1, stream, []int{1 << 20})
}

// A configuration whose dictionary is out of range is refused, and one
// that names none takes upstream's 8 MiB.
func TestUnitReader2Config(t *testing.T) {
	for _, dictCap := range []int{1, MinDictCap - 1, -1} {
		if _, err := (Reader2Config{DictCap: dictCap}).NewReader2(
			bytes.NewReader(nil),
		); !errors.Is(
			err,
			errDictCapRange,
		) {
			t.Errorf("dictionary %d: %v", dictCap, err)
		}
	}
	r, err := Reader2Config{}.NewReader2(bytes.NewReader(pack(t, MinDictCap, []byte("x"))))
	if err != nil || r.dict.buf.Cap() != 8<<20 {
		t.Fatalf("the default dictionary: %v", err)
	}
	if _, err := newDecoderDict(MaxDictCap+1, nil); !errors.Is(err, errDictCap) {
		t.Errorf("a dictionary past MaxDictCap: %v", err)
	}
}
