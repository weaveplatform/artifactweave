package acceptance

import (
	"archive/tar"
	"bytes"
	"compress/gzip"
	"context"
	"crypto"
	"crypto/rand"
	"crypto/rsa"
	"crypto/sha1" //nolint:gosec // apk v2 signatures use SHA-1 on compressed control bytes.
	"crypto/sha256"
	"crypto/x509"
	"encoding/hex"
	"encoding/json"
	"encoding/pem"
	"fmt"
	"io"
	"net/http"
	"net/url"
	"os"
	"path/filepath"
	"runtime"
	"strings"

	"github.com/cucumber/godog"

	"github.com/weaveplatform/artifactweave/internal/fakeupstream"
	"github.com/weaveplatform/artifactweave/internal/format/alpine"
)

func (w *world) registerAlpine(sc *godog.ScenarioContext) {
	sc.Step(`^I create an Alpine signing pair "([^"]*)" with alias "([^"]*)"$`,
		w.createAlpineSigningPair)
	sc.Step(`^a signed Alpine package "([^"]*)" at "([^"]*)" under "([^"]*)"$`,
		w.createAlpinePackage)
	sc.Step(
		`^a signed Alpine package "([^"]*)" at "([^"]*)" with message "([^"]*)" under "([^"]*)"$`,
		w.createAlpinePackageMessage,
	)
	sc.Step(`^the apk repository is "([^"]*)"$`, w.setAlpineRepository)
	sc.Step(`^I run apk "([^"]*)"$`, w.runAlpine)
	sc.Step(`^I clear the apk installation root$`, w.clearAlpineRoot)
	sc.Step(`^an Alpine upstream serves "([^"]*)" at "([^"]*)"$`, w.alpineUpstream)
	sc.Step(`^the Alpine upstream redirects "([^"]*)" at "([^"]*)"$`, w.alpineRedirect)
	sc.Step(`^the Alpine upstream corrupts "([^"]*)" at "([^"]*)"$`, w.alpineCorruptUpstreamPackage)
	sc.Step(`^the Alpine upstream publishes an invalid index$`, w.alpineInvalidUpstreamIndex)
	sc.Step(`^I corrupt the Alpine package "([^"]*)" at "([^"]*)"$`, w.corruptAlpinePackage)
	sc.Step(
		`^I deploy the Alpine package "([^"]*)" at "([^"]*)" to "([^"]*)" as "([^"]*)" with password "([^"]*)"$`,
		w.deployAlpineAs,
	)
}

func (w *world) createAlpineSigningPair(name, alias string) error {
	key, err := rsa.GenerateKey(rand.Reader, 2048)
	if err != nil {
		return err
	}
	publicDER, err := x509.MarshalPKIXPublicKey(&key.PublicKey)
	if err != nil {
		return err
	}
	public := pem.EncodeToMemory(&pem.Block{Type: "PUBLIC KEY", Bytes: publicDER})
	body, err := json.Marshal(map[string]string{
		"pairName": name, "pairType": "RSA", "alias": alias,
		"privateKey": string(pem.EncodeToMemory(&pem.Block{
			Type: "RSA PRIVATE KEY", Bytes: x509.MarshalPKCS1PrivateKey(key),
		})),
		"publicKey": string(public),
	})
	if err != nil {
		return err
	}
	w.alpineKey = key
	dir, err := w.filesDir()
	if err != nil {
		return err
	}
	keys := filepath.Join(dir, "keys")
	if err := os.MkdirAll(keys, 0o750); err != nil {
		return err
	}
	if err := os.WriteFile(filepath.Join(keys, alias+".rsa.pub"), public, 0o600); err != nil {
		return err
	}
	return w.send(http.MethodPost, "/artifactory/api/security/keypair", string(body),
		func(r *http.Request) { r.SetBasicAuth("admin", testAdminPassword) })
}

func (w *world) createAlpinePackage(name, version, folder string) error {
	return w.createAlpinePackageMessage(name, version, "installed "+name, folder)
}

