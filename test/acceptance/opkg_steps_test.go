package acceptance

import (
	"bytes"
	"compress/gzip"
	"crypto/md5" //nolint:gosec // Opkg's optional MD5Sum metadata field.
	"encoding/hex"
	"fmt"
	"net/url"
	"os"
	"path/filepath"
	"runtime"
	"strings"

	"github.com/cucumber/godog"

	"github.com/weaveplatform/artifactweave/internal/fakeupstream"
	"github.com/weaveplatform/artifactweave/internal/format/opkg"
)

func (w *world) registerOpkg(sc *godog.ScenarioContext) {
	sc.Step(`^an Opkg package "([^"]*)" version "([^"]*)" variant "([01])" under "([^"]*)"$`,
		w.createOpkgPackage)
	sc.Step(`^the opkg feed is "([^"]*)"$`, w.setOpkgFeed)
	sc.Step(`^I run opkg "([^"]*)"$`, w.runOpkg)
	sc.Step(`^I clear the opkg installation root$`, w.clearOpkgRoot)
	sc.Step(`^an Opkg upstream serves "([^"]*)" version "([^"]*)"$`, w.opkgUpstream)
	sc.Step(`^the Opkg upstream corrupts "([^"]*)" version "([^"]*)"$`,
		w.opkgCorruptUpstream)
	sc.Step(`^the Opkg upstream publishes an invalid index$`, w.opkgInvalidUpstreamIndex)
	sc.Step(`^the Opkg upstream adds beta version "([^"]*)"$`, w.opkgUpstreamAddsBeta)
	sc.Step(`^the Opkg upstream redirects alpha version "([^"]*)"$`, w.opkgUpstreamRedirectsAlpha)
	sc.Step(`^an independent OpenWrt Opkg upstream is available$`, w.opkgOpenWrtUpstream)
	sc.Step(`^the OpenWrt upstream changes the IPK and its MD5 but not SHA256$`,
		w.opkgOpenWrtSHA256Mismatch)
}

func (w *world) createOpkgPackage(name, version, variant, folder string) error {
	if name != "alpha" && name != "beta" ||
		version != "1.0" && version != "2.0" {
		return fmt.Errorf("unavailable controlled Opkg package %s %s", name, version)
	}
	source := filepath.Join("..", "..", "internal", "format", "opkg", "testdata",
		fmt.Sprintf("%s_%s_%s.ipk", name, version, variant))
	body, err := os.ReadFile(source)
	if err != nil {
		return err
	}
	dir, err := w.filesDir()
	if err != nil {
		return err
	}
	p := filepath.Join(dir, folder, fmt.Sprintf("%s_%s_all.ipk", name, version))
	if err := os.MkdirAll(filepath.Dir(p), 0o750); err != nil {
		return err
	}
	return os.WriteFile(p, body, 0o600)
}

func (w *world) setOpkgFeed(key string) error {
	dir, err := w.filesDir()
	if err != nil {
		return err
	}
	base := w.url
	if runtime.GOOS == "darwin" && os.Getenv("ARTIFACTWEAVE_OPKG_DESKTOP") == "1" {
		base = strings.Replace(base, "127.0.0.1", "host.docker.internal", 1)
	}
	u, err := url.Parse(base)
	if err != nil {
		return err
	}
	u.User = url.UserPassword("admin", testAdminPassword)
	for _, name := range []string{"opkg-root", "opkg-lists", "opkg-cache", "opkg-tmp"} {
		if err := os.MkdirAll(filepath.Join(dir, name), 0o750); err != nil {
			return err
		}
	}
	config := fmt.Sprintf("src/gz controlled %s/artifactory/%s/feed\narch all 10\narch x86_64 20\n",
		u.String(), key)
	return os.WriteFile(filepath.Join(dir, "opkg.conf"), []byte(config), 0o600)
}

