package acceptance

import (
	"archive/tar"
	"bytes"
	"compress/gzip"
	"crypto/sha256"
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"runtime"
	"strings"

	"github.com/cucumber/godog"

	"github.com/weaveplatform/artifactweave/internal/fakeupstream"
	podfmt "github.com/weaveplatform/artifactweave/internal/format/cocoapods"
)

func (w *world) registerCocoaPods(sc *godog.ScenarioContext) {
	sc.Step(
		`^the CocoaPods upstream received (\d+) (GET|POST) requests for "([^"]*)"$`,
		w.remoteRequests,
	)
	sc.Step(
		`^a CocoaPods Git upstream serves a pod with a pinned submodule$`,
		w.cocoaPodsGitUpstream,
	)
	sc.Step(`^a CocoaPods project for repository "([^"]*)"$`, w.cocoaPodsProject)
	sc.Step(`^a CocoaPods archive named "([^"]*)" version "([^"]*)"$`, w.cocoaPodsArchive)
	sc.Step(
		`^a static Ruby CocoaPods archive named "([^"]*)" version "([^"]*)"$`,
		w.staticRubyCocoaPodsArchive,
	)
	sc.Step(`^I run pod "([^"]*)"$`, w.runPod)
	sc.Step(`^a CocoaPods HTTP upstream serves the archive "([^"]*)"$`, w.cocoaPodsHTTPUpstream)
	sc.Step(`^the CocoaPods client cache is cleared$`, w.clearCocoaPodsClient)
	sc.Step(
		`^the CocoaPods upstream advertises a wrong archive checksum$`,
		w.cocoaPodsWrongChecksum,
	)
	sc.Step(`^the CocoaPods upstream fails its version index$`, w.cocoaPodsIndexFault)
	sc.Step(
		`^the CocoaPods upstream redirects the source archive "([^"]*)"$`,
		w.cocoaPodsSourceRedirect,
	)
	sc.Step(
		`^the CocoaPods upstream redirects the source archive "([^"]*)" to an approved origin$`,
		w.cocoaPodsCrossOriginRedirect,
	)
	sc.Step(
		`^the CocoaPods registry starts with its archive origin approved$`,
		w.cocoaPodsStartWithArchiveOrigin,
	)
	sc.Step(
		`^the approved CocoaPods source received the archive without upstream credentials$`,
		w.cocoaPodsSourceUncredentialed,
	)
}

func (w *world) cocoaPodsCrossOriginRedirect(archiveName string) error {
	if w.remote == nil {
		return fmt.Errorf("CocoaPods upstream is not running")
	}
	dir, err := w.filesDir()
	if err != nil {
		return err
	}
	archive, err := os.ReadFile(filepath.Join(dir, archiveName))
	if err != nil {
		return err
	}
	w.podSource = fakeupstream.New(w.t, fakeupstream.Options{})
	w.closers = append(w.closers, w.podSource.Close)
	w.podSource.Set("/approved/"+archiveName, fakeupstream.Resource{Body: archive})
	w.remote.Set("/"+archiveName, fakeupstream.Resource{
		Redirect: w.podSource.URL + "/approved/" + archiveName,
	})
	w.vars["pod_source"] = w.podSource.URL
	return nil
}

func (w *world) cocoaPodsStartWithArchiveOrigin() error {
	if w.podSource == nil {
		return fmt.Errorf("approved CocoaPods source is not running")
	}
	w.env = append(w.env, "ARTIFACTWEAVE_REMOTE_ALLOWED_PRIVATE_ORIGINS="+w.podSource.URL)
	return w.start()
}

func (w *world) cocoaPodsSourceUncredentialed() error {
	if w.podSource == nil {
		return fmt.Errorf("approved CocoaPods source is not running")
	}
	archiveGET := 0
	for _, request := range w.podSource.Requests() {
		if request.Path != "/approved/HelloPod-1.0.0.tar.gz" {
			continue
		}
		if request.Method == "GET" {
			archiveGET++
		}
		if request.Header.Get("Authorization") != "" || request.Header.Get("Cookie") != "" {
			return fmt.Errorf("approved CocoaPods source received upstream credentials")
		}
	}
	if archiveGET == 0 {
		return fmt.Errorf("approved CocoaPods source received no archive GET")
	}
	return nil
}

