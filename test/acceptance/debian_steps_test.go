package acceptance

import (
	"bytes"
	"context"
	"crypto/md5" //nolint:gosec // Debian Packages uses MD5 as a wire checksum.
	"crypto/sha256"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"net/url"
	"os"
	"path/filepath"
	"runtime"
	"strings"
	"time"

	"github.com/cucumber/godog"

	"github.com/weaveplatform/artifactweave/internal/fakeupstream"
	"github.com/weaveplatform/artifactweave/internal/format/debian"
	"github.com/weaveplatform/artifactweave/internal/format/terraform/terraformtest"
	"github.com/weaveplatform/artifactweave/pkg/types"
)

func (w *world) registerDebian(sc *godog.ScenarioContext) {
	sc.Step(`^I create a Debian signing pair "([^"]*)"$`, w.createDebianSigningPair)
	sc.Step(`^a dpkg-built Debian package "([^"]*)" at "([^"]*)" under "([^"]*)"$`,
		w.copyDebianPackage)
	sc.Step(`^a dpkg-built Debian package "([^"]*)" at "([^"]*)" variant "([01])" under "([^"]*)"$`,
		w.copyDebianPackageVariant)
	sc.Step(`^the Debian response body matches the file "([^"]*)"$`, w.debianBodyMatchesFile)
	sc.Step(`^I GET the Debian by-hash index for "([^"]*)" as "([^"]*)" with password "([^"]*)"$`,
		w.getDebianByHashAs)
	sc.Step(
		`^I deploy the Debian file "([^"]*)" to "([^"]*)" as "([^"]*)" with password "([^"]*)"$`,
		w.deployDebianAs,
	)
	sc.Step(`^a dpkg-built Debian source package under "([^"]*)"$`, w.copyDebianSource)
	sc.Step(`^the apt repository is "([^"]*)"$`, w.setAptRepository)
	sc.Step(`^the apt source repository is "([^"]*)"$`, w.setAptSourceRepository)
	sc.Step(`^the apt snapshot repository is "([^"]*)" at tag "([^"]*)"$`,
		w.setAptSnapshotRepository)
	sc.Step(`^I run apt "([^"]*)"$`, w.runApt)
	sc.Step(`^a Debian upstream serves "([^"]*)" at "([^"]*)"$`, w.debianUpstream)
	sc.Step(`^the Debian upstream redirects "([^"]*)" at "([^"]*)"$`, w.debianRedirect)
	sc.Step(`^a Debian upstream serves the complete source package$`, w.debianSourceUpstream)
	sc.Step(`^I clear the apt client cache$`, w.clearAptCache)
	sc.Step(`^the Debian upstream publishes an invalid Packages index$`, w.debianInvalidIndex)
	sc.Step(`^the Debian upstream corrupts "([^"]*)" at "([^"]*)"$`, w.debianCorruptPackage)
}

func (w *world) getDebianByHashAs(repoKey, user, password string) error {
	const index = "dists/stable/main/binary-amd64/Packages.gz"
	if err := w.send(http.MethodGet, "/artifactory/"+repoKey+"/"+index, "",
		func(r *http.Request) { r.SetBasicAuth("admin", testAdminPassword) }); err != nil {
		return err
	}
	if w.status != http.StatusOK {
		return fmt.Errorf("read Debian index as admin: status %d", w.status)
	}
	checksum := sha256.Sum256(w.body)
	byHash := "/artifactory/" + repoKey +
		"/dists/stable/main/binary-amd64/by-hash/SHA256/" + fmt.Sprintf("%x", checksum)
	return w.send(http.MethodGet, byHash, "",
		func(r *http.Request) { r.SetBasicAuth(user, password) })
}

func (w *world) createDebianSigningPair(name string) error {
	key, err := terraformtest.NewKey()
	if err != nil {
		return err
	}
	dir, err := w.filesDir()
	if err != nil {
		return err
	}
	if err := os.WriteFile(
		filepath.Join(dir, "debian.asc"),
		[]byte(key.Public),
		0o644,
	); err != nil {
		return err
	}
	w.debianPrivate = key.Private
	body, err := json.Marshal(map[string]string{
		"pairName": name, "alias": name, "pairType": "GPG",
		"publicKey": key.Public, "privateKey": key.Private,
	})
	if err != nil {
		return err
	}
	return w.send(http.MethodPost, "/artifactory/api/security/keypair", string(body),
		func(r *http.Request) { r.SetBasicAuth("admin", testAdminPassword) })
}