func (w *world) runOpkg(args string) error {
	cmd := []string{
		"opkg", "-f", "/work/opkg.conf", "-o", "/work/opkg-root",
		"-l", "/work/opkg-lists", "--cache-dir", "/work/opkg-cache",
		"-t", "/work/opkg-tmp",
	}
	cmd = append(cmd, strings.Fields(w.expand(args))...)
	return w.runContainer(w.opkgImage, nil, cmd)
}

func (w *world) clearOpkgRoot() error {
	dir, err := w.filesDir()
	if err != nil {
		return err
	}
	for _, name := range []string{"opkg-root", "opkg-lists", "opkg-cache", "opkg-tmp"} {
		if err := os.RemoveAll(filepath.Join(dir, name)); err != nil {
			return err
		}
		if err := os.MkdirAll(filepath.Join(dir, name), 0o750); err != nil {
			return err
		}
	}
	return nil
}

func (w *world) opkgUpstream(name, version string) error {
	dir, err := w.filesDir()
	if err != nil {
		return err
	}
	p := fmt.Sprintf("%s_%s_all.ipk", name, version)
	body, err := os.ReadFile(filepath.Join(dir, "ipk", p))
	if err != nil {
		return err
	}
	pkg, err := opkg.ReadPackage(w.t.Context(), bytes.NewReader(body), int64(len(body)),
		"feed/"+p)
	if err != nil {
		return err
	}
	pkg.SHA256 = opkg.Digest(body)
	md5sum := md5.Sum(body) //nolint:gosec // Wire compatibility with Opkg's MD5Sum field.
	pkg.MD5 = hex.EncodeToString(md5sum[:])
	files, err := opkg.EncodeIndex([]opkg.Package{pkg}, "feed")
	if err != nil {
		return err
	}
	w.remote = fakeupstream.New(w.t, fakeupstream.Options{})
	w.closers = append(w.closers, w.remote.Close)
	for name, data := range files {
		w.remote.Set("/feed/"+name, fakeupstream.Resource{Body: data})
	}
	w.remote.Set("/feed/"+p, fakeupstream.Resource{Body: body})
	if w.vars == nil {
		w.vars = map[string]string{}
	}
	w.vars["upstream"] = w.remote.URL
	return nil
}

func (w *world) opkgCorruptUpstream(name, version string) error {
	dir, err := w.filesDir()
	if err != nil {
		return err
	}
	p := fmt.Sprintf("%s_%s_all.ipk", name, version)
	body, err := os.ReadFile(filepath.Join(dir, "ipk", p))
	if err != nil {
		return err
	}
	if len(body) < 2 {
		return fmt.Errorf("Opkg fixture is too small")
	}
	body[len(body)-2] ^= 0xff
	w.remote.Set("/feed/"+p, fakeupstream.Resource{Body: body})
	return nil
}

func (w *world) opkgInvalidUpstreamIndex() error {
	w.remote.Set("/feed/Packages", fakeupstream.Resource{Body: []byte("invalid Opkg index")})
	w.remote.Set("/feed/Packages.gz", fakeupstream.Resource{Body: []byte("invalid Opkg index")})
	return nil
}

func (w *world) opkgUpstreamAddsBeta(version string) error {
	if err := w.createOpkgPackage("beta", version, "0", "ipk"); err != nil {
		return err
	}
	dir, err := w.filesDir()
	if err != nil {
		return err
	}
	packages := make([]opkg.Package, 0, 2)
	for _, name := range []string{"alpha", "beta"} {
		filename := fmt.Sprintf("%s_%s_all.ipk", name, version)
		body, err := os.ReadFile(filepath.Join(dir, "ipk", filename))
		if err != nil {
			return err
		}
		pkg, err := opkg.ReadPackage(w.t.Context(), bytes.NewReader(body), int64(len(body)),
			"feed/"+filename)
		if err != nil {
			return err
		}
		pkg.SHA256 = opkg.Digest(body)
		md5sum := md5.Sum(body) //nolint:gosec // Opkg's wire MD5Sum field.
		pkg.MD5 = hex.EncodeToString(md5sum[:])
		packages = append(packages, pkg)
		w.remote.Set("/feed/"+filename, fakeupstream.Resource{Body: body})
	}
	files, err := opkg.EncodeIndex(packages, "feed")
	if err != nil {
		return err
	}
	for name, data := range files {
		w.remote.Set("/feed/"+name, fakeupstream.Resource{Body: data})
	}
	return nil
}

