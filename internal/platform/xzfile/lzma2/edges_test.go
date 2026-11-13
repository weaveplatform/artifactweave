package lzma2

import (
	"bytes"
	"errors"
	"io"
	"testing"
	"testing/iotest"
)

// panics fails t unless f panics.
func panics(t *testing.T, what string, f func()) {
	t.Helper()
	defer func() {
		if recover() == nil {
			t.Errorf("%s did not panic", what)
		}
	}()
	f()
}

// stalled is a reader that never gives a byte and never fails.
type stalled struct{}

func (stalled) Read([]byte) (int, error) { return 0, nil }

// The checks upstream makes of what only a broken caller or input could
// give: a range coder whose first bytes are wrong or missing, a reader that
// stops giving bytes, properties out of range and an operation that is
// neither a match nor a literal.
func TestUnitDecoderEdges(t *testing.T) {
	for _, c := range []struct {
		in   []byte
		want error
	}{
		{[]byte{1, 0, 0, 0, 0}, errFirstByte},
		{[]byte{0, 0xff, 0xff, 0xff, 0xff}, errCodeRange},
		{nil, io.EOF},
		{[]byte{0, 0}, io.EOF},
	} {
		if _, err := newRangeDecoder(bytes.NewReader(c.in)); !errors.Is(err, c.want) {
			t.Errorf("range decoder of % x: %v, want %v", c.in, err, c.want)
		}
	}
	r := bytes.NewReader([]byte{1})
	if ByteReader(r) != io.ByteReader(r) {
		t.Error("ByteReader wrapped a reader that reads bytes")
	}
	for name, c := range map[string]struct {
		r    io.Reader
		want error
	}{
		"one byte":   {iotest.OneByteReader(bytes.NewReader([]byte{7})), nil},
		"at its end": {iotest.OneByteReader(bytes.NewReader(nil)), io.EOF},
		"no bytes":   {stalled{}, io.ErrNoProgress},
	} {
		if _, err := ByteReader(c.r).ReadByte(); !errors.Is(err, c.want) {
			t.Errorf("%s: %v, want %v", name, err, c.want)
		}
	}
	if (match{n: 3}).Len() != 3 || (lit{}).Len() != 1 {
		t.Error("an operation's length")
	}
	panics(t, "a literal codec with lc 9", func() { new(literalCodec).init(maxLC+1, 0) })
	panics(t, "a literal codec with lp 5", func() { new(literalCodec).init(0, maxLP+1) })
	panics(t, "a tree of no bits", func() { makeProbTree(0) })
	dict, err := newDecoderDict(MinDictCap, nil)
	if err != nil {
		t.Fatal(err)
	}
	panics(t, "an operation of another kind", func() { _ = (&decoder{Dict: dict}).apply(nil) })
}

// A dictionary refuses a match of no length or past the longest, one
// before anything is written, and one larger than its room; and a write
// that grow refuses leaves it as it was.
func TestUnitDictionaryEdges(t *testing.T) {
	refused := errors.New("no room")
	grown := 0
	d, err := newDecoderDict(1<<20, func(int) error {
		if grown++; grown > 1 {
			return refused
		}
		return nil
	})
	if err != nil {
		t.Fatal(err)
	}
	if err = d.writeMatch(1, 2); !errors.Is(err, errDistanceRange) {
		t.Errorf("a match into an empty dictionary: %v", err)
	}
	if _, err = d.Write(make([]byte, startCap)); err != nil {
		t.Fatal(err)
	}
	for _, length := range []int{0, maxMatchLen + 1} {
		if err = d.writeMatch(1, length); !errors.Is(err, errLengthRange) {
			t.Errorf("a match of %d: %v", length, err)
		}
	}
	if err = d.writeMatch(1, 2); !errors.Is(err, refused) || d.pos() != startCap {
		t.Errorf("a match grow refuses: %v at %d", err, d.pos())
	}
	if err = d.WriteByte(1); !errors.Is(err, refused) || d.pos() != startCap {
		t.Errorf("a byte grow refuses: %v at %d", err, d.pos())
	}
	if n, err := d.Write([]byte{1}); n != 0 || !errors.Is(err, refused) || d.pos() != startCap {
		t.Errorf("a write grow refuses: %d %v at %d", n, err, d.pos())
	}
	full, err := newDecoderDict(MinDictCap, nil)
	if err != nil {
		t.Fatal(err)
	}
	if _, err = full.Write(make([]byte, MinDictCap)); err != nil {
		t.Fatal(err)
	}
	if err = full.writeMatch(1, 2); !errors.Is(err, ErrNoSpace) {
		t.Errorf("a match into a full dictionary: %v", err)
	}
	if err = full.WriteByte(1); !errors.Is(err, ErrNoSpace) {
		t.Errorf("a byte into a full dictionary: %v", err)
	}
	if _, err = newDecoderDict(
		1<<20,
		func(int) error { return refused },
	); !errors.Is(
		err,
		refused,
	) {
		t.Errorf("a dictionary grow refuses at once: %v", err)
	}
}