func (w *world) debianUpstream(name, version string) error {
	if w.debianPrivate == "" {
		return fmt.Errorf("Debian signing pair is missing")
	}
	if strings.ContainsAny(name+version, "/\\") {
		return fmt.Errorf("unsafe Debian fixture coordinates")
	}
	archive, err := os.ReadFile(filepath.Join("..", "..", "internal", "format", "debian",
		"testdata", name+"_"+version+"_amd64_0.deb"))
	if err != nil {
		return err
	}
	binary, err := debian.ReadBinary(w.t.Context(), bytes.NewReader(archive), int64(len(archive)))
	if err != nil {
		return err
	}
	packagePath := "pool/main/a/" + name + "/" + name + "_" + version + "_amd64.deb"
	packages, err := debian.Packages([]debian.IndexedBinary{{
		Binary: binary, Path: packagePath, Size: int64(len(archive)),
		Checksums: types.Checksums{
			MD5:    fmt.Sprintf("%x", md5.Sum(archive)),
			SHA256: fmt.Sprintf("%x", sha256.Sum256(archive)),
		},
	}})
	if err != nil {
		return err
	}
	compressed, err := debian.GzipIndex(packages)
	if err != nil {
		return err
	}
	release, err := debian.Release("stable", []string{"main"}, []string{"amd64"},
		[]debian.ReleaseEntry{
			{Path: "main/binary-amd64/Packages", Body: packages},
			{Path: "main/binary-amd64/Packages.gz", Body: compressed},
		}, time.Now().UTC())
	if err != nil {
		return err
	}
	signed, err := debian.SignRelease(release, w.debianPrivate, "")
	if err != nil {
		return err
	}
	w.remote = fakeupstream.New(w.t, fakeupstream.Options{})
	w.closers = append(w.closers, w.remote.Close)
	base := "/dists/stable/"
	w.remote.Set(base+"InRelease", fakeupstream.Resource{Body: signed.InRelease})
	w.remote.Set(base+"Release", fakeupstream.Resource{Body: release})
	w.remote.Set(base+"Release.gpg", fakeupstream.Resource{Body: signed.ReleaseGPG})
	w.remote.Set(base+"main/binary-amd64/Packages", fakeupstream.Resource{Body: packages})
	w.remote.Set(base+"main/binary-amd64/Packages.gz", fakeupstream.Resource{Body: compressed})
	for _, index := range []struct {
		body []byte
	}{
		{packages}, {compressed},
	} {
		checksum := sha256.Sum256(index.body)
		w.remote.Set(base+"main/binary-amd64/by-hash/SHA256/"+fmt.Sprintf("%x", checksum),
			fakeupstream.Resource{Body: index.body})
	}
	w.remote.Set("/"+packagePath, fakeupstream.Resource{Body: archive})
	if w.vars == nil {
		w.vars = map[string]string{}
	}
	w.vars["upstream"] = w.remote.URL
	return nil
}