func (w *world) opkgUpstreamRedirectsAlpha(version string) error {
	dir, err := w.filesDir()
	if err != nil {
		return err
	}
	filename := fmt.Sprintf("alpha_%s_all.ipk", version)
	body, err := os.ReadFile(filepath.Join(dir, "ipk", filename))
	if err != nil {
		return err
	}
	w.remote.Set("/redirected/"+filename, fakeupstream.Resource{Body: body})
	w.remote.Set("/feed/"+filename, fakeupstream.Resource{
		Redirect: w.remote.URL + "/redirected/" + filename,
	})
	return nil
}

// Serve metadata copied from OpenWrt's published feed. The upstream bytes
// must not be built by the adapter under test.
func (w *world) opkgOpenWrtUpstream() error {
	base := filepath.Join("..", "..", "internal", "format", "opkg", "testdata")
	raw, err := os.ReadFile(filepath.Join(base, "openwrt-Packages"))
	if err != nil {
		return err
	}
	ipk, err := os.ReadFile(filepath.Join(base, "464xlat_13_x86_64.ipk"))
	if err != nil {
		return err
	}
	w.remote = fakeupstream.New(w.t, fakeupstream.Options{})
	w.closers = append(w.closers, w.remote.Close)
	if err := w.setOpenWrtResources(raw, ipk); err != nil {
		return err
	}
	if w.vars == nil {
		w.vars = map[string]string{}
	}
	w.vars["upstream"] = w.remote.URL
	return nil
}

func (w *world) setOpenWrtResources(raw, ipk []byte) error {
	// The pinned opkg 0.7.0 build verifies MD5Sum, whereas the published
	// OpenWrt 24.10 feed has SHA256sum only. Add an independently computed
	// MD5 to the original record; Artifactweave still enforces its SHA256.
	md5sum := md5.Sum(ipk) //nolint:gosec // Opkg client wire compatibility.
	needle := []byte("Filename: 464xlat_13_x86_64.ipk\n")
	if !bytes.Contains(raw, needle) {
		return fmt.Errorf("published OpenWrt fixture lost 464xlat record")
	}
	raw = bytes.Replace(raw, needle,
		[]byte(fmt.Sprintf("Filename: 464xlat_13_x86_64.ipk\nMD5Sum: %x\n", md5sum)), 1)
	var buf bytes.Buffer
	z := gzip.NewWriter(&buf)
	if _, err := z.Write(raw); err != nil {
		return err
	}
	if err := z.Close(); err != nil {
		return err
	}
	w.remote.Set("/feed/Packages", fakeupstream.Resource{Body: raw})
	w.remote.Set("/feed/Packages.gz", fakeupstream.Resource{Body: buf.Bytes()})
	w.remote.Set("/feed/464xlat_13_x86_64.ipk", fakeupstream.Resource{Body: ipk})
	return nil
}

func (w *world) opkgOpenWrtSHA256Mismatch() error {
	base := filepath.Join("..", "..", "internal", "format", "opkg", "testdata")
	raw, err := os.ReadFile(filepath.Join(base, "openwrt-Packages"))
	if err != nil {
		return err
	}
	ipk, err := os.ReadFile(filepath.Join(base, "464xlat_13_x86_64.ipk"))
	if err != nil {
		return err
	}
	// gzip's mtime header is not part of its checksum. The IPK stays valid,
	// but its bytes differ from the original published SHA256sum.
	ipk[4] ^= 1
	if _, err := opkg.ReadPackage(w.t.Context(), bytes.NewReader(ipk), int64(len(ipk)),
		"feed/464xlat_13_x86_64.ipk"); err != nil {
		return fmt.Errorf("changed authentic IPK must remain valid: %w", err)
	}
	return w.setOpenWrtResources(raw, ipk)
}
