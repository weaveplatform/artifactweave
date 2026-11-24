package api_test

import (
	"bytes"
	"context"
	"errors"
	"testing"

	"github.com/ProtonMail/go-crypto/openpgp"
	"github.com/ProtonMail/go-crypto/openpgp/armor"

	"github.com/weaveplatform/artifactweave/internal/access/api"
	"github.com/weaveplatform/artifactweave/internal/access/api/accessapitest"
	"github.com/weaveplatform/artifactweave/internal/kernel"
)

// gpgKey is a generated OpenPGP key's armored halves, the private one
// encrypted with passphrase when it is set.
func gpgKey(t *testing.T, passphrase string) (private, public string) {
	t.Helper()
	e, err := openpgp.NewEntity("artifactweave", "test", "test@example.com", nil)
	if err != nil {
		t.Fatal(err)
	}
	arm := func(typ string, write func(*bytes.Buffer) error) string {
		var raw, out bytes.Buffer
		if err := write(&raw); err != nil {
			t.Fatal(err)
		}
		w, err := armor.Encode(&out, typ, nil)
		if err != nil {
			t.Fatal(err)
		}
		if _, err := w.Write(raw.Bytes()); err != nil {
			t.Fatal(err)
		}
		if err := w.Close(); err != nil {
			t.Fatal(err)
		}
		return out.String()
	}
	public = arm(openpgp.PublicKeyType, func(w *bytes.Buffer) error { return e.Serialize(w) })
	if passphrase == "" {
		return arm(openpgp.PrivateKeyType, func(w *bytes.Buffer) error { return e.SerializePrivate(w, nil) }), public
	}
	if err := e.EncryptPrivateKeys([]byte(passphrase), nil); err != nil {
		t.Fatal(err)
	}
	return arm(openpgp.PrivateKeyType, func(w *bytes.Buffer) error {
		return e.SerializePrivateWithoutSigning(w, nil)
	}), public
}

// TestUnitMetadataSigningPair: a repository's own pair signs whatever the
// instance pair is, with its read's errors; with none named, the instance
// pair signs only once it is a complete, unlockable GPG pair whose halves
// belong together, and an incomplete one signs nothing rather than failing.
func TestUnitMetadataSigningPair(t *testing.T) {
	ctx := context.Background()
	priv, pub := gpgKey(t, "")
	encPriv, encPub := gpgKey(t, "s3cret")
	_, otherPub := gpgKey(t, "")
	own := api.Keypair{Name: "own", Type: api.KeypairRSA, PrivateKey: "rsa", PublicKey: "rsa"}
	instance := func(k api.Keypair) *accessapitest.Keys {
		k.Name = api.DefaultGPGKey
		return &accessapitest.Keys{Keypairs: map[string]api.Keypair{"own": own, api.DefaultGPGKey: k}}
	}
	complete := api.Keypair{Type: api.KeypairGPG, PrivateKey: priv, PublicKey: pub}
	for name, c := range map[string]struct {
		keys  api.Keys
		ref   string
		signs bool
		name  string
	}{
		"own pair first":        {instance(complete), "own", true, "own"},
		"instance pair":         {instance(complete), "", true, api.DefaultGPGKey},
		"encrypted instance":    {instance(api.Keypair{Type: api.KeypairGPG, PrivateKey: encPriv, PublicKey: encPub, Passphrase: "s3cret"}), "", true, api.DefaultGPGKey},
		"no instance pair":      {&accessapitest.Keys{}, "", false, ""},
		"no key store":          {nil, "", false, ""},
		"private half only":     {instance(api.Keypair{Type: api.KeypairGPG, PrivateKey: priv}), "", false, ""},
		"public half only":      {instance(api.Keypair{Type: api.KeypairGPG, PublicKey: pub}), "", false, ""},
		"passphrase missing":    {instance(api.Keypair{Type: api.KeypairGPG, PrivateKey: encPriv, PublicKey: encPub}), "", false, ""},
		"halves differ":         {instance(api.Keypair{Type: api.KeypairGPG, PrivateKey: priv, PublicKey: otherPub}), "", false, ""},
		"not a GPG pair":        {instance(api.Keypair{Type: api.KeypairRSA, PrivateKey: priv, PublicKey: pub}), "", false, ""},
		"private not a key":     {instance(api.Keypair{Type: api.KeypairGPG, PrivateKey: "x", PublicKey: pub}), "", false, ""},
		"public not a key":      {instance(api.Keypair{Type: api.KeypairGPG, PrivateKey: priv, PublicKey: "x"}), "", false, ""},
		"private is the public": {instance(api.Keypair{Type: api.KeypairGPG, PrivateKey: pub, PublicKey: pub}), "", false, ""},
	} {
		k, signs, err := api.MetadataSigningPair(ctx, c.keys, c.ref)
		if err != nil || signs != c.signs || k.Name != c.name {
			t.Errorf("%s: got %q %v %v, want %q %v", name, k.Name, signs, err, c.name, c.signs)
		}
	}
	// A named pair that does not exist, or a store that cannot be read,
	// fails as it did before the instance key.
	if _, _, err := api.MetadataSigningPair(ctx, &accessapitest.Keys{}, "absent"); !errors.Is(err, kernel.ErrNotFound) {
		t.Errorf("a missing named pair: %v", err)
	}
	if _, _, err := api.MetadataSigningPair(ctx, nil, "own"); err == nil {
		t.Error("a named pair without a key store signed")
	}
	boom := errors.New("boom")
	for _, ref := range []string{"", "own"} {
		if _, _, err := api.MetadataSigningPair(ctx, &accessapitest.Keys{Err: boom}, ref); !errors.Is(err, boom) {
			t.Errorf("an unreadable store with ref %q: %v", ref, err)
		}
	}
}