func (w *world) debianSourceUpstream() error {
	if w.debianPrivate == "" {
		return fmt.Errorf("Debian signing pair is missing")
	}
	folder := "pool/main/a/alpha/"
	names := []string{
		"alpha_1.0-1.dsc", "alpha_1.0.orig.tar.gz",
		"alpha_1.0-1.debian.tar.xz",
	}
	files := map[string][]byte{}
	checksums := map[string]types.Checksums{}
	for _, name := range names {
		body, err := os.ReadFile(filepath.Join("..", "..", "internal", "format", "debian",
			"testdata", name))
		if err != nil {
			return err
		}
		files[name] = body
		checksums[name] = types.Checksums{
			MD5:    fmt.Sprintf("%x", md5.Sum(body)), //nolint:gosec // Debian wire checksum.
			SHA256: fmt.Sprintf("%x", sha256.Sum256(body)),
		}
	}
	dsc := names[0]
	src, err := debian.ReadSource(
		w.t.Context(),
		bytes.NewReader(files[dsc]),
		int64(len(files[dsc])),
	)
	if err != nil {
		return err
	}
	entry := debian.IndexedSource{
		Source: src, Path: folder + dsc, Size: int64(len(files[dsc])),
		Checksums: checksums[dsc],
	}
	for _, part := range src.Parts {
		entry.Archives = append(entry.Archives, debian.SourceArchive{
			Name: part.Name, Size: int64(len(files[part.Name])),
			Checksums: checksums[part.Name],
		})
	}
	index, err := debian.Sources([]debian.IndexedSource{entry})
	if err != nil {
		return err
	}
	compressed, err := debian.GzipIndex(index)
	if err != nil {
		return err
	}
	release, err := debian.Release("stable", []string{"main"}, []string{"amd64"},
		[]debian.ReleaseEntry{
			{Path: "main/source/Sources", Body: index},
			{Path: "main/source/Sources.gz", Body: compressed},
		}, time.Now().UTC())
	if err != nil {
		return err
	}
	signed, err := debian.SignRelease(release, w.debianPrivate, "")
	if err != nil {
		return err
	}
	w.remote = fakeupstream.New(w.t, fakeupstream.Options{})
	w.closers = append(w.closers, w.remote.Close)
	base := "/dists/stable/"
	for path, body := range map[string][]byte{
		"InRelease": signed.InRelease, "Release": release,
		"Release.gpg":         signed.ReleaseGPG,
		"main/source/Sources": index, "main/source/Sources.gz": compressed,
	} {
		w.remote.Set(base+path, fakeupstream.Resource{Body: body})
	}
	for _, body := range [][]byte{index, compressed} {
		sha := sha256.Sum256(body)
		w.remote.Set(base+"main/source/by-hash/SHA256/"+fmt.Sprintf("%x", sha),
			fakeupstream.Resource{Body: body})
	}
	for name, body := range files {
		w.remote.Set("/"+folder+name, fakeupstream.Resource{Body: body})
	}
	if w.vars == nil {
		w.vars = map[string]string{}
	}
	w.vars["upstream"] = w.remote.URL
	return nil
}

func (w *world) debianRedirect(name, version string) error {
	if strings.ContainsAny(name+version, "/\\") {
		return fmt.Errorf("unsafe Debian fixture coordinates")
	}
	archive, err := os.ReadFile(filepath.Join("..", "..", "internal", "format", "debian",
		"testdata", name+"_"+version+"_amd64_0.deb"))
	if err != nil {
		return err
	}
	original := "/pool/main/a/" + name + "/" + name + "_" + version + "_amd64.deb"
	destination := "/mirrors/" + name + "_" + version + "_amd64.deb"
	w.remote.Set(original, fakeupstream.Resource{Redirect: w.remote.URL + destination})
	w.remote.Set(destination, fakeupstream.Resource{Body: archive})
	return nil
}

func (w *world) clearAptCache() error {
	dir, err := w.filesDir()
	if err != nil {
		return err
	}
	for _, name := range []string{"lists", "archives"} {
		if err := os.RemoveAll(filepath.Join(dir, name)); err != nil {
			return err
		}
		if err := os.MkdirAll(filepath.Join(dir, name, "partial"), 0o755); err != nil {
			return err
		}
	}
	for _, name := range []string{
		"alpha_1.0-1.dsc", "alpha_1.0.orig.tar.gz",
		"alpha_1.0-1.debian.tar.xz",
	} {
		if err := os.Remove(filepath.Join(dir, name)); err != nil && !os.IsNotExist(err) {
			return err
		}
	}
	return nil
}

func (w *world) debianInvalidIndex() error {
	w.remote.Set("/dists/stable/main/binary-amd64/Packages.gz",
		fakeupstream.Resource{Body: []byte("invalid Packages index")})
	return nil
}

func (w *world) debianCorruptPackage(name, version string) error {
	if strings.ContainsAny(name+version, "/\\") {
		return fmt.Errorf("unsafe Debian fixture coordinates")
	}
	p := "/pool/main/a/" + name + "/" + name + "_" + version + "_amd64.deb"
	w.remote.Set(p, fakeupstream.Resource{Body: []byte("invalid Debian archive")})
	return nil
}

func (w *world) copyDebianPackage(name, version, folder string) error {
	return w.copyDebianPackageVariant(name, version, "0", folder)
}

func (w *world) copyDebianPackageVariant(name, version, variant, folder string) error {
	if strings.ContainsAny(name+version+folder, "/\\") {
		return fmt.Errorf("unsafe Debian fixture coordinates")
	}
	if variant != "0" && variant != "1" {
		return fmt.Errorf("unknown Debian fixture variant")
	}
	content, err := os.ReadFile(filepath.Join("..", "..", "internal", "format", "debian",
		"testdata", name+"_"+version+"_amd64_"+variant+".deb"))
	if err != nil {
		return err
	}
	dir, err := w.filesDir()
	if err != nil {
		return err
	}
	if err := os.MkdirAll(filepath.Join(dir, folder), 0o750); err != nil {
		return err
	}
	return os.WriteFile(filepath.Join(dir, folder, name+"_"+version+"_amd64.deb"),
		content, 0o600)
}

