package crypt

import (
	"bytes"
	"encoding/base64"
	"errors"
	"strings"
	"testing"

	"github.com/google/go-cmp/cmp"
)

var (
	masterA = bytes.Repeat([]byte("a"), 32)
	masterB = bytes.Repeat([]byte("b"), 32)
)

func ring(t *testing.T, material []byte) *Keyring {
	t.Helper()
	k, err := New(material)
	if err != nil {
		t.Fatal(err)
	}
	return k
}

func TestSealOpenRoundTrip(t *testing.T) {
	k := ring(t, masterA)
	for _, plain := range [][]byte{[]byte("hunter2"), {}, bytes.Repeat([]byte{0}, 4096)} {
		sealed, err := k.Seal("repository.remote.password", "maven-remote", plain)
		if err != nil {
			t.Fatal(err)
		}
		if !IsSealed(sealed) || bytes.Contains(sealed, []byte("hunter2")) {
			t.Fatal("sealed value leaks plaintext or lacks the header")
		}
		got, err := k.Open("repository.remote.password", "maven-remote", sealed)
		if err != nil || !bytes.Equal(got, plain) {
			t.Fatalf("open: %q %v", got, err)
		}
	}
	a, _ := k.Seal("p", "r", []byte("same"))
	b, _ := k.Seal("p", "r", []byte("same"))
	if bytes.Equal(a, b) {
		t.Fatal("nonces must differ between seals")
	}
}

func TestOpenRejects(t *testing.T) {
	k := ring(t, masterA)
	sealed, err := k.Seal("webhook.secret", "hook-1", []byte("s3cret"))
	if err != nil {
		t.Fatal(err)
	}
	flip := func(i int) []byte {
		b := append([]byte(nil), sealed...)
		b[i] ^= 1
		return b
	}
	cases := []struct {
		name         string
		purpose, row string
		value        []byte
		ring         *Keyring
		want         error
	}{
		{"other purpose", "keypair.private", "hook-1", sealed, k, ErrTampered},
		{"other row", "webhook.secret", "hook-2", sealed, k, ErrTampered},
		{"flipped ciphertext", "webhook.secret", "hook-1", flip(len(sealed) - 1), k, ErrTampered},
		{"flipped nonce", "webhook.secret", "hook-1", flip(headerLen), k, ErrTampered},
		{"flipped key id", "webhook.secret", "hook-1", flip(magicLen + 4), k, ErrWrongKey},
		{
			"different master, same version",
			"webhook.secret",
			"hook-1",
			sealed,
			ring(t, masterB),
			ErrWrongKey,
		},
		{"unknown version", "webhook.secret", "hook-1", flip(magicLen + 3), k, ErrUnknownKey},
		{"bad magic", "webhook.secret", "hook-1", flip(1), k, ErrBadFormat},
		{"too short", "webhook.secret", "hook-1", sealed[:headerLen+nonceLen], k, ErrBadFormat},
		{"plaintext", "webhook.secret", "hook-1", []byte("s3cret"), k, ErrBadFormat},
		{"empty purpose", "", "hook-1", sealed, k, ErrBadPurpose},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			if _, err := tc.ring.Open(tc.purpose, tc.row, tc.value); !errors.Is(err, tc.want) {
				t.Fatalf("got %v, want %v", err, tc.want)
			}
		})
	}
	if _, err := k.Seal("", "r", nil); !errors.Is(err, ErrBadPurpose) {
		t.Fatal(err)
	}
}

func TestPurposeAndRowCannotBeShifted(t *testing.T) {
	k := ring(t, masterA)
	sealed, _ := k.Seal("ab", "c", []byte("x"))
	if _, err := k.Open("a", "bc", sealed); !errors.Is(err, ErrTampered) {
		t.Fatalf("boundary between purpose and row must be authenticated: %v", err)
	}
}

