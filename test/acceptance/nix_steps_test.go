package acceptance

import (
	"archive/tar"
	"bytes"
	"crypto/ed25519"
	"crypto/rand"
	"crypto/x509"
	"encoding/base64"
	"encoding/json"
	"encoding/pem"
	"fmt"
	"net/http"
	"net/url"
	"os"
	"path/filepath"
	"runtime"
	"strings"

	"github.com/cucumber/godog"
	"github.com/ulikunitz/xz"

	"github.com/weaveplatform/artifactweave/internal/artifacts/adapters/upstream/fakeupstream"
	nixfmt "github.com/weaveplatform/artifactweave/internal/artifacts/ecosystem/nix"
)

func (w *world) registerNix(sc *godog.ScenarioContext) {
	sc.Step(`^the Nix signing key is installed$`, w.nixSigningKey)
	sc.Step(`^I publish the controlled Nix path to "([^"]*)"$`, w.nixPublish)
	sc.Step(
		`^I substitute the controlled Nix path from "([^"]*)" with the "(trusted|wrong)" key$`,
		w.nixSubstitute,
	)
	sc.Step(`^the controlled signed Nix upstream is available$`, w.nixUpstream)
	sc.Step(
		`^I publish two Nix channel snapshots to "([^"]*)" and follow the channel with nix-channel$`,
		w.nixChannel,
	)
}

func (w *world) nixSigningKey() error {
	public, private, err := ed25519.GenerateKey(rand.Reader)
	if err != nil {
		return err
	}
	privateDER, err := x509.MarshalPKCS8PrivateKey(private)
	if err != nil {
		return err
	}
	publicDER, err := x509.MarshalPKIXPublicKey(public)
	if err != nil {
		return err
	}
	if w.vars == nil {
		w.vars = map[string]string{}
	}
	w.vars["nix-public"] = "artifactweave-nix-1:" + base64.StdEncoding.EncodeToString(public)
	w.vars["nix-private"] = base64.StdEncoding.EncodeToString(private)
	body, err := json.Marshal(
		map[string]string{
			"pairName": "nix-signing",
			"pairType": "Ed25519",
			"alias":    "artifactweave-nix-1",
			"privateKey": string(
				pem.EncodeToMemory(&pem.Block{Type: "PRIVATE KEY", Bytes: privateDER}),
			),
			"publicKey": string(
				pem.EncodeToMemory(&pem.Block{Type: "PUBLIC KEY", Bytes: publicDER}),
			),
		},
	)
	if err != nil {
		return err
	}
	if err = w.send(
		http.MethodPost,
		"/artifactory/api/security/keypair",
		string(body),
		func(r *http.Request) { r.SetBasicAuth("admin", testAdminPassword) },
	); err != nil {
		return err
	}
	return w.statusIs(201)
}

func (w *world) nixURL(key string) string {
	base := w.url
	if runtime.GOOS == "darwin" && os.Getenv("ARTIFACTWEAVE_NIX_DESKTOP") == "1" {
		base = strings.Replace(base, "127.0.0.1", "host.docker.internal", 1)
	}
	return strings.Replace(
		base,
		"://",
		"://admin:"+testAdminPassword+"@",
		1,
	) + "/artifactory/api/nix/" + key
}

func (w *world) nixPublish(key string) error {
	script := `set -eu
nix --version
mkdir /tmp/weave-fixture
printf 'native nix fixture\n' > /tmp/weave-fixture/message
store_path=$(nix-store --add /tmp/weave-fixture)
printf '%s\n' "$store_path" > /work/nix-store-path
nix --extra-experimental-features nix-command copy --to "$1?compression=xz" "$store_path"
`
	return w.runContainer(
		w.nixImage,
		[]string{"--user", "0:0", "-e", "HOME=/tmp/nix-publisher"},
		[]string{"sh", "-c", script, "nix-publish", w.nixURL(key)},
	)
}

func (w *world) nixSubstitute(key, trust string) error {
	dir, err := w.filesDir()
	if err != nil {
		return err
	}
	data, err := os.ReadFile(filepath.Join(dir, "nix-store-path"))
	if err != nil {
		return err
	}
	public := w.vars["nix-public"]
	if trust == "wrong" {
		public = "artifactweave-nix-1:" + base64.StdEncoding.EncodeToString(
			make([]byte, ed25519.PublicKeySize),
		)
	}
	script := `set -eu
nix --extra-experimental-features nix-command --option require-sigs true --option trusted-public-keys "$2" copy --from "$1" --to 'local?root=/tmp/nix-consumer' "$3"
cat "/tmp/nix-consumer$3/message"
`
	return w.runContainer(
		w.nixImage,
		[]string{"--user", "0:0", "-e", "HOME=/tmp/nix-consumer-home"},
		[]string{
			"sh",
			"-c",
			script,
			"nix-substitute",
			w.nixURL(key),
			public,
			strings.TrimSpace(string(data)),
		},
	)
}

