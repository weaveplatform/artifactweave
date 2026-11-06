package blob

import (
	"bytes"
	"crypto/sha256"
	"encoding/hex"
	"errors"
	"io"
	"strings"
	"testing"
)

var errInjected = errors.New("injected")

func TestUnitHasherSaveRestoreContinues(t *testing.T) {
	whole := NewHasher()
	_, _ = whole.Write([]byte("hello, world"))

	first := NewHasher()
	_, _ = first.Write([]byte("hello, "))
	saved, err := first.MarshalBinary()
	if err != nil {
		t.Fatal(err)
	}
	resumed := NewHasher()
	if err := resumed.UnmarshalBinary(saved); err != nil {
		t.Fatal(err)
	}
	_, _ = resumed.Write([]byte("world"))
	if resumed.Sum() != whole.Sum() {
		t.Fatalf("resumed %+v, whole %+v", resumed.Sum(), whole.Sum())
	}
}

// hasherKnownStateSHA256 is the SHA-256 of the state a new Hasher saves
// after reading "artifactweave upload, part one; ". Upload sessions persist
// that state between requests and processes, so a session saved by one
// build must restore in the next. A round trip within one build, as in
// TestUnitHasherSaveRestoreContinues, cannot see a change of encoding.
const hasherKnownStateSHA256 = "ce3f466f0c53391dfc65c9420c5feb2dc9e4bbdffeb8a701cdd095c101608d50"

func TestUnitHasherStateKnownAnswer(t *testing.T) {
	const head, tail = "artifactweave upload, part one; ", "part two"
	h := NewHasher()
	_, _ = h.Write([]byte(head))
	state, err := h.MarshalBinary()
	if err != nil {
		t.Fatal(err)
	}
	// The hash below covers the framing too; checking it first names the
	// part that changed. Each standard library state starts with its magic.
	rest := state
	for _, part := range []struct {
		name, magic string
		size        int
	}{
		{"SHA-256", "sha\x03", 108},
		{"SHA-512", "sha\x07", 204},
		{"SHA-1", "sha\x01", 96},
		{"MD5", "md5\x01", 92},
	} {
		if len(rest) < 2+len(part.magic) {
			t.Fatalf("%s state: %d bytes left, want a length and a state", part.name, len(rest))
		}
		n := int(rest[0])<<8 | int(rest[1])
		if n != part.size || string(rest[2:2+len(part.magic)]) != part.magic {
			t.Fatalf("%s state: length %d and magic %q, want %d and %q",
				part.name, n, rest[2:2+len(part.magic)], part.size, part.magic)
		}
		rest = rest[min(len(rest), 2+n):]
	}
	if len(rest) != 0 {
		t.Fatalf("%d bytes after the MD5 state, want none", len(rest))
	}
	sum := sha256.Sum256(state)
	if got := hex.EncodeToString(sum[:]); got != hasherKnownStateSHA256 {
		t.Fatalf("the saved state changed: its SHA-256 is %s, want %s", got, hasherKnownStateSHA256)
	}

	// The saved state continues the stream: the digests are those of head
	// and tail together, as shasum and md5 compute them.
	resumed := NewHasher()
	if err := resumed.UnmarshalBinary(state); err != nil {
		t.Fatal(err)
	}
	_, _ = resumed.Write([]byte(tail))
	want := Digests{
		SHA256: "588b9b19885591f15d7a2913a4e65fe636c92567f213874a5e6481ebcad7473b",
		SHA512: "877f0741f1af2c0b0b5699bb1cddaba3b2a13e804057aa13358e3d4f570e05b3" +
			"9f4a94e932db030b68f5309545b7cd3132fb2f77aae08dfd524f94d6e1ce7686",
		SHA1: "7862a220a899a8afa8dc21a573753e6097a26229",
		MD5:  "50112c50eaac9359c718c69d557b4a24",
	}
	if got := resumed.Sum(); got != want {
		t.Fatalf("resumed digests %+v, want %+v", got, want)
	}
}

func TestUnitHasherRejectsBadState(t *testing.T) {
	good, err := NewHasher().MarshalBinary()
	if err != nil {
		t.Fatal(err)
	}
	cases := map[string][]byte{
		"empty":           nil,
		"truncated len":   good[:1],
		"truncated body":  good[:10],
		"trailing bytes":  append(append([]byte{}, good...), 0),
		"corrupt payload": append([]byte{0, 3}, []byte("bad")...),
	}
	for name, b := range cases {
		h := NewHasher()
		before := h.Sum()
		if err := h.UnmarshalBinary(b); !errors.Is(err, ErrHasherState) {
			t.Errorf("%s: %v", name, err)
		}
		if h.Sum() != before {
			t.Errorf("%s: a failed restore must leave the hasher untouched", name)
		}
	}
}