func TestRotation(t *testing.T) {
	v1 := ring(t, masterA)
	old, _ := v1.Seal("p", "r", []byte("before"))

	v2, err := v1.Rotate(masterB)
	if err != nil {
		t.Fatal(err)
	}
	if v1.Active() != 1 || v2.Active() != 2 {
		t.Fatalf("active versions %d %d", v1.Active(), v2.Active())
	}
	if diff := cmp.Diff([]uint32{1, 2}, v2.Versions()); diff != "" {
		t.Fatal(diff)
	}
	if got, err := v2.Open("p", "r", old); err != nil || string(got) != "before" {
		t.Fatalf("rotated ring must open old values: %q %v", got, err)
	}
	fresh, _ := v2.Seal("p", "r", []byte("after"))
	if ver, _ := Version(fresh); ver != 2 {
		t.Fatalf("new seals use version %d", ver)
	}
	if _, err := v1.Open("p", "r", fresh); !errors.Is(err, ErrUnknownKey) {
		t.Fatalf("the old ring cannot know the new version: %v", err)
	}

	resealed, changed, err := v2.Reseal("p", "r", old)
	if err != nil || !changed {
		t.Fatalf("reseal old: %v %v", changed, err)
	}
	if ver, _ := Version(resealed); ver != 2 {
		t.Fatalf("reseal left version %d", ver)
	}
	same, changed, err := v2.Reseal("p", "r", resealed)
	if err != nil || changed || !bytes.Equal(same, resealed) {
		t.Fatalf("reseal current: %v %v", changed, err)
	}
	if _, _, err := v2.Reseal("p", "other", resealed); !errors.Is(err, ErrTampered) {
		t.Fatalf("reseal must authenticate current values too: %v", err)
	}
	if _, _, err := v2.Reseal("p", "other", old); !errors.Is(err, ErrTampered) {
		t.Fatalf("reseal of a moved old value: %v", err)
	}
	if _, _, err := v2.Reseal("p", "r", []byte("plain")); !errors.Is(err, ErrBadFormat) {
		t.Fatal(err)
	}

	// After every value is resealed the operator restarts with only the new
	// key, as version 2.
	only2, err := NewKeyring(MasterKey{Version: 2, Material: masterB})
	if err != nil {
		t.Fatal(err)
	}
	if got, err := only2.Open("p", "r", resealed); err != nil || string(got) != "before" {
		t.Fatalf("new key alone: %q %v", got, err)
	}
	if _, err := v1.Rotate([]byte("short")); !errors.Is(err, ErrWeakKey) {
		t.Fatal(err)
	}
}

func TestResealSealFailure(t *testing.T) {
	v1 := ring(t, masterA)
	old, _ := v1.Seal("p", "r", []byte("x"))
	v2, _ := v1.Rotate(masterB)
	withRand(t, failingReader{})
	if _, _, err := v2.Reseal("p", "r", old); err == nil {
		t.Fatal("a nonce failure must surface")
	}
}

func TestNewKeyringValidates(t *testing.T) {
	cases := map[string]struct {
		keys []MasterKey
		want error
	}{
		"none":      {nil, ErrNoKeys},
		"version 0": {[]MasterKey{{Version: 0, Material: masterA}}, ErrBadVersion},
		"duplicate": {
			[]MasterKey{{Version: 1, Material: masterA}, {Version: 1, Material: masterB}},
			ErrBadVersion,
		},
		"weak": {[]MasterKey{{Version: 1, Material: []byte("short")}}, ErrWeakKey},
	}
	for name, tc := range cases {
		if _, err := NewKeyring(tc.keys...); !errors.Is(err, tc.want) {
			t.Errorf("%s: %v", name, err)
		}
	}
	k, err := NewKeyring(
		MasterKey{Version: 3, Material: masterA},
		MasterKey{Version: 1, Material: masterB},
	)
	if err != nil || k.Active() != 3 {
		t.Fatalf("the highest version seals: %v", err)
	}
}

