package acceptance

import (
	"bytes"
	"crypto/sha256"
	"fmt"
	"os"
	"path/filepath"

	"github.com/ProtonMail/go-crypto/openpgp"
	"github.com/ProtonMail/go-crypto/openpgp/packet"
	"github.com/cucumber/godog"
	"go.yaml.in/yaml/v3"
)

func (w *world) registerHelm(sc *godog.ScenarioContext) {
	sc.Step(`^a Helm signing keyring in "([^"]*)"$`, w.helmKeyring)
	sc.Step(
		`^the Helm index has one "([^"]*)" version "([^"]*)" matching "([^"]*)"$`,
		w.helmIndexDigest,
	)
}

// Generate an unencrypted throwaway key for each scenario; Helm itself creates
// the provenance record with package --sign. Binary keyrings work with both the
// older client and Helm 4, without requiring a GnuPG installation.
func (w *world) helmKeyring(name string) error {
	dir, err := w.filesDir()
	if err != nil {
		return err
	}
	key, err := openpgp.NewEntity("artifactweave", "acceptance only", "helm@example.invalid",
		&packet.Config{Algorithm: packet.PubKeyAlgoRSA, RSABits: 2048})
	if err != nil {
		return err
	}
	var public, private bytes.Buffer
	if err := key.Serialize(&public); err != nil {
		return err
	}
	if err := key.SerializePrivate(&private, nil); err != nil {
		return err
	}
	dir = filepath.Join(dir, name)
	if err := os.MkdirAll(dir, 0o700); err != nil {
		return err
	}
	if err := os.WriteFile(filepath.Join(dir, "public.gpg"), public.Bytes(), 0o600); err != nil {
		return err
	}
	return os.WriteFile(filepath.Join(dir, "private.gpg"), private.Bytes(), 0o600)
}

// Parse the served index independently from the server's Helm model, and check
// the exact version count as well as the digest of the archive Helm downloaded.
func (w *world) helmIndexDigest(chart, version, name string) error {
	var index struct {
		Entries map[string][]struct {
			Version string `yaml:"version"`
			Digest  string `yaml:"digest"`
		} `yaml:"entries"`
	}
	if err := yaml.Unmarshal(w.body, &index); err != nil {
		return err
	}
	dir, err := w.filesDir()
	if err != nil {
		return err
	}
	body, err := os.ReadFile(filepath.Join(dir, name))
	if err != nil {
		return err
	}
	want := fmt.Sprintf("%x", sha256.Sum256(body))
	var count int
	for _, entry := range index.Entries[chart] {
		if entry.Version != version {
			continue
		}
		count++
		if entry.Digest != want {
			return fmt.Errorf("index digest %s, want %s for %s", entry.Digest, want, name)
		}
	}
	if count != 1 {
		return fmt.Errorf("index has %d entries for %s %s, want one", count, chart, version)
	}
	return nil
}
