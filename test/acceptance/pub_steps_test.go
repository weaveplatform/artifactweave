package acceptance

import (
	"archive/tar"
	"bytes"
	"compress/gzip"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"fmt"
	"net/http"
	"os"
	"path/filepath"
	"slices"
	"strings"

	"github.com/cucumber/godog"

	"github.com/weaveplatform/artifactweave/internal/fakeupstream"
)

type pubFixtureVersion struct {
	Version       string         `json:"version"`
	ArchiveURL    string         `json:"archive_url"`
	ArchiveSHA256 string         `json:"archive_sha256"`
	Pubspec       map[string]any `json:"pubspec"`
}

func (w *world) registerPub(sc *godog.ScenarioContext) {
	sc.Step(`^the Dart client uses pub repository "([^"]*)" with token "([^"]*)"$`, w.dartConfig)
	sc.Step(`^I run dart in "([^"]*)" "([^"]*)"$`, w.runDart)
	sc.Step(`^a Dart pub upstream serves package "([^"]*)" version "([^"]*)"$`, w.dartUpstream)
	sc.Step(
		`^the Dart pub upstream redirects the archive for "([^"]*)" version "([^"]*)"$`,
		w.dartUpstreamRedirect,
	)
	sc.Step(
		`^the Dart pub upstream corrupts the archive for "([^"]*)" version "([^"]*)"$`,
		w.dartCorruptUpstream,
	)
	sc.Step(
		`^a Dart pub archive "([^"]*)" version "([^"]*)" is saved as "([^"]*)"$`,
		w.dartSaveArchive,
	)
	sc.Step(`^I clear the Dart pub client cache$`, w.dartClearCache)
}

func (w *world) dartConfig(repoKey, tokenName string) error {
	if err := w.clientReachable("Dart"); err != nil {
		return err
	}
	token := w.vars[tokenName]
	if token == "" {
		return fmt.Errorf("missing Dart pub token %q", tokenName)
	}
	if w.dartFrontURL == "" {
		if err := w.frontWithTLSBase(true); err != nil {
			return err
		}
		w.dartFrontURL = "https://" + w.vars["tlshost"]
		if os.Getenv("ARTIFACTWEAVE_DART_DESKTOP") == "1" {
			w.dartFrontURL = strings.Replace(w.dartFrontURL, "127.0.0.1", "example.com", 1)
		}
	}
	w.dartRepo, w.dartToken = repoKey, token
	clientURL := w.dartClientURL()
	w.vars["darturl"] = clientURL
	if err := w.runDart(
		".",
		"pub token add "+clientURL+"/artifactory/api/pub/"+repoKey+" --env-var AW_PUB_TOKEN",
	); err != nil {
		return err
	}
	if w.lastRC != 0 {
		return fmt.Errorf("Dart pub token setup failed: %s", w.body)
	}
	return nil
}

func (w *world) dartClientURL() string {
	return w.dartFrontURL
}

func (w *world) runDart(dir, args string) error {
	if w.dartRepo == "" || w.dartToken == "" {
		return fmt.Errorf("Dart pub repository and token are not configured")
	}
	if dir == ".." || strings.HasPrefix(dir, "../") || filepath.IsAbs(dir) {
		return fmt.Errorf("invalid Dart project directory")
	}
	url := w.dartClientURL() + "/artifactory/api/pub/" + w.dartRepo
	flags := []string{
		"--user", "0:0", "--entrypoint", "/usr/local/bin/artifactweave-dart",
		"-w", "/work/" + dir,
		"-e", "PUB_HOSTED_URL=" + url,
		"-e", "PUB_CACHE=/work/.pub-cache",
		"-e", "AW_PUB_TOKEN=" + w.dartToken,
		"-e", fmt.Sprintf("AW_CLIENT_UID=%d", os.Getuid()),
		"-e", fmt.Sprintf("AW_CLIENT_GID=%d", os.Getgid()),
	}
	if os.Getenv("ARTIFACTWEAVE_DART_DESKTOP") == "1" {
		flags = append(flags, "--add-host", "example.com:host-gateway")
	}
	return w.runContainer(w.dartImage, flags, w.clientArgs(args))
}

