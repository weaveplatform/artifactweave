// Command gpgkey writes a fresh GPG key pair without a passphrase, as
// ASCII-armored private.asc and public.asc in the directory it is given, for
// the Terraform scenarios' artifactory_keypair (test/terraform/run.sh). It
// replaces gpg there: gpg is not on every host, and its agent fails when the
// socket path under GNUPGHOME passes the platform's limit.
//
//	gpgkey <directory>
package main

import (
	"bytes"
	"fmt"
	"io"
	"os"
	"path/filepath"

	"github.com/ProtonMail/go-crypto/openpgp"
	"github.com/ProtonMail/go-crypto/openpgp/armor"
	"github.com/ProtonMail/go-crypto/openpgp/packet"
)

func main() {
	if len(os.Args) != 2 {
		fmt.Fprintln(os.Stderr, "usage: gpgkey <directory>")
		os.Exit(2)
	}
	if err := run(os.Args[1]); err != nil {
		fmt.Fprintln(os.Stderr, "gpgkey:", err)
		os.Exit(1)
	}
}

func run(dir string) error {
	// RSA named rather than left to the library's default, which a later
	// go-crypto may change: JFrog's gpg-signing page, which its Terraform page
	// links for the provider registry's key, generates an RSA key.
	cfg := &packet.Config{Algorithm: packet.PubKeyAlgoRSA, RSABits: 2048}
	e, err := openpgp.NewEntity("artifactweave Terraform fixture", "throwaway test key",
		"terraform-fixture@example.invalid", cfg)
	if err != nil {
		return fmt.Errorf("generate: %w", err)
	}
	if err := os.MkdirAll(dir, 0o700); err != nil {
		return fmt.Errorf("create %s: %w", dir, err)
	}
	if err := write(filepath.Join(dir, "private.asc"), openpgp.PrivateKeyType, 0o600,
		func(w io.Writer) error { return e.SerializePrivate(w, cfg) }); err != nil {
		return err
	}
	return write(filepath.Join(dir, "public.asc"), openpgp.PublicKeyType, 0o644, e.Serialize)
}

// write armors what serialize writes as one blockType block in path.
func write(path, blockType string, mode os.FileMode, serialize func(io.Writer) error) error {
	var out bytes.Buffer
	w, err := armor.Encode(&out, blockType, nil)
	if err != nil {
		return fmt.Errorf("armor %s: %w", path, err)
	}
	if err := serialize(w); err != nil {
		return fmt.Errorf("serialize %s: %w", path, err)
	}
	if err := w.Close(); err != nil {
		return fmt.Errorf("armor %s: %w", path, err)
	}
	out.WriteByte('\n')
	if err := os.WriteFile(path, out.Bytes(), mode); err != nil {
		return fmt.Errorf("write %s: %w", path, err)
	}
	return nil
}
