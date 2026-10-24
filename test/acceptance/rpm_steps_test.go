package acceptance

import (
	"bytes"
	"crypto/sha256"
	"encoding/json"
	"fmt"
	"net/http"
	"net/url"
	"os"
	"path/filepath"
	"runtime"
	"strings"
	"time"

	"github.com/cucumber/godog"

	"github.com/weaveplatform/artifactweave/internal/fakeupstream"
	"github.com/weaveplatform/artifactweave/internal/format/rpm"
	"github.com/weaveplatform/artifactweave/internal/format/terraform/terraformtest"
)

const rpmFixture = "artifactweave-alpha-1.0-1.fc42.noarch.rpm"

func (w *world) registerRPM(sc *godog.ScenarioContext) {
	sc.Step(`^a genuine RPM package is available$`, w.prepareRPMFixture)
	sc.Step(`^the dnf repository is "([^"]*)"$`, w.setRPMRepository)
	sc.Step(
		`^the dnf repository is "([^"]*)" as "([^"]*)" with password "([^"]*)"$`,
		w.setRPMRepositoryAs,
	)
	sc.Step(
		`^I deploy the file "([^"]*)" to "([^"]*)" as "([^"]*)" with password "([^"]*)"$`,
		w.deployFileAs,
	)
	sc.Step(`^I run dnf "([^"]*)"$`, w.runDNF)
	sc.Step(`^an RPM upstream serves the genuine package$`, w.rpmUpstream)
	sc.Step(`^I clear the RPM installation root$`, w.clearRPMRoot)
	sc.Step(`^I create a GPG RPM metadata signing pair$`, w.createRPMSigningPair)
	sc.Step(`^the RPM upstream publishes an invalid manifest$`, w.rpmInvalidManifest)
	sc.Step(`^the RPM upstream publishes an empty index$`, w.rpmEmptyIndex)
	sc.Step(`^the RPM upstream corrupts its package$`, w.rpmCorruptPackage)
	sc.Step(`^the RPM upstream redirects its package$`, w.rpmRedirectPackage)
}

func (w *world) rpmRedirectPackage() error {
	if w.remote == nil {
		return fmt.Errorf("RPM upstream is not running")
	}
	dir, err := w.filesDir()
	if err != nil {
		return err
	}
	body, err := os.ReadFile(filepath.Join(dir, rpmFixture))
	if err != nil {
		return err
	}
	destination := "/mirrors/" + rpmFixture
	w.remote.Set("/pool/"+rpmFixture, fakeupstream.Resource{Redirect: w.remote.URL + destination})
	w.remote.Set(destination, fakeupstream.Resource{Body: body})
	return nil
}

func (w *world) rpmInvalidManifest() error {
	if w.remote == nil {
		return fmt.Errorf("RPM upstream is not running")
	}
	w.remote.Set("/repodata/repomd.xml", fakeupstream.Resource{Body: []byte("invalid repomd")})
	return nil
}

func (w *world) rpmEmptyIndex() error {
	if w.remote == nil {
		return fmt.Errorf("RPM upstream is not running")
	}
	meta, err := rpm.EncodeMetadata(nil, "", time.Unix(1700000001, 0))
	if err != nil {
		return err
	}
	for p, data := range meta.Files {
		w.remote.Set("/"+p, fakeupstream.Resource{Body: data})
	}
	return nil
}

func (w *world) rpmCorruptPackage() error {
	if w.remote == nil {
		return fmt.Errorf("RPM upstream is not running")
	}
	dir, err := w.filesDir()
	if err != nil {
		return err
	}
	body, err := os.ReadFile(filepath.Join(dir, rpmFixture))
	if err != nil {
		return err
	}
	body[len(body)-1] ^= 0xff
	w.remote.Set("/pool/"+rpmFixture, fakeupstream.Resource{Body: body})
	return nil
}

func (w *world) createRPMSigningPair() error {
	key, err := terraformtest.NewKey()
	if err != nil {
		return err
	}
	input, err := json.Marshal(map[string]string{
		"pairName": "rpm-sign", "pairType": "GPG", "alias": "rpm-sign",
		"publicKey": key.Public, "privateKey": key.Private,
	})
	if err != nil {
		return err
	}
	dir, err := w.filesDir()
	if err != nil {
		return err
	}
	if err := os.WriteFile(
		filepath.Join(dir, "rpm-sign.pub"),
		[]byte(key.Public),
		0o600,
	); err != nil {
		return err
	}
	w.rpmSigned = true
	return w.send(http.MethodPost, "/artifactory/api/security/keypair", string(input),
		func(r *http.Request) { r.SetBasicAuth("admin", testAdminPassword) })
}

