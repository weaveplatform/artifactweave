package acceptance

import (
	"archive/tar"
	"bytes"
	"compress/gzip"
	"crypto/tls"
	"encoding/pem"
	"fmt"
	"net/http"
	"os"
	"path/filepath"
	"strings"

	"github.com/cucumber/godog"

	"github.com/weaveplatform/artifactweave/internal/fakeupstream"
)

func (w *world) registerBower(sc *godog.ScenarioContext) {
	sc.Step(`^a Bower client project for repository "([^"]*)"$`, w.bowerProject)
	sc.Step(
		`^a Bower package "([^"]*)" version "([^"]*)" with marker "([^"]*)" under "([^"]*)"$`,
		w.bowerPackage,
	)
	sc.Step(
		`^a Bower upstream serves package "([^"]*)" version "([^"]*)" with marker "([^"]*)" for repository "([^"]*)"$`,
		w.bowerUpstream,
	)
	sc.Step(
		`^the Bower upstream adds version "([^"]*)" with marker "([^"]*)" for repository "([^"]*)"$`,
		w.bowerUpstreamAdds,
	)
	sc.Step(
		`^the Bower upstream redirects version "([^"]*)" for repository "([^"]*)"$`,
		w.bowerRedirect,
	)
	sc.Step(
		`^the Bower upstream corrupts version "([^"]*)" for repository "([^"]*)"$`,
		w.bowerCorrupt,
	)
	sc.Step(
		`^the Bower upstream uses a plain tag for version "([^"]*)" in repository "([^"]*)"$`,
		w.bowerPlainTag,
	)
	sc.Step(
		`^a (public|private) Git-backed Bower upstream serves package "([^"]*)" version "([^"]*)" with marker "([^"]*)"$`,
		w.bowerGitUpstream,
	)
	sc.Step(`^I run bower "([^"]*)"$`, w.runBower)
	sc.Step(`^I clear the Bower client cache and installation$`, w.clearBower)
}

func (w *world) bowerProject(key string) error {
	dir, err := w.filesDir()
	if err != nil {
		return err
	}
	base := w.url
	if os.Getenv("ARTIFACTWEAVE_BOWER_DESKTOP") == "1" {
		base = strings.Replace(base, "127.0.0.1", "host.docker.internal", 1)
	}
	url := "http://admin:" + testAdminPassword + "@" + strings.TrimPrefix(
		base,
		"http://",
	) + "/artifactory/api/bower/" + key
	config := fmt.Sprintf(
		`{"registry":{"search":[%q],"register":%q},"resolvers":["bower-art-resolver"],"interactive":false,"analytics":false}`+"\n",
		url,
		url,
	)
	return os.WriteFile(filepath.Join(dir, ".bowerrc"), []byte(config), 0o600)
}

func (w *world) bowerPackage(name, version, marker, root string) error {
	dir, err := w.filesDir()
	if err != nil {
		return err
	}
	base := filepath.Join(dir, root, name, version)
	if err := os.MkdirAll(base, 0o700); err != nil {
		return err
	}
	manifest := fmt.Sprintf(`{"name":%q,"version":%q,"main":"index.js"}`+"\n", name, version)
	if err := os.WriteFile(filepath.Join(base, "bower.json"), []byte(manifest), 0o600); err != nil {
		return err
	}
	return os.WriteFile(
		filepath.Join(base, "index.js"),
		[]byte("module.exports = "+fmt.Sprintf("%q", marker)+";\n"),
		0o600,
	)
}

func (w *world) bowerUpstream(name, version, marker, key string) error {
	refs := strings.Repeat("0", 44) + " refs/tags/v" + version + "\n"
	if err := w.remoteUpstream("/bower/refs/"+key+"/"+name, refs); err != nil {
		return err
	}
	w.remote.Set(
		"/bower/packages",
		fakeupstream.Resource{
			Body: []byte(fmt.Sprintf(`[{"name":%q,"url":%q}]`, name, "art://"+key+"/"+name)),
		},
	)
	archive, err := bowerArchive(name, version, marker)
	if err != nil {
		return err
	}
	w.remote.Set(
		"/bower/binaries/"+key+"/"+name+".git/v"+version,
		fakeupstream.Resource{Body: archive},
	)
	return nil
}

func (w *world) bowerUpstreamAdds(version, marker, key string) error {
	name := "hello"
	refs := strings.Repeat("0", 44) + " refs/tags/v1.0.0\n" +
		strings.Repeat("1", 44) + " refs/tags/v" + version + "\n"
	w.remote.Set("/bower/refs/"+key+"/"+name, fakeupstream.Resource{Body: []byte(refs)})
	archive, err := bowerArchive(name, version, marker)
	if err != nil {
		return err
	}
	w.remote.Set(
		"/bower/binaries/"+key+"/"+name+".git/v"+version,
		fakeupstream.Resource{Body: archive},
	)
	return nil
}

func (w *world) bowerRedirect(version, key string) error {
	name := "hello"
	original := "/bower/binaries/" + key + "/" + name + ".git/v" + version
	archive, err := bowerArchive(name, version, "redirected")
	if err != nil {
		return err
	}
	target := "/bower/redirected/" + key + "/" + name + "/v" + version
	w.remote.Set(target, fakeupstream.Resource{Body: archive})
	w.remote.Set(original, fakeupstream.Resource{Redirect: target})
	return nil
}