func (w *world) createAlpinePackageMessage(name, version, message, folder string) error {
	if w.alpineKey == nil {
		return fmt.Errorf("Alpine signing pair is missing")
	}
	content := []byte(message + "\n")
	data, err := alpineTestTar(
		"usr/share/"+name+"/message.txt",
		content,
		false,
	)
	if err != nil {
		return err
	}
	dataHash := sha256.Sum256(data)
	info := []byte(fmt.Sprintf("pkgname = %s\npkgver = %s\narch = x86_64\nsize = %d\n"+
		"pkgdesc = controlled Alpine package\nurl = https://example.invalid/alpine\n"+
		"license = MIT\ndatahash = %s\n",
		name, version, len(content), hex.EncodeToString(dataHash[:]),
	))
	control, err := alpineTestTar(".PKGINFO", info, true)
	if err != nil {
		return err
	}
	h := sha1.Sum(control) //nolint:gosec // apk v2 wire format.
	signature, err := rsa.SignPKCS1v15(rand.Reader, w.alpineKey, crypto.SHA1, h[:])
	if err != nil {
		return err
	}
	sig, err := alpineTestTar(".SIGN.RSA.test.rsa.pub", signature, true)
	if err != nil {
		return err
	}
	dir, err := w.filesDir()
	if err != nil {
		return err
	}
	file := filepath.Join(dir, folder, name+"-"+version+".apk")
	if err := os.MkdirAll(filepath.Dir(file), 0o750); err != nil {
		return err
	}
	return os.WriteFile(file, bytes.Join([][]byte{sig, control, data}, nil), 0o600)
}

func alpineTestTar(name string, content []byte, cut bool) ([]byte, error) {
	var out bytes.Buffer
	z := gzip.NewWriter(&out)
	tw := tar.NewWriter(z)
	header := &tar.Header{Name: name, Mode: 0o644, Size: int64(len(content))}
	if !strings.HasPrefix(name, ".") {
		parts := strings.Split(name, "/")
		for i := 1; i < len(parts); i++ {
			if err := tw.WriteHeader(&tar.Header{
				Name: strings.Join(parts[:i], "/") + "/", Typeflag: tar.TypeDir,
				Mode: 0o755,
			}); err != nil {
				return nil, err
			}
		}
		hash := sha1.Sum(content) //nolint:gosec // apk v2 data tar checksum field.
		header.PAXRecords = map[string]string{
			"APK-TOOLS.checksum.SHA1": hex.EncodeToString(hash[:]),
		}
	}
	if err := tw.WriteHeader(header); err != nil {
		return nil, err
	}
	if _, err := tw.Write(content); err != nil {
		return nil, err
	}
	if cut {
		if err := tw.Flush(); err != nil {
			return nil, err
		}
	} else if err := tw.Close(); err != nil {
		return nil, err
	}
	if err := z.Close(); err != nil {
		return nil, err
	}
	return out.Bytes(), nil
}

func (w *world) setAlpineRepository(key string) error {
	dir, err := w.filesDir()
	if err != nil {
		return err
	}
	base := w.url
	if runtime.GOOS == "darwin" && os.Getenv("ARTIFACTWEAVE_ALPINE_DESKTOP") == "1" {
		base = strings.Replace(base, "127.0.0.1", "host.docker.internal", 1)
	}
	u, err := url.Parse(base)
	if err != nil {
		return err
	}
	u.User = url.UserPassword("admin", testAdminPassword)
	return os.WriteFile(filepath.Join(dir, "repositories"),
		[]byte(u.String()+"/artifactory/"+key+"/v3.20/main\n"), 0o600)
}

func (w *world) runAlpine(args string) error {
	// apk-tools 3 needs usermode to initialize a writable root as the host UID.
	// It still verifies the repository index and package signatures.
	command := []string{
		"apk", "--root", "/work/alpine-root", "--initdb", "--keys-dir", "/work/keys",
		"--repositories-file", "/work/repositories", "--no-cache", "--usermode",
	}
	if runtime.GOOS == "darwin" && os.Getenv("ARTIFACTWEAVE_ALPINE_DESKTOP") == "1" {
		command = append(command, "--arch", "x86_64")
	}
	command = append(command, strings.Fields(w.expand(args))...)
	return w.runContainer(w.alpineImage, nil, command)
}