func (w *world) rpmUpstream() error {
	dir, err := w.filesDir()
	if err != nil {
		return err
	}
	body, err := os.ReadFile(filepath.Join(dir, rpmFixture))
	if err != nil {
		return err
	}
	pkg, err := rpm.ReadPackage(
		w.t.Context(),
		bytes.NewReader(body),
		int64(len(body)),
		"pool/"+rpmFixture,
	)
	if err != nil {
		return err
	}
	pkg.SHA256 = fmt.Sprintf("%x", sha256.Sum256(body))
	meta, err := rpm.EncodeMetadata([]rpm.Package{pkg}, "", time.Unix(1700000000, 0))
	if err != nil {
		return err
	}
	w.remote = fakeupstream.New(w.t, fakeupstream.Options{})
	w.closers = append(w.closers, w.remote.Close)
	for p, data := range meta.Files {
		w.remote.Set("/"+p, fakeupstream.Resource{Body: data})
	}
	w.remote.Set("/pool/"+rpmFixture, fakeupstream.Resource{Body: body})
	if w.vars == nil {
		w.vars = map[string]string{}
	}
	w.vars["upstream"] = w.remote.URL
	return nil
}

func (w *world) clearRPMRoot() error {
	dir, err := w.filesDir()
	if err != nil {
		return err
	}
	if err := os.RemoveAll(filepath.Join(dir, "rpm-root")); err != nil {
		return err
	}
	return os.RemoveAll(filepath.Join(dir, "dnf-cache"))
}

func (w *world) prepareRPMFixture() error {
	dir, err := w.filesDir()
	if err != nil {
		return err
	}
	body, err := os.ReadFile(
		filepath.Join("..", "..", "internal", "format", "rpm", "testdata", rpmFixture),
	)
	if err != nil {
		return err
	}
	return os.WriteFile(filepath.Join(dir, rpmFixture), body, 0o600)
}

func (w *world) setRPMRepository(key string) error {
	return w.setRPMRepositoryAs(key, "admin", testAdminPassword)
}

func (w *world) setRPMRepositoryAs(key, user, password string) error {
	base, err := url.Parse(w.url)
	if err != nil {
		return err
	}
	if runtime.GOOS == "darwin" && os.Getenv("ARTIFACTWEAVE_RPM_DESKTOP") == "1" {
		base.Host = strings.Replace(base.Host, "127.0.0.1", "host.docker.internal", 1)
	}
	base.User = url.UserPassword(user, password)
	dir, err := w.filesDir()
	if err != nil {
		return err
	}
	return os.WriteFile(filepath.Join(dir, "rpm-baseurl"),
		[]byte(base.String()+"/artifactory/"+key), 0o600)
}

func (w *world) runDNF(args string) error {
	dir, err := w.filesDir()
	if err != nil {
		return err
	}
	base, err := os.ReadFile(filepath.Join(dir, "rpm-baseurl"))
	if err != nil {
		return err
	}
	uid, gid := os.Getuid(), os.Getgid()
	repoCheck := "0"
	if w.rpmSigned {
		repoCheck = "1"
	}
	cmd := "dnf -y --disablerepo='*' --repofrompath=artifactweave,'" + strings.TrimSpace(
		string(base),
	) + "' " +
		"--setopt=artifactweave.gpgcheck=0 --setopt=artifactweave.repo_gpgcheck=" + repoCheck + " " +
		"--setopt=artifactweave.gpgkey=file:///work/rpm-sign.pub " +
		"--setopt=cachedir=/work/dnf-cache --installroot=/work/rpm-root " + args +
		fmt.Sprintf(
			"; code=$?; chown -R %d:%d /work/rpm-root /work/dnf-cache 2>/dev/null; exit $code",
			uid,
			gid,
		)
	return w.runContainer(w.fedoraImage, []string{"--user", "0:0"}, []string{"sh", "-lc", cmd})
}