func (w *world) bowerCorrupt(version, key string) error {
	w.remote.Set(
		"/bower/binaries/"+key+"/hello.git/v"+version,
		fakeupstream.Resource{Body: []byte("not a gzip tarball")},
	)
	return nil
}

func (w *world) bowerPlainTag(version, key string) error {
	name := "hello"
	w.remote.Set("/bower/refs/"+key+"/"+name, fakeupstream.Resource{
		Body: []byte(strings.Repeat("a", 44) + " refs/tags/" + version + "\n"),
	})
	archive, err := bowerArchive(name, version, "plain-tag")
	if err != nil {
		return err
	}
	w.remote.Set(
		"/bower/binaries/"+key+"/"+name+".git/"+version,
		fakeupstream.Resource{Body: archive},
	)
	w.remote.Delete("/bower/binaries/" + key + "/" + name + ".git/v" + version)
	return nil
}

func (w *world) bowerGitUpstream(kind, name, version, marker string) error {
	if kind == "private" {
		w.remote = fakeupstream.New(w.t, fakeupstream.Options{
			TLS:  &tls.Config{MinVersion: tls.VersionTLS12},
			Auth: fakeupstream.Auth{Username: "git-user", Password: "git-secret"},
		})
		w.closers = append(w.closers, w.remote.Close)
		caFile := filepath.Join(w.t.TempDir(), "private-git-ca.pem")
		ca := pem.EncodeToMemory(&pem.Block{Type: "CERTIFICATE", Bytes: w.remote.Certificate().Raw})
		if err := os.WriteFile(caFile, ca, 0o600); err != nil {
			return err
		}
		w.env = append(w.env, "SSL_CERT_FILE="+caFile)
		if w.vars == nil {
			w.vars = map[string]string{}
		}
		w.vars["upstream"] = w.remote.URL
	} else if err := w.remoteUpstream("/ready", "Git fixture"); err != nil {
		return err
	}
	gitURL := w.remote.URL + "/git/testorg/" + name + ".git"
	w.remote.Set(
		"/registry/packages/"+name,
		fakeupstream.Resource{Body: []byte(fmt.Sprintf(`{"name":%q,"url":%q}`, name, gitURL))},
	)
	w.remote.Set(
		"/registry/packages",
		fakeupstream.Resource{Body: []byte(fmt.Sprintf(`[{"name":%q,"url":%q}]`, name, gitURL))},
	)
	w.remote.Set(
		"/registry/packages/search/"+name,
		fakeupstream.Resource{Body: []byte(fmt.Sprintf(`[{"name":%q,"url":%q}]`, name, gitURL))},
	)
	ref := strings.Repeat("a", 40) + " refs/tags/v" + version + "\n"
	pack := fmt.Sprintf("%04x", len(ref)+4) + ref
	w.remote.Set("/git/testorg/"+name+".git/info/refs", fakeupstream.Resource{
		Body: []byte("001e# service=git-upload-pack\n0000" + pack + "0000"),
		Header: http.Header{
			"Content-Type": []string{"application/x-git-upload-pack-advertisement"},
		},
	})
	archive, err := bowerGitArchive(name, version, marker)
	if err != nil {
		return err
	}
	w.remote.Set(
		"/git/testorg/"+name+"/archive/refs/tags/v"+version+".tar.gz",
		fakeupstream.Resource{Body: archive},
	)
	return nil
}

func bowerArchive(name, version, marker string) ([]byte, error) {
	return makeBowerArchive(name, version, marker, false)
}

func bowerGitArchive(name, version, marker string) ([]byte, error) {
	return makeBowerArchive(name, version, marker, true)
}

func makeBowerArchive(name, version, marker string, git bool) ([]byte, error) {
	manifest := fmt.Sprintf(`{"name":%q,"version":%q,"main":"index.js"}`+"\n", name, version)
	if git {
		manifest = fmt.Sprintf(`{"name":%q,"main":"index.js"}`+"\n", name)
	}
	var out bytes.Buffer
	gz := gzip.NewWriter(&out)
	tw := tar.NewWriter(gz)
	for _, f := range []struct{ name, body string }{{"bower.json", manifest}, {"index.js", "module.exports = " + fmt.Sprintf("%q", marker) + ";\n"}} {
		if err := tw.WriteHeader(
			&tar.Header{
				Name: name + "-" + version + "/" + f.name,
				Mode: 0o644,
				Size: int64(len(f.body)),
			},
		); err != nil {
			return nil, err
		}
		if _, err := tw.Write([]byte(f.body)); err != nil {
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

func (w *world) runBower(args string) error {
	return w.runContainer(w.bowerImage, []string{
		"-e", "npm_config_cache=/work/.npm-cache",
		"-e", "BOWER_DISABLE_ANALYTICS=1",
	}, append([]string{"--allow-root"}, w.clientArgs(args)...))
}

func (w *world) clearBower() error {
	dir, err := w.filesDir()
	if err != nil {
		return err
	}
	for _, name := range []string{"bower_components", ".cache/bower", ".bower/cache", ".bower/registry"} {
		if err := os.RemoveAll(filepath.Join(dir, name)); err != nil {
			return err
		}
	}
	return nil
}