func (w *world) cocoaPodsSourceRedirect(archiveName string) error {
	if w.remote == nil {
		return fmt.Errorf("CocoaPods upstream is not running")
	}
	dir, err := w.filesDir()
	if err != nil {
		return err
	}
	archive, err := os.ReadFile(filepath.Join(dir, archiveName))
	if err != nil {
		return err
	}
	redirected := "/moved/" + archiveName
	w.remote.Set(redirected, fakeupstream.Resource{Body: archive})
	w.remote.Set("/"+archiveName, fakeupstream.Resource{Redirect: w.remote.URL + redirected})
	return nil
}

func (w *world) cocoaPodsWrongChecksum() error {
	if w.remote == nil {
		return fmt.Errorf("CocoaPods upstream is not running")
	}
	spec, err := json.Marshal(map[string]any{
		"name":      "HelloPod",
		"version":   "1.0.0",
		"summary":   "Controlled upstream pod",
		"homepage":  "https://example.org/HelloPod",
		"license":   map[string]string{"type": "MIT"},
		"authors":   map[string]string{"Example": "example@example.org"},
		"platforms": map[string]string{"ios": "12.0"},
		"source": map[string]string{
			"http":   w.remote.URL + "/HelloPod-1.0.0.tar.gz",
			"sha256": strings.Repeat("0", 64),
		},
		"source_files": "Sources/*.{h,m}",
	})
	if err != nil {
		return err
	}
	w.remote.Set("/"+podfmt.SpecPath("HelloPod", "1.0.0"), fakeupstream.Resource{Body: spec})
	return nil
}

func (w *world) cocoaPodsIndexFault() error {
	if w.remote == nil {
		return fmt.Errorf("CocoaPods upstream is not running")
	}
	w.remote.Set("/"+podfmt.ShardFile("HelloPod"), fakeupstream.Resource{Status: 503})
	return nil
}

func (w *world) cocoaPodsHTTPUpstream(archiveName string) error {
	if err := w.remoteUpstream("/health", "up"); err != nil {
		return err
	}
	dir, err := w.filesDir()
	if err != nil {
		return err
	}
	archive, err := os.ReadFile(filepath.Join(dir, archiveName))
	if err != nil {
		return err
	}
	name, version, ok := strings.Cut(strings.TrimSuffix(archiveName, ".tar.gz"), "-")
	if !ok {
		return fmt.Errorf("invalid CocoaPods test archive %s", archiveName)
	}
	checksum := fmt.Sprintf("%x", sha256.Sum256(archive))
	spec, err := json.Marshal(map[string]any{
		"name":      name,
		"version":   version,
		"summary":   "Controlled upstream pod",
		"homepage":  "https://example.org/HelloPod",
		"license":   map[string]string{"type": "MIT"},
		"authors":   map[string]string{"Example": "example@example.org"},
		"platforms": map[string]string{"ios": "12.0"},
		"source": map[string]string{
			"http":   w.remote.URL + "/" + archiveName,
			"sha256": checksum,
		},
		"source_files": "Sources/*.{h,m}",
	})
	if err != nil {
		return err
	}
	w.remote.Set(
		"/CocoaPods-version.yml",
		fakeupstream.Resource{
			Body: []byte("---\nmin: 1.0.0\nlast: 1.17.0\nprefix_lengths:\n- 1\n- 1\n- 1\n"),
		},
	)
	w.remote.Set("/all_pods.txt", fakeupstream.Resource{Body: []byte(name + "\n")})
	w.remote.Set(
		"/"+podfmt.ShardFile(name),
		fakeupstream.Resource{Body: []byte(name + "/" + version + "\n")},
	)
	w.remote.Set("/"+podfmt.SpecPath(name, version), fakeupstream.Resource{
		Body: spec, Header: map[string][]string{"Content-Type": {"application/json"}},
	})
	w.remote.Set("/"+archiveName, fakeupstream.Resource{Body: archive})
	return nil
}

func (w *world) clearCocoaPodsClient() error {
	dir, err := w.filesDir()
	if err != nil {
		return err
	}
	for _, name := range []string{".cocoapods", "Pods", "Podfile.lock"} {
		if err := os.RemoveAll(filepath.Join(dir, name)); err != nil {
			return err
		}
	}
	return os.MkdirAll(filepath.Join(dir, ".cocoapods"), 0o700)
}

