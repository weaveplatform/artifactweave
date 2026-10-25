package acceptance

import (
	"bytes"
	"context"
	"crypto/md5" //nolint:gosec // Bundler 1.x ETags use MD5 for compact metadata.
	"fmt"
	"net/url"
	"os"
	"path/filepath"
	"runtime"
	"strings"
	"time"

	"github.com/cucumber/godog"

	"github.com/weaveplatform/artifactweave/internal/fakeupstream"
	gemfmt "github.com/weaveplatform/artifactweave/internal/format/gems"
)

func (w *world) registerGems(sc *godog.ScenarioContext) {
	sc.Step(`^the pinned rake RubyGem is available$`, w.copyPinnedGem)
	sc.Step(`^a RubyGems upstream serves the pinned rake gem$`, w.gemsUpstream)
	sc.Step(`^the RubyGems source is "([^"]*)"$`, w.gemsSource)
	sc.Step(`^I run Ruby "(gem|bundle)" "([^"]*)"$`, w.runRuby)
}

func (w *world) copyPinnedGem() error {
	dir, err := w.filesDir()
	if err != nil {
		return err
	}
	b, err := os.ReadFile(
		filepath.Join("..", "..", "internal", "format", "gems", "testdata", "rake-12.3.1.gem"),
	)
	if err != nil {
		return err
	}
	return os.WriteFile(filepath.Join(dir, "rake-12.3.1.gem"), b, 0o600)
}

func (w *world) gemsUpstream() error {
	if err := w.copyPinnedGem(); err != nil {
		return err
	}
	dir, err := w.filesDir()
	if err != nil {
		return err
	}
	b, err := os.ReadFile(filepath.Join(dir, "rake-12.3.1.gem"))
	if err != nil {
		return err
	}
	p, err := gemfmt.Read(
		context.Background(),
		bytes.NewReader(b),
		int64(len(b)),
		"gems/rake-12.3.1.gem",
	)
	if err != nil {
		return err
	}
	w.remote = fakeupstream.New(w.t, fakeupstream.Options{})
	w.closers = append(w.closers, w.remote.Close)
	packages := []gemfmt.Package{p}
	set := func(path string, body []byte) {
		sum := md5.Sum(body)
		w.remote.Set("/"+path, fakeupstream.Resource{Body: body, ETag: fmt.Sprintf("\"%x\"", sum)})
	}
	set("versions", gemfmt.CompactVersions(packages))
	set("names", gemfmt.CompactNames(packages))
	set("info/rake", gemfmt.CompactInfo("rake", packages))
	set("gems/rake-12.3.1.gem", b)
	for _, name := range []string{"specs.4.8.gz", "latest_specs.4.8.gz", "prerelease_specs.4.8.gz"} {
		body, err := gemfmt.Specs(
			packages,
			name == "latest_specs.4.8.gz",
			name == "prerelease_specs.4.8.gz",
		)
		if err != nil {
			return err
		}
		set(name, body)
	}
	quick, err := gemfmt.QuickSpec(p, time.Unix(0, 0))
	if err != nil {
		return err
	}
	set("quick/Marshal.4.8/rake-12.3.1.gemspec.rz", quick)
	if w.vars == nil {
		w.vars = map[string]string{}
	}
	w.vars["upstream"] = w.remote.URL
	return nil
}

func (w *world) gemsSource(key string) error {
	dir, err := w.filesDir()
	if err != nil {
		return err
	}
	base := w.url
	if runtime.GOOS == "darwin" && os.Getenv("ARTIFACTWEAVE_RUBY_DESKTOP") == "1" {
		base = strings.Replace(base, "127.0.0.1", "host.docker.internal", 1)
	}
	u, err := url.Parse(base)
	if err != nil {
		return err
	}
	u.User = url.UserPassword("admin", testAdminPassword)
	source := u.String() + "/artifactory/api/gems/" + key + "/"
	if w.vars == nil {
		w.vars = map[string]string{}
	}
	w.vars["gemsource"] = source
	return os.WriteFile(
		filepath.Join(dir, "Gemfile"),
		[]byte("source '"+source+"'\ngem 'rake', '12.3.1'\n"),
		0o600,
	)
}

func (w *world) runRuby(client, args string) error {
	flags := []string{
		"--entrypoint",
		client,
		"-e",
		"GEM_HOME=/work/gem-home",
		"-e",
		"GEM_PATH=/work/gem-home",
		"-e",
		"BUNDLE_PATH=/work/bundle",
		"-e",
		"BUNDLE_USER_CACHE=/work/cache",
		"-e",
		"HTTP_PROXY=",
		"-e",
		"HTTPS_PROXY=",
	}
	return w.runContainer(w.rubyImage, flags, w.clientArgs(args))
}
