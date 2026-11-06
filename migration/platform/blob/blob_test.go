package blob

import (
	"bytes"
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