func (w *world) cocoaPodsProject(key string) error {
	dir, err := w.filesDir()
	if err != nil {
		return err
	}
	base := w.url
	machine := "127.0.0.1"
	if runtime.GOOS == "darwin" {
		base = strings.Replace(base, "127.0.0.1", "host.docker.internal", 1)
		machine = "host.docker.internal"
	}
	source := base + "/artifactory/api/pods/" + key
	text := fmt.Sprintf(`source %q
install! 'cocoapods', :integrate_targets => false
platform :ios, '12.0'
target 'Demo' do
  pod 'HelloPod', '1.0.0'
end
`, source)
	if err := os.WriteFile(filepath.Join(dir, "Podfile"), []byte(text), 0o600); err != nil {
		return err
	}
	if err := os.WriteFile(
		filepath.Join(dir, ".netrc"),
		[]byte(
			"machine "+machine+" login admin password "+testAdminPassword+"\n",
		),
		0o600,
	); err != nil {
		return err
	}
	return os.MkdirAll(filepath.Join(dir, ".cocoapods"), 0o700)
}

func (w *world) cocoaPodsArchive(name, version string) error {
	data, err := json.Marshal(map[string]any{
		"name": name, "version": version, "summary": "ArtifactWeave native CocoaPods proof",
		"homepage": "https://example.org/HelloPod", "license": map[string]string{"type": "MIT"},
		"authors":      map[string]string{"Example": "example@example.org"},
		"platforms":    map[string]string{"ios": "12.0"},
		"source":       map[string]string{"http": "https://example.invalid/unrewritten.tar.gz"},
		"source_files": "Sources/*.{h,m}",
	})
	if err != nil {
		return err
	}
	return w.packCocoaPodsArchive(name, version, name+".podspec.json", data)
}

func (w *world) staticRubyCocoaPodsArchive(name, version string) error {
	data := fmt.Sprintf(`Pod::Spec.new do |s|
  s.name = '%s'
  s.version = '%s'
  s.summary = 'ArtifactWeave native CocoaPods proof'
  s.homepage = 'https://example.org/HelloPod'
  s.license = { :type => 'MIT' }
  s.authors = { 'Example' => 'example@example.org' }
  s.source = {
    :http => 'https://example.invalid/unrewritten.tar.gz',
  }
  s.ios.deployment_target = '12.0'
  s.default_subspecs = 'Core'
  s.subspec 'Core' do |ss|
    ss.source_files = 'Sources/*.{h,m}'
  end
end`, name, version)
	return w.packCocoaPodsArchive(name, version, name+".podspec", []byte(data))
}

func (w *world) packCocoaPodsArchive(name, version, specName string, spec []byte) error {
	dir, err := w.filesDir()
	if err != nil {
		return err
	}
	var packed bytes.Buffer
	gz := gzip.NewWriter(&packed)
	tw := tar.NewWriter(gz)
	for file, body := range map[string][]byte{
		specName:             spec,
		"Sources/HelloPod.h": []byte("void hello_pod(void);\n"),
		"Sources/HelloPod.m": []byte("void hello_pod(void) {}\n"),
	} {
		if err := tw.WriteHeader(
			&tar.Header{Name: file, Mode: 0o644, Size: int64(len(body))},
		); err != nil {
			return err
		}
		if _, err := tw.Write(body); err != nil {
			return err
		}
	}
	if err := tw.Close(); err != nil {
		return err
	}
	if err := gz.Close(); err != nil {
		return err
	}
	return os.WriteFile(filepath.Join(dir, name+"-"+version+".tar.gz"), packed.Bytes(), 0o600)
}

func (w *world) runPod(args string) error {
	argv := w.clientArgs(args)
	if runtime.GOOS == "darwin" {
		for i := range argv {
			argv[i] = strings.ReplaceAll(argv[i], "127.0.0.1", "host.docker.internal")
		}
	}
	return w.runContainer(w.cocoapodsImage, []string{
		"--entrypoint", "pod", "-e", "COCOAPODS_HOME=/work/.cocoapods",
		"-e", "COCOAPODS_DISABLE_STATS=true",
	}, argv)
}
