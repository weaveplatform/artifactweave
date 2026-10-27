package remote

import (
	"bytes"
	"compress/zlib"
	"context"
	"crypto"
	"encoding/binary"
	"errors"
	"io"
	"testing"

	"github.com/go-git/go-git/v5/plumbing"
	githash "github.com/go-git/go-git/v5/plumbing/hash"

	"github.com/weaveplatform/artifactweave/internal/source"
)

func TestUnitCocoaPodsPackDecodesObjectsAndDeltas(t *testing.T) {
	t.Parallel()
	base := []byte("hello")
	hash := plumbing.ComputeHash(plumbing.BlobObject, base)
	// Git delta: source length 5, result length 6, copy all five source
	// bytes, then append the literal '!'. These fixtures are independent
	// of go-git's pack encoder and exercise both delta representations.
	delta := []byte{5, 6, 0x90, 5, 1, '!'}
	for _, tc := range []struct {
		name string
		rows []cocoaPodsPackFixture
		want string
	}{
		{"blob", []cocoaPodsPackFixture{{typ: 3, data: base}}, "hello"},
		{"offset delta", []cocoaPodsPackFixture{
			{typ: 3, data: base}, {typ: 6, data: delta},
		}, "hello!"},
		{"reference delta", []cocoaPodsPackFixture{
			{typ: 3, data: base}, {typ: 7, data: delta, ref: hash},
		}, "hello!"},
		{"forward reference delta", []cocoaPodsPackFixture{
			{typ: 7, data: delta, ref: hash}, {typ: 3, data: base},
		}, "hello!"},
	} {
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()
			data := cocoaPodsPackFixtureBytes(t, tc.rows...)
			storage, err := decodeCocoaPodsPack(t.Context(), data, 1024, 20)
			if err != nil {
				t.Fatal(err)
			}
			wantHash := plumbing.ComputeHash(plumbing.BlobObject, []byte(tc.want))
			obj, err := storage.EncodedObject(plumbing.BlobObject, wantHash)
			if err != nil || obj.Size() != int64(len(tc.want)) {
				t.Fatalf("missing expected object %s: %v", wantHash, err)
			}
			r, err := obj.Reader()
			if err != nil {
				t.Fatal(err)
			}
			got, err := io.ReadAll(r)
			_ = r.Close()
			if err != nil || string(got) != tc.want {
				t.Fatalf("object = %q, %v; want %q", got, err, tc.want)
			}
		})
	}
}

func TestUnitCocoaPodsPackRejectsMalformedAndOversizedInput(t *testing.T) {
	t.Parallel()
	valid := cocoaPodsPackFixtureBytes(t, cocoaPodsPackFixture{typ: 3, data: []byte("hello")})
	oversizedDelta := binary.AppendUvarint(nil, 5)
	oversizedDelta = binary.AppendUvarint(oversizedDelta, cocoaPodsObjectBytes+1)
	baseHash := plumbing.ComputeHash(plumbing.BlobObject, []byte("hello"))
	for _, tc := range []struct {
		name string
		data []byte
	}{
		{"empty input", nil},
		{"short header", []byte("PACK")},
		{"empty pack", cocoaPodsPackFixtureBytes(t)},
		{"bad signature", append([]byte("FAIL"), valid[4:]...)},
		{"unsupported version", cocoaPodsPackReplaceCount(valid, 4, 7)},
		{"too many objects", cocoaPodsPackReplaceCount(valid, 8, cocoaPodsPackObjects+1)},
		{"missing object", cocoaPodsPackReplaceCount(valid, 8, 2)},
		{"missing footer", valid[:len(valid)-20]},
		{"bad checksum", append(bytes.Clone(valid[:len(valid)-1]), valid[len(valid)-1]^1)},
		{"trailing bytes", append(bytes.Clone(valid), []byte("extra")...)},
		{"invalid object type", cocoaPodsPackFixtureBytes(t, cocoaPodsPackFixture{typ: 5})},
		{"object declared too big", cocoaPodsPackFixtureBytes(t, cocoaPodsPackFixture{
			typ: 3, declared: cocoaPodsObjectBytes + 1, data: []byte("x"),
		})},
		{"short inflated object", cocoaPodsPackFixtureBytes(t, cocoaPodsPackFixture{
			typ: 3, declared: 6, data: []byte("hello"),
		})},
		{"inflated size overrun", cocoaPodsPackFixtureBytes(t, cocoaPodsPackFixture{
			typ: 3, declared: 1, data: []byte("hello"),
		})},
		{"unresolved reference", cocoaPodsPackFixtureBytes(t, cocoaPodsPackFixture{
			typ: 7, data: []byte{5, 6, 0x90, 5, 1, '!'},
		})},
		{"missing delta sizes", cocoaPodsPackFixtureBytes(t,
			cocoaPodsPackFixture{typ: 3, data: []byte("hello")},
			cocoaPodsPackFixture{typ: 7, ref: baseHash, data: []byte{5}},
		)},
		{"delta target too big", cocoaPodsPackFixtureBytes(t,
			cocoaPodsPackFixture{typ: 3, data: []byte("hello")},
			cocoaPodsPackFixture{typ: 7, ref: baseHash, data: oversizedDelta},
		)},
		{"delta integer overflow", cocoaPodsPackFixtureBytes(t,
			cocoaPodsPackFixture{typ: 7, data: bytes.Repeat([]byte{0xff}, 20)},
		)},
		{"invalid delta command", cocoaPodsPackFixtureBytes(t,
			cocoaPodsPackFixture{typ: 3, data: []byte("hello")},
			cocoaPodsPackFixture{typ: 7, ref: baseHash, data: []byte{5, 6, 0}},
		)},
	} {
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()
			storage, err := decodeCocoaPodsPack(t.Context(), tc.data, 256<<20, 10000)
			if storage != nil || !errors.Is(err, source.ErrInvalidResult) {
				t.Fatalf("invalid pack returned storage=%v, error=%v", storage, err)
			}
		})
	}
}

