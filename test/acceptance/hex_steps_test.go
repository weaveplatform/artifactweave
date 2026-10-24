package acceptance

import (
	"bytes"
	"crypto/rand"
	"crypto/rsa"
	"fmt"
	"os"
	"path/filepath"
	"strconv"

	"github.com/cucumber/godog"

	"github.com/weaveplatform/artifactweave/internal/fakeupstream"
	hexfmt "github.com/weaveplatform/artifactweave/internal/format/hex"
)

func (w *world) registerHex(sc *godog.ScenarioContext) {
	sc.Step(`^a Hex project for repository "([^"]*)"$`, w.hexProject)
	sc.Step(`^a signed Hex upstream serves decimal 2.0.0$`, w.hexUpstream)
	sc.Step(`^the Hex client cache is cleared$`, w.clearHexClient)
}

func (w *world) hexProject(repoKey string) error {
	dir, err := w.filesDir()
	if err != nil {
		return err
	}
	fixture, err := os.ReadFile("../../internal/format/hex/testdata/decimal-2.0.0.tar")
	if err != nil {
		return err
	}
	if err := os.WriteFile(filepath.Join(dir, "decimal-2.0.0.tar"), fixture, 0o600); err != nil {
		return err
	}
	if err := w.getAsAdmin("/artifactory/api/hex/" + repoKey + "/public_key"); err != nil {
		return err
	}
	if w.status != 200 {
		return fmt.Errorf("Hex public key returned %d: %s", w.status, w.body)
	}
	if err := os.WriteFile(filepath.Join(dir, repoKey+".pem"), w.body, 0o600); err != nil {
		return err
	}
	mix := fmt.Sprintf(`defmodule HexAcceptance.MixProject do
  use Mix.Project
  def project do
    [app: :hex_acceptance, version: "0.1.0", elixir: "~> 1.16",
     deps: [{:decimal, "2.0.0", repo: %q}]]
  end
end
`, repoKey)
	return os.WriteFile(filepath.Join(dir, "mix.exs"), []byte(mix), 0o600)
}

func (w *world) runMix(args string) error {
	return w.runContainer(w.hexImage, []string{
		"--entrypoint", "mix", "-e", "HEX_HOME=/work/.hex",
	}, w.clientArgs(args))
}

func (w *world) hexUpstream() error {
	fixture, err := os.ReadFile("../../internal/format/hex/testdata/decimal-2.0.0.tar")
	if err != nil {
		return err
	}
	pkg, err := hexfmt.InspectTarball(bytes.NewReader(fixture))
	if err != nil {
		return err
	}
	key, err := rsa.GenerateKey(rand.Reader, 2048)
	if err != nil {
		return err
	}
	reg := hexfmt.RegistryPackage{
		Name:       "decimal",
		Repository: "hexpm",
		Releases:   []hexfmt.Package{pkg},
	}
	pack, err := hexfmt.EncodePackage(reg, key)
	if err != nil {
		return err
	}
	versions, err := hexfmt.EncodeVersions("hexpm", []hexfmt.RegistryPackage{reg}, key)
	if err != nil {
		return err
	}
	if err := w.remoteUpstream("/hex/ready", "ready"); err != nil {
		return err
	}
	w.remote.Set("/hex/packages/decimal", fakeupstream.Resource{Body: pack})
	w.remote.Set("/hex/versions", fakeupstream.Resource{Body: versions})
	w.remote.Set(
		"/hex/tarballs/decimal-2.0.0.tar",
		fakeupstream.Resource{Redirect: "/hex/redirect/decimal-2.0.0.tar"},
	)
	w.remote.Set("/hex/redirect/decimal-2.0.0.tar", fakeupstream.Resource{Body: fixture})
	w.vars["hexUpstreamKeyJSON"] = strconv.Quote(hexfmt.PublicKeyPEM(key))
	return nil
}

func (w *world) clearHexClient() error {
	dir, err := w.filesDir()
	if err != nil {
		return err
	}
	for _, name := range []string{".hex", "deps", "_build", "mix.lock"} {
		if err := os.RemoveAll(filepath.Join(dir, name)); err != nil {
			return err
		}
	}
	return nil
}