func pubTestArchive(name, version string) ([]byte, error) {
	var out bytes.Buffer
	gz := gzip.NewWriter(&out)
	tw := tar.NewWriter(gz)
	files := []struct {
		path string
		data []byte
	}{
		{
			path: "pubspec.yaml",
			data: []byte(fmt.Sprintf(
				"name: %s\nversion: %s\ndescription: remote fixture\nenvironment:\n  sdk: ^3.10.0\n",
				name,
				version,
			)),
		},
		{path: "lib/" + name + ".dart", data: []byte("String message() => 'remote Dart pub';\n")},
	}
	for _, file := range files {
		if err := tw.WriteHeader(
			&tar.Header{Name: file.path, Mode: 0o644, Size: int64(len(file.data))},
		); err != nil {
			return nil, err
		}
		if _, err := tw.Write(file.data); err != nil {
			return nil, err
		}
	}
	if err := tw.Close(); err != nil {
		return nil, err
	}
	if err := gz.Close(); err != nil {
		return nil, err
	}
	return out.Bytes(), nil
}

func (w *world) dartUpstream(name, version string) error {
	if err := w.remoteUpstream("/pub/.ready", "ready"); err != nil {
		return err
	}
	archive, err := pubTestArchive(name, version)
	if err != nil {
		return err
	}
	archivePath := "/pub/packages/" + name + "/versions/" + version + ".tar.gz"
	hash := sha256.Sum256(archive)
	if w.dartHashes == nil {
		w.dartHashes = map[string][32]byte{}
	}
	if before, seen := w.dartHashes[archivePath]; seen && before != hash {
		return fmt.Errorf("Dart pub fixture archive changed for %s %s", name, version)
	}
	w.dartHashes[archivePath] = hash
	if w.dartVersions == nil {
		w.dartVersions = map[string]map[string]pubFixtureVersion{}
	}
	if w.dartVersions[name] == nil {
		w.dartVersions[name] = map[string]pubFixtureVersion{}
	}
	w.dartVersions[name][version] = pubFixtureVersion{
		Version: version, ArchiveURL: w.remote.URL + archivePath,
		ArchiveSHA256: hex.EncodeToString(hash[:]),
		Pubspec: map[string]any{
			"name": name, "version": version,
			"description": "remote fixture",
			"environment": map[string]any{"sdk": "^3.10.0"},
		},
	}
	versions := make([]pubFixtureVersion, 0, len(w.dartVersions[name]))
	for _, entry := range w.dartVersions[name] {
		versions = append(versions, entry)
	}
	slices.SortFunc(versions, func(a, b pubFixtureVersion) int {
		return strings.Compare(a.Version, b.Version)
	})
	doc := map[string]any{"name": name, "versions": versions}
	data, err := json.Marshal(doc)
	if err != nil {
		return err
	}
	w.remote.Set("/pub/api/packages/"+name, fakeupstream.Resource{
		Body: data, Header: http.Header{"Content-Type": {"application/vnd.pub.v2+json"}},
	})
	w.remote.Set(archivePath, fakeupstream.Resource{Body: archive})
	return nil
}

func (w *world) dartUpstreamRedirect(name, version string) error {
	if w.remote == nil {
		return fmt.Errorf("Dart pub upstream is not running")
	}
	path := "/pub/packages/" + name + "/versions/" + version + ".tar.gz"
	archive, err := pubTestArchive(name, version)
	if err != nil {
		return err
	}
	w.remote.Set(
		path,
		fakeupstream.Resource{Redirect: "/pub/blobs/" + name + "-" + version + ".tar.gz"},
	)
	w.remote.Set("/pub/blobs/"+name+"-"+version+".tar.gz", fakeupstream.Resource{Body: archive})
	return nil
}

func (w *world) dartCorruptUpstream(name, version string) error {
	if w.remote == nil {
		return fmt.Errorf("Dart pub upstream is not running")
	}
	w.remote.Set("/pub/packages/"+name+"/versions/"+version+".tar.gz",
		fakeupstream.Resource{Body: []byte("corrupt archive")})
	return nil
}

func (w *world) dartSaveArchive(name, version, file string) error {
	data, err := pubTestArchive(name, version)
	if err != nil {
		return err
	}
	dir, err := w.filesDir()
	if err != nil {
		return err
	}
	full := filepath.Join(dir, file)
	if err := os.MkdirAll(filepath.Dir(full), 0o750); err != nil {
		return err
	}
	return os.WriteFile(full, data, 0o600)
}

func (w *world) dartClearCache() error {
	dir, err := w.filesDir()
	if err != nil {
		return err
	}
	return os.RemoveAll(filepath.Join(dir, ".pub-cache"))
}