func (w *world) debianBodyMatchesFile(name string) error {
	dir, err := w.filesDir()
	if err != nil {
		return err
	}
	if filepath.Clean(name) != name || strings.HasPrefix(name, "../") ||
		filepath.IsAbs(name) {
		return fmt.Errorf("unsafe Debian expected file path")
	}
	want, err := os.ReadFile(filepath.Join(dir, name))
	if err != nil {
		return err
	}
	if !bytes.Equal(w.body, want) {
		return fmt.Errorf("Debian response content differs from %s", name)
	}
	return nil
}

func (w *world) deployDebianAs(name, target, user, password string) error {
	dir, err := w.filesDir()
	if err != nil {
		return err
	}
	archive, err := os.ReadFile(filepath.Join(dir, name))
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

func (w *world) copyDebianSource(folder string) error {
	if strings.ContainsAny(folder, "/\\") {
		return fmt.Errorf("unsafe Debian source fixture folder")
	}
	dir, err := w.filesDir()
	if err != nil {
		return err
	}
	if err := os.MkdirAll(filepath.Join(dir, folder), 0o750); err != nil {
		return err
	}
	for _, name := range []string{
		"alpha_1.0-1.dsc", "alpha_1.0.orig.tar.gz", "alpha_1.0-1.debian.tar.xz",
	} {
		content, err := os.ReadFile(filepath.Join("..", "..", "internal", "format", "debian",
			"testdata", name))
		if err != nil {
			return err
		}
		if err := os.WriteFile(filepath.Join(dir, folder, name), content, 0o600); err != nil {
			return err
		}
	}
	return nil
}

func (w *world) setAptRepository(key string) error {
	return w.setAptRepositoryKind(key, "deb")
}

func (w *world) setAptSourceRepository(key string) error {
	return w.setAptRepositoryKind(key, "deb-src")
}

func (w *world) setAptSnapshotRepository(key, tag string) error {
	return w.setAptRepositoryURL("/artifactory/api/deb/"+key+"/snapshots/"+tag, "deb")
}

func (w *world) setAptRepositoryKind(key, kind string) error {
	return w.setAptRepositoryURL("/artifactory/"+key, kind)
}

func (w *world) setAptRepositoryURL(repoPath, kind string) error {
	dir, err := w.filesDir()
	if err != nil {
		return err
	}
	base := w.url
	if runtime.GOOS == "darwin" && os.Getenv("ARTIFACTWEAVE_DEBIAN_DESKTOP") == "1" {
		base = strings.Replace(base, "127.0.0.1", "host.docker.internal", 1)
	}
	u, err := url.Parse(base)
	if err != nil {
		return err
	}
	sources := kind + " [signed-by=/work/debian.asc] " + base + repoPath +
		" stable main\n"
	if err := os.WriteFile(filepath.Join(dir, "sources.list"), []byte(sources), 0o644); err != nil {
		return err
	}
	auth := "machine " + u.Scheme + "://" + u.Host +
		" login admin password " + testAdminPassword + "\n"
	if err := os.WriteFile(filepath.Join(dir, "auth.conf"), []byte(auth), 0o600); err != nil {
		return err
	}
	for _, path := range []string{"lists/partial", "archives/partial"} {
		if err := os.MkdirAll(filepath.Join(dir, path), 0o755); err != nil {
			return err
		}
	}
	return nil
}

func (w *world) runApt(args string) error {
	command := []string{
		"apt-get",
		"-o", "Dir::Etc::sourcelist=/work/sources.list",
		"-o", "Dir::Etc::sourceparts=-",
		"-o", "Dir::Etc::netrc=/work/auth.conf",
		"-o", "Dir::State::lists=/work/lists",
		"-o", "Dir::Cache::archives=/work/archives",
		"-o", "Acquire::Languages=none",
	}
	command = append(command, strings.Fields(w.expand(args))...)
	return w.runContainer(w.debianImage, []string{
		"--platform", "linux/amd64", "--user", "0:0", "--entrypoint", "apt-get",
		"-e", "DEBIAN_FRONTEND=noninteractive",
	}, command[1:])
}