func TestUnitCocoaPodsPackPreservesRemainingFetchBudget(t *testing.T) {
	t.Parallel()
	base := []byte("hello")
	hash := plumbing.ComputeHash(plumbing.BlobObject, base)
	plain := cocoaPodsPackFixtureBytes(t,
		cocoaPodsPackFixture{typ: 3, data: bytes.Repeat([]byte("a"), 40)},
		cocoaPodsPackFixture{typ: 3, data: bytes.Repeat([]byte("b"), 40)},
	)
	delta := cocoaPodsPackFixtureBytes(t,
		cocoaPodsPackFixture{typ: 3, data: base},
		cocoaPodsPackFixture{typ: 7, ref: hash, data: []byte{5, 6, 0x90, 5, 1, '!'}},
	)
	for _, tc := range []struct {
		name    string
		data    []byte
		bytes   int64
		objects int
	}{
		{"zero bytes", plain, 0, 2},
		{"negative bytes", plain, -1, 2},
		{"no objects left", plain, 80, 0},
		{"too few objects left", plain, 80, 1},
		{"aggregate inflation", plain, 79, 2},
		{"delta instructions and result", delta, 16, 2},
	} {
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()
			storage, err := decodeCocoaPodsPack(t.Context(), tc.data, tc.bytes, tc.objects)
			if storage != nil || !errors.Is(err, source.ErrInvalidResult) {
				t.Fatalf("budget overflow returned storage=%v, error=%v", storage, err)
			}
		})
	}
	if _, err := decodeCocoaPodsPack(t.Context(), plain, 80, 2); err != nil {
		t.Fatalf("exact remaining budget should work: %v", err)
	}
	if _, err := decodeCocoaPodsPack(t.Context(), delta, 17, 2); err != nil {
		t.Fatalf("exact delta working budget should work: %v", err)
	}
}

func TestUnitCocoaPodsPackCancellation(t *testing.T) {
	ctx, cancel := context.WithCancel(t.Context())
	cancel()
	data := cocoaPodsPackFixtureBytes(t, cocoaPodsPackFixture{typ: 3, data: []byte("hello")})
	storage, err := decodeCocoaPodsPack(ctx, data, 1024, 20)
	if storage != nil || !errors.Is(err, context.Canceled) {
		t.Fatalf("cancelled fetch returned storage=%v, error=%v", storage, err)
	}
}

type cocoaPodsPackFixture struct {
	typ      byte
	data     []byte
	declared uint64
	ref      plumbing.Hash
}

func cocoaPodsPackFixtureBytes(t *testing.T, rows ...cocoaPodsPackFixture) []byte {
	t.Helper()
	data := []byte("PACK")
	data = binary.BigEndian.AppendUint32(data, 2)
	data = binary.BigEndian.AppendUint32(data, uint32(len(rows)))
	previous := 0
	for _, row := range rows {
		offset := len(data)
		length := row.declared
		if length == 0 {
			length = uint64(len(row.data))
		}
		first := row.typ<<4 | byte(length&15)
		length >>= 4
		if length > 0 {
			first |= 0x80
		}
		data = append(data, first)
		for length > 0 {
			b := byte(length & 0x7f)
			length >>= 7
			if length > 0 {
				b |= 0x80
			}
			data = append(data, b)
		}
		if row.typ == 7 {
			data = append(data, row.ref[:]...)
		} else if row.typ == 6 {
			distance := offset - previous
			if distance <= 0 || distance >= 128 {
				t.Fatal("fixture requires a one-byte delta offset")
			}
			data = append(data, byte(distance))
		}
		var compressed bytes.Buffer
		w := zlib.NewWriter(&compressed)
		_, _ = w.Write(row.data)
		if err := w.Close(); err != nil {
			t.Fatal(err)
		}
		data = append(data, compressed.Bytes()...)
		previous = offset
	}
	return cocoaPodsPackChecksum(data)
}

func cocoaPodsPackChecksum(data []byte) []byte {
	h := githash.New(crypto.SHA1)
	_, _ = h.Write(data)
	return h.Sum(data)
}

func cocoaPodsPackReplaceCount(data []byte, offset int, value uint32) []byte {
	out := bytes.Clone(data[:len(data)-20])
	binary.BigEndian.PutUint32(out[offset:], value)
	return cocoaPodsPackChecksum(out)
}