func (w *world) nixUpstream() error {
	const path = "fyli4q2yj6vh01bs0jxqbvj5hir5zvqc.narinfo"
	root := filepath.Join(repoRoot, "internal/artifacts/ecosystem/nix/testdata/cache")
	data, err := os.ReadFile(filepath.Join(root, path))
	if err != nil {
		return err
	}
	info, err := nixfmt.Parse(data, path)
	if err != nil {
		return err
	}
	private, err := base64.StdEncoding.DecodeString(w.vars["nix-private"])
	if err != nil {
		return err
	}
	if err = info.Sign("artifactweave-nix-1", ed25519.PrivateKey(private)); err != nil {
		return err
	}
	archive, err := os.ReadFile(filepath.Join(root, info.URL))
	if err != nil {
		return err
	}
	if err = w.remoteUpstream("/"+path, string(info.Encode(info.URL))); err != nil {
		return err
	}
	w.remote.Set(
		"/"+path,
		fakeupstream.Resource{
			Header: http.Header{"Content-Type": {"text/x-nix-narinfo"}},
			Body:   info.Encode(info.URL),
		},
	)
	w.remote.Set(
		"/"+info.URL,
		fakeupstream.Resource{
			Header: http.Header{"Content-Type": {"application/x-nix-nar"}},
			Body:   archive,
		},
	)
	dir, err := w.filesDir()
	if err != nil {
		return err
	}
	if err = os.WriteFile(
		filepath.Join(dir, "nix-store-path"),
		[]byte(info.StorePath+"\n"),
		0o600,
	); err != nil {
		return fmt.Errorf("Nix fixture store path: %w", err)
	}
	return nil
}

// nixChannelTarball writes /work/<name>.tar.xz: a nixexprs.tar.xz whose one
// top-level folder, as nix-channel's unpack-channel requires, holds a
// default.nix and a marker file naming the snapshot.
func (w *world) nixChannelTarball(name string) error {
	var archive bytes.Buffer
	tw := tar.NewWriter(&archive)
	if err := tw.WriteHeader(
		&tar.Header{Name: "nixexprs/", Typeflag: tar.TypeDir, Mode: 0o755},
	); err != nil {
		return err
	}
	for file, body := range map[string]string{
		"default.nix": "{ ... }: { }\n",
		"marker":      "channel snapshot " + name + "\n",
	} {
		if err := tw.WriteHeader(&tar.Header{
			Name: "nixexprs/" + file, Typeflag: tar.TypeReg, Mode: 0o644, Size: int64(len(body)),
		}); err != nil {
			return err
		}
		if _, err := tw.Write([]byte(body)); err != nil {
			return err
		}
	}
	if err := tw.Close(); err != nil {
		return err
	}
	var out bytes.Buffer
	xw, err := xz.NewWriter(&out)
	if err != nil {
		return err
	}
	if _, err = xw.Write(archive.Bytes()); err != nil {
		return err
	}
	if err = xw.Close(); err != nil {
		return err
	}
	dir, err := w.filesDir()
	if err != nil {
		return err
	}
	return os.WriteFile(filepath.Join(dir, name+".tar.xz"), out.Bytes(), 0o644)
}

// nixChannel uploads a snapshot with curl as the JFrog guide does, follows
// the channel with nix-channel --add and --update, then uploads a newer
// snapshot and updates again, checking the unpacked channel each time.
// Credentials come from a netrc file: Nix 2.35 drops a URL's user and
// password from the effective URL it appends nixexprs.tar.xz to, so a
// channel URL's own credentials reach only the first request. Nix keeps a
// downloaded channel for tarball-ttl seconds (an hour by default), so the
// second update sets it to 0 to ask the registry again.
func (w *world) nixChannel(key string) error {
	for _, name := range []string{"first", "second"} {
		if err := w.nixChannelTarball(name); err != nil {
			return err
		}
	}
	u, err := url.Parse(w.nixURL(key))
	if err != nil {
		return err
	}
	u.User = nil
	dir, err := w.filesDir()
	if err != nil {
		return err
	}
	netrc := fmt.Sprintf("machine %s login admin password %s\n", u.Hostname(), testAdminPassword)
	if err = os.WriteFile(filepath.Join(dir, "nix-netrc"), []byte(netrc), 0o600); err != nil {
		return err
	}
	script := `set -eu
mkdir -p "$HOME"
channel="$1/channels/nixpkgs-25.11"
curl -fsS -o /dev/null --netrc-file /work/nix-netrc -X PUT -T /work/first.tar.xz "$channel/nixpkgs/nixpkgs-25.11pre1"
nix-channel --add "$channel" nixpkgs
nix-channel --update nixpkgs
marker="$HOME/.nix-defexpr/channels/nixpkgs/marker"
test "$(cat "$marker")" = 'channel snapshot first'
curl -fsS -o /dev/null --netrc-file /work/nix-netrc -X PUT -T /work/second.tar.xz "$channel/nixpkgs/nixpkgs-25.11pre2"
NIX_CONFIG="$NIX_CONFIG
tarball-ttl = 0" nix-channel --update nixpkgs
printf 'followed %s\n' "$(cat "$marker")"
`
	return w.runContainer(
		w.nixImage,
		[]string{
			"--user", "0:0", "-e", "HOME=/tmp/nix-channel-home",
			"-e", "NIX_CONFIG=netrc-file = /work/nix-netrc",
		},
		[]string{"sh", "-c", script, "nix-channel", u.String()},
	)
}