func TestParseMasterKey(t *testing.T) {
	raw32 := bytes.Repeat([]byte{0xfe}, 32)
	cases := map[string]struct {
		in   string
		want []byte
		err  error
	}{
		"std base64":    {base64.StdEncoding.EncodeToString(raw32), raw32, nil},
		"raw url b64":   {base64.RawURLEncoding.EncodeToString(raw32), raw32, nil},
		"padded spaces": {"  " + base64.StdEncoding.EncodeToString(raw32) + "\n", raw32, nil},
		"passphrase": {
			strings.Repeat("correct horse ", 3),
			[]byte(strings.TrimSpace(strings.Repeat("correct horse ", 3))),
			nil,
		},
		// Valid base64 that decodes too short is treated as a raw passphrase.
		"short b64 long text": {strings.Repeat("QUJD", 8), []byte(strings.Repeat("QUJD", 8)), nil},
		"too short":           {"short", nil, ErrWeakKey},
	}
	for name, tc := range cases {
		got, err := ParseMasterKey(tc.in)
		if !errors.Is(err, tc.err) || (tc.err == nil && !bytes.Equal(got, tc.want)) {
			t.Errorf("%s: %q %v", name, got, err)
		}
	}
}

type failingReader struct{}

func (failingReader) Read([]byte) (int, error) { return 0, errors.New("no entropy") }

func withRand(t *testing.T, r interface{ Read([]byte) (int, error) }) {
	t.Helper()
	old := randReader
	randReader = r
	t.Cleanup(func() { randReader = old })
}

func TestSealNonceFailure(t *testing.T) {
	k := ring(t, masterA)
	withRand(t, failingReader{})
	if _, err := k.Seal("p", "r", []byte("x")); err == nil {
		t.Fatal("a nonce failure must not produce a sealed value")
	}
}

func TestMasking(t *testing.T) {
	if Mask("") != "" || Mask("pw") != Masked {
		t.Fatal("mask")
	}
	if KeepIfMasked(Masked, "stored") != "stored" || KeepIfMasked("new", "stored") != "new" ||
		KeepIfMasked("", "stored") != "" {
		t.Fatal("keep if masked")
	}
}

func TestParseVersionedKey(t *testing.T) {
	const raw = "0123456789abcdef0123456789abcdef"
	for _, tc := range []struct {
		in      string
		version uint32
		err     error
	}{
		{raw, 1, nil},
		{"v1:" + raw, 1, nil},
		{"v7:" + raw, 7, nil},
		{"v0:" + raw, 0, ErrBadVersion},
		{"vx:" + raw, 0, ErrBadVersion},
		{"v2:short", 0, ErrWeakKey},
		{"v" + raw, 1, nil}, // no colon: the whole string is the key
	} {
		got, err := ParseVersionedKey(tc.in)
		if !errors.Is(err, tc.err) {
			t.Fatalf("%q: err = %v, want %v", tc.in, err, tc.err)
		}
		if err == nil && got.Version != tc.version {
			t.Fatalf("%q: version = %d, want %d", tc.in, got.Version, tc.version)
		}
	}
	// Two configured versions build a keyring that seals under the newer one
	// and still opens values sealed under the older.
	old, _ := ParseVersionedKey(raw)
	next, _ := ParseVersionedKey("v2:" + strings.Repeat("z", 32))
	v1, _ := NewKeyring(old)
	sealed, err := v1.Seal("p", "r", []byte("secret"))
	if err != nil {
		t.Fatal(err)
	}
	both, err := NewKeyring(old, next)
	if err != nil {
		t.Fatal(err)
	}
	if both.Active() != 2 {
		t.Fatalf("active = %d", both.Active())
	}
	if plain, err := both.Open("p", "r", sealed); err != nil || string(plain) != "secret" {
		t.Fatalf("open = %q, %v", plain, err)
	}
}
