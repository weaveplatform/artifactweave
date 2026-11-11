package crypt

import (
	"encoding/hex"
	"errors"
	"testing"
)

// The golden values below are persisted state, not fixtures to regenerate.
// Every sealed column and sealed Terraform state already stored was written in
// this format, and Derive must keep giving the keys it gave before. A changed
// magic, layout, salt or HKDF label still compiles, and the round-trip tests
// still pass because they seal and open with the same build, but every stored
// value would stop opening and every derived key would change. A failure here
// means the change breaks existing databases, not that a golden value is out
// of date.
const (
	katPurpose       = "keypairs.private_key"
	katRowID         = "release-signing"
	katPlain         = "known answer"
	katNonce         = "000102030405060708090a0b"
	katDerivePurpose = "known-answer"

	// katSealedV1 is katPlain sealed under version 1, katMasterV1.
	katSealedV1 = "00617773" + // magic
		"00000001" + // version
		"afde17f3" + // key id
		"000102030405060708090a0b" + // nonce
		"108fbc98719a58598a40c13a" + // ciphertext
		"ae0392e6a0268dabe8475ce983855327" // tag

	// katSealedV2 is katPlain sealed under version 2, katMasterV2, by a
	// keyring rotated from version 1.
	katSealedV2 = "00617773" + // magic
		"00000002" + // version
		"158b210f" + // key id
		"000102030405060708090a0b" + // nonce
		"ea5a9ea667cd139811c5cf51" + // ciphertext
		"b59cb133eb7c74dcaa893cc328ff18d8" // tag

	// katDerived is Derive(katDerivePurpose) under version 1, katMasterV1.
	katDerived = "5f0e6dc57f46721203aebe1ec5a442921b4a63043bc5cef1b7b6e54eac33d756"
)

var (
	katMasterV1 = []byte("artifactweave known answer key 1")
	katMasterV2 = []byte("artifactweave known answer key 2")
)

// fixedNonce gives every seal the same nonce, so a sealed value is
// deterministic.
type fixedNonce []byte

func (n fixedNonce) Read(p []byte) (int, error) { return copy(p, n), nil }

func TestUnitSealedFormatKnownAnswer(t *testing.T) {
	withRand(t, fixedNonce(unhex(t, katNonce)))
	v1, err := New(katMasterV1)
	if err != nil {
		t.Fatal(err)
	}
	v2, err := v1.Rotate(katMasterV2)
	if err != nil {
		t.Fatal(err)
	}
	// A server restarted with only the new key builds its keyring from the
	// configuration rather than by rotating, and must open what v2 sealed.
	only2, err := NewKeyring(MasterKey{Version: 2, Material: katMasterV2})
	if err != nil {
		t.Fatal(err)
	}
	cases := []struct {
		name   string
		seal   *Keyring
		opens  []*Keyring
		golden string
	}{
		{"version 1", v1, []*Keyring{v1, v2}, katSealedV1},
		{"version 2 after a rotation", v2, []*Keyring{v2, only2}, katSealedV2},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			sealed, err := tc.seal.Seal(katPurpose, katRowID, []byte(katPlain))
			if err != nil {
				t.Fatal(err)
			}
			if got := hex.EncodeToString(sealed); got != tc.golden {
				t.Fatalf("sealed value\n got %s\nwant %s", got, tc.golden)
			}
			golden := unhex(t, tc.golden)
			for _, k := range tc.opens {
				plain, err := k.Open(katPurpose, katRowID, golden)
				if err != nil || string(plain) != katPlain {
					t.Fatalf("open: %q, %v", plain, err)
				}
			}
			_, err = tc.seal.Open("keypairs.passphrase", katRowID, golden)
			if !errors.Is(err, ErrTampered) {
				t.Fatalf("open under another purpose: %v", err)
			}
			_, err = tc.seal.Open(katPurpose, "other-row", golden)
			if !errors.Is(err, ErrTampered) {
				t.Fatalf("open for another row: %v", err)
			}
		})
	}
}

func TestUnitDeriveKnownAnswer(t *testing.T) {
	k, err := New(katMasterV1)
	if err != nil {
		t.Fatal(err)
	}
	if got := hex.EncodeToString(k.Derive(katDerivePurpose)); got != katDerived {
		t.Fatalf("derived key\n got %s\nwant %s", got, katDerived)
	}
}

func unhex(t *testing.T, s string) []byte {
	t.Helper()
	b, err := hex.DecodeString(s)
	if err != nil {
		t.Fatal(err)
	}
	return b
}