func TestUnitVerify(t *testing.T) {
	got := Digests{SHA256: "a", SHA1: "b", MD5: "c"}
	if err := Verify(Digests{}, got); err != nil {
		t.Fatal(err)
	}
	if err := Verify(got, got); err != nil {
		t.Fatal(err)
	}
	for _, want := range []Digests{{SHA256: "x"}, {SHA1: "x"}, {MD5: "x"}} {
		if err := Verify(want, got); !errors.Is(err, ErrDigestMismatch) {
			t.Errorf("%+v: %v", want, err)
		}
	}
}

func TestUnitValidate(t *testing.T) {
	ok := Digests{
		SHA256: strings.Repeat("a", 64),
		SHA1:   strings.Repeat("b", 40),
		MD5:    strings.Repeat("c", 32),
	}
	if err := ValidateDigests(ok); err != nil {
		t.Fatal(err)
	}
	if err := ValidateDigests(Digests{}); err != nil {
		t.Fatal(err)
	}
	for _, bad := range []Digests{
		{SHA256: strings.Repeat("A", 64)},
		{SHA1: strings.Repeat("b", 39)},
		{MD5: "g" + strings.Repeat("c", 31)},
	} {
		if err := ValidateDigests(bad); !errors.Is(err, ErrInvalidDigest) {
			t.Errorf("%+v: %v", bad, err)
		}
	}
	if ValidUploadID("../" + strings.Repeat("a", 29)) {
		t.Fatal("path characters in an upload id")
	}
}

func TestUnitResolveOptions(t *testing.T) {
	sha := strings.Repeat("d", 64)
	o, err := ResolveOptions(WithExpectedSHA256(sha))
	if err != nil || o.ExpectedSHA256 != sha {
		t.Fatalf("%+v %v", o, err)
	}
	if _, err := ResolveOptions(WithExpectedSHA256("x")); !errors.Is(err, ErrInvalidDigest) {
		t.Fatal(err)
	}
}

type shortWriter struct{ limit int }

func (w *shortWriter) Write(p []byte) (int, error) {
	n := min(len(p), w.limit)
	w.limit -= n
	return n, nil
}

type errWriter struct{ after int }

func (w *errWriter) Write(p []byte) (int, error) {
	n := min(len(p), w.after)
	w.after -= n
	if n < len(p) {
		return n, errInjected
	}
	return n, nil
}

type errReader struct{}

func (errReader) Read([]byte) (int, error) { return 0, errInjected }

func TestUnitCopyHashedKeepsHashInStepWithWrites(t *testing.T) {
	body := bytes.Repeat([]byte("z"), 1000)
	cases := map[string]struct {
		w       io.Writer
		r       io.Reader
		stored  int64
		wantErr error
	}{
		"complete":     {&bytes.Buffer{}, bytes.NewReader(body), 1000, nil},
		"short write":  {&shortWriter{limit: 300}, bytes.NewReader(body), 300, io.ErrShortWrite},
		"write error":  {&errWriter{after: 400}, bytes.NewReader(body), 400, errInjected},
		"read error":   {&bytes.Buffer{}, errReader{}, 0, errInjected},
		"empty reader": {&bytes.Buffer{}, bytes.NewReader(nil), 0, nil},
	}
	for name, tc := range cases {
		t.Run(name, func(t *testing.T) {
			h := NewHasher()
			n, err := CopyHashed(tc.w, tc.r, h)
			if n != tc.stored || !errors.Is(err, tc.wantErr) || (tc.wantErr == nil && err != nil) {
				t.Fatalf("n %d err %v", n, err)
			}
			ref := NewHasher()
			_, _ = ref.Write(body[:n])
			if h.Sum() != ref.Sum() {
				t.Fatal("hash covers bytes that were not stored")
			}
		})
	}
}

func TestUnitNewUploadID(t *testing.T) {
	id, err := NewUploadID()
	if err != nil || !ValidUploadID(id) {
		t.Fatalf("%q %v", id, err)
	}
	old := randReader
	randReader = errReader{}
	t.Cleanup(func() { randReader = old })
	if _, err := NewUploadID(); !errors.Is(err, errInjected) {
		t.Fatal(err)
	}
}