func (w *world) clearAlpineRoot() error {
	dir, err := w.filesDir()
	if err != nil {
		return err
	}
	return os.RemoveAll(filepath.Join(dir, "alpine-root"))
}

func (w *world) alpineUpstream(name, version string) error {
	dir, err := w.filesDir()
	if err != nil {
		return err
	}
	archive, err := os.ReadFile(filepath.Join(dir, "apk", name+"-"+version+".apk"))
	if err != nil {
		return err
	}
	path := "v3.20/main/x86_64/" + name + "-" + version + ".apk"
	pkg, err := alpine.ReadPackage(
		w.t.Context(),
		bytes.NewReader(archive),
		int64(len(archive)),
		path,
	)
	if err != nil {
		return err
	}
	index, err := alpine.EncodeIndex(
		[]alpine.Package{pkg},
		"controlled upstream",
		"test.rsa.pub",
		w.alpineKey,
	)
	if err != nil {
		return err
	}
	w.remote = fakeupstream.New(w.t, fakeupstream.Options{})
	w.closers = append(w.closers, w.remote.Close)
	w.remote.Set("/v3.20/main/x86_64/APKINDEX.tar.gz", fakeupstream.Resource{Body: index})
	w.remote.Set("/"+path, fakeupstream.Resource{Body: archive})
	if w.vars == nil {
		w.vars = map[string]string{}
	}
	w.vars["upstream"] = w.remote.URL
	return nil
}

func (w *world) alpineRedirect(name, version string) error {
	dir, err := w.filesDir()
	if err != nil {
		return err
	}
	archive, err := os.ReadFile(filepath.Join(dir, "apk", name+"-"+version+".apk"))
	if err != nil {
		return err
	}
	original := "/v3.20/main/x86_64/" + name + "-" + version + ".apk"
	destination := "/mirrors/" + name + "-" + version + ".apk"
	w.remote.Set(original, fakeupstream.Resource{Redirect: w.remote.URL + destination})
	w.remote.Set(destination, fakeupstream.Resource{Body: archive})
	return nil
}

func (w *world) alpineCorruptUpstreamPackage(name, version string) error {
	dir, err := w.filesDir()
	if err != nil {
		return err
	}
	archive, err := os.ReadFile(filepath.Join(dir, "apk", name+"-"+version+".apk"))
	if err != nil {
		return err
	}
	if len(archive) < 2 {
		return fmt.Errorf("Alpine package is too short to corrupt")
	}
	archive[len(archive)-2] ^= 0xff
	w.remote.Set("/v3.20/main/x86_64/"+name+"-"+version+".apk",
		fakeupstream.Resource{Body: archive})
	return nil
}

func (w *world) alpineInvalidUpstreamIndex() error {
	w.remote.Set("/v3.20/main/x86_64/APKINDEX.tar.gz",
		fakeupstream.Resource{Body: []byte("not an Alpine index")})
	return nil
}

func (w *world) corruptAlpinePackage(name, version string) error {
	dir, err := w.filesDir()
	if err != nil {
		return err
	}
	file := filepath.Join(dir, "apk", name+"-"+version+".apk")
	data, err := os.ReadFile(file)
	if err != nil {
		return err
	}
	if len(data) < 2 {
		return fmt.Errorf("Alpine package is too short to corrupt")
	}
	data[len(data)-2] ^= 0xff
	return os.WriteFile(file, data, 0o600)
}

func (w *world) deployAlpineAs(name, version, target, user, password string) error {
	dir, err := w.filesDir()
	if err != nil {
		return err
	}
	archive, err := os.ReadFile(filepath.Join(dir, "apk", name+"-"+version+".apk"))
	if err != nil {
		return err
	}
	req, err := http.NewRequestWithContext(context.Background(), http.MethodPut,
		w.url+w.expand(target), bytes.NewReader(archive))
	if err != nil {
		return err
	}
	req.SetBasicAuth(user, password)
	resp, err := http.DefaultClient.Do(req)
	if err != nil {
		return err
	}
	defer func() { _ = resp.Body.Close() }()
	w.status, w.header = resp.StatusCode, resp.Header
	w.body, err = io.ReadAll(resp.Body)
	return err
}
