package acceptance

import (
	"archive/tar"
	"archive/zip"
	"bytes"
	"compress/gzip"
	"context"
	"crypto/sha1" //nolint:gosec // npm's dist.shasum is SHA-1 by definition
	"crypto/sha512"
	"encoding/base64"
	"encoding/hex"
	"encoding/json"
	"encoding/pem"
	"fmt"
	"io"
	"io/fs"
	"net/http"
	"net/http/httptest"
	"net/http/httputil"
	"net/url"
	"os"
	"os/exec"
	"path"
	"path/filepath"
	"strings"
	"time"

	"github.com/cucumber/godog"
)

// The package clients P7 tests against (decision 0015): npm and Maven run
// in containers pinned by digest (clients/*.Dockerfile), which the job builds
// and names in ARTIFACTWEAVE_NODE_IMAGE and ARTIFACTWEAVE_MAVEN_IMAGE; Go is
// the runner's own toolchain. The containers use --network host so they
// reach the binary on 127.0.0.1, and the base URL that metadata carries stays
// valid; they run as the runner's user, so what they write into the
// scenario's directory can be removed afterwards.

// clientImages maps each containerised client to the variable naming its
// image.
var clientImages = map[string]string{
	"npm":   "ARTIFACTWEAVE_NODE_IMAGE",
	"Maven": "ARTIFACTWEAVE_MAVEN_IMAGE",
}

// imageWait bounds the wait for a client image the job builds in the
// background while the -cover binary compiles.
const imageWait = 10 * time.Minute

func (w *world) registerClients(sc *godog.ScenarioContext) {
	sc.Step(`^the (npm|Maven) client can reach the registry$`, w.clientReachable)
	sc.Step(`^the Go toolchain is available$`, w.goAvailable)
	sc.Step(`^I run npm "([^"]*)"$`, w.runNPM)
	sc.Step(`^I run mvn "([^"]*)"$`, w.runMaven)
	sc.Step(`^the registry is fronted by TLS$`, w.frontWithTLS)
	sc.Step(`^the Go proxy is "([^"]*)"$`, w.setGoProxy)
	sc.Step(`^I run go "([^"]*)"$`, w.runGo)
	sc.Step(`^I deploy the file "([^"]*)" to "([^"]*)" as admin$`, w.deployFile)
	sc.Step(`^I deploy the files under "([^"]*)" to "([^"]*)" as admin$`, w.deployTree)
	sc.Step(
		`^a Go module "([^"]*)" at "([^"]*)" laid out as a module proxy under "([^"]*)"$`,
		w.writeGoModule,
	)
	sc.Step(
		`^an npm package "([^"]*)" at "([^"]*)" with its tarball at "([^"]*)", under "([^"]*)"$`,
		w.writeNPMPackage,
	)
	sc.Step(`^the Maven project "([^"]*)" in "([^"]*)"$`, w.copyMavenProject)
	sc.Step(`^the file "([^"]*)" exists$`, w.fileExists)
	sc.Step(`^the file "([^"]*)" contains "([^"]*)"$`, w.fileContains)
}

// clientReachable skips the scenario unless the client's image is named and
// a Docker daemon on this host shares the binary's 127.0.0.1, which only
// Linux gives (dockerReachable); it then waits for the image the job may
// still be building.
func (w *world) clientReachable(client string) error {
	img := os.Getenv(clientImages[client])
	if img == "" || w.dockerReachable() != nil {
		return godog.ErrSkip
	}
	deadline := time.Now().Add(imageWait)
	for exec.Command("docker", "image", "inspect", img).Run() != nil {
		if time.Now().After(deadline) {
			return fmt.Errorf("image %s was not built within %s", img, imageWait)
		}
		time.Sleep(5 * time.Second)
	}
	return nil
}

// goAvailable skips the scenario when no go command is on PATH.
func (w *world) goAvailable() error {
	if _, err := exec.LookPath("go"); err != nil {
		return godog.ErrSkip
	}
	return nil
}

// clientArgs expands a client's arguments and splits them on whitespace;
// {sp} puts a space inside one argument, as for jf.
func (w *world) clientArgs(args string) []string {
	argv := strings.Fields(w.expand(args))
	for i := range argv {
		argv[i] = strings.ReplaceAll(argv[i], "{sp}", " ")
	}
	return argv
}

// runContainer runs a client image against the scenario's files, mounted at
// /work, with /work as its home.
func (w *world) runContainer(image string, flags, argv []string) error {
	dir, err := w.filesDir()
	if err != nil {
		return err
	}
	args := []string{
		"run", "--rm", "--network", "host",
		"--user", fmt.Sprintf("%d:%d", os.Getuid(), os.Getgid()),
		"-v", dir + ":/work", "-w", "/work", "-e", "HOME=/work",
	}
	args = append(append(append(args, flags...), image), argv...)
	return w.runClient(exec.Command("docker", args...))
}

// runNPM runs npm in the pinned node image, with its cache in the scenario's
// files and its network side trips (audit, funding, update checks) off.
func (w *world) runNPM(args string) error {
	return w.runContainer(os.Getenv(clientImages["npm"]), []string{
		"--entrypoint", "npm",
		"-e", "npm_config_cache=/work/.npm-cache",
		"-e", "npm_config_update_notifier=false",
		"-e", "npm_config_audit=false",
		"-e", "npm_config_fund=false",
	}, w.clientArgs(args))
}

// runMaven runs mvn in the pinned Maven image. ARTIFACTWEAVE_MAVEN_REPO, the
// job's cached ~/.m2/repository, is mounted at /m2 for {m2}; user.home is
// set because the runner's user has no entry in the image's passwd.
func (w *world) runMaven(args string) error {
	repo := os.Getenv("ARTIFACTWEAVE_MAVEN_REPO")
	if repo == "" {
		home, err := os.UserHomeDir()
		if err != nil {
			return err
		}
		repo = filepath.Join(home, ".m2", "repository")
	}
	if err := os.MkdirAll(repo, 0o750); err != nil {
		return err
	}
	argv := w.clientArgs(strings.ReplaceAll(args, "{m2}", "/m2"))
	return w.runContainer(os.Getenv(clientImages["Maven"]), []string{
		"--entrypoint", "mvn",
		"-v", repo + ":/m2",
		"-e", "MAVEN_OPTS=-Duser.home=/work",
	}, append([]string{"-B", "--no-transfer-progress"}, argv...))
}

// frontWithTLS puts a TLS-terminating reverse proxy in front of the binary,
// as a deployment's load balancer does, and names it {tlsauthurl} with the
// seeded admin's credentials. The go command sends credentials only over
// HTTPS (cmd/go/internal/web refuses them for an http:// proxy), and the
// binary serves plain HTTP. go trusts the front through SSL_CERT_FILE, which
// macOS and Windows honour from Go 1.27.
func (w *world) frontWithTLS() error {
	target, err := url.Parse(w.url)
	if err != nil {
		return err
	}
	front := httptest.NewTLSServer(httputil.NewSingleHostReverseProxy(target))
	w.t.Cleanup(front.Close)
	cert := pem.EncodeToMemory(&pem.Block{Type: "CERTIFICATE", Bytes: front.Certificate().Raw})
	if err := w.writeFile("tls-front.pem", &godog.DocString{Content: string(cert)}); err != nil {
		return err
	}
	w.tlsCA = filepath.Join(w.dir, "tls-front.pem")
	if w.vars == nil {
		w.vars = map[string]string{}
	}
	w.vars["tlsauthurl"] = "https://admin:" + testAdminPassword + "@" +
		strings.TrimPrefix(front.URL, "https://") + "/artifactory"
	return nil
}

func (w *world) setGoProxy(proxy string) error {
	w.goProxy = w.expand(proxy)
	return nil
}

// runGo runs the runner's go command against the scenario's proxy, with
// every cache under the scenario's files and nothing reaching the public
// checksum database or a newer toolchain.
func (w *world) runGo(args string) error {
	dir, err := w.filesDir()
	if err != nil {
		return err
	}
	cache := filepath.Join(dir, ".go")
	cmd := exec.Command("go", w.clientArgs(args)...)
	cmd.Dir = dir
	cmd.Env = append(os.Environ(),
		"GOPROXY="+w.goProxy,
		"GONOSUMDB=*",
		"GOPRIVATE=",
		"GONOPROXY=",
		"GOTOOLCHAIN=local",
		"GOFLAGS=-modcacherw",
		"GOENV=off",
		"GOWORK=off",
		"GO111MODULE=on",
		"GOPATH="+filepath.Join(cache, "path"),
		"GOMODCACHE="+filepath.Join(cache, "mod"),
		"GOCACHE="+filepath.Join(cache, "build"),
	)
	if w.tlsCA != "" {
		cmd.Env = append(cmd.Env, "SSL_CERT_FILE="+w.tlsCA)
	}
	return w.runClient(cmd)
}

// deployFile PUTs one of the scenario's files to an item path as admin.
func (w *world) deployFile(name, target string) error {
	dir, err := w.filesDir()
	if err != nil {
		return err
	}
	body, err := os.ReadFile(filepath.Join(dir, name))
	if err != nil {
		return err
	}
	req, err := http.NewRequestWithContext(context.Background(), http.MethodPut,
		w.url+w.expand(target), bytes.NewReader(body))
	if err != nil {
		return err
	}
	req.Header.Set("Content-Type", "application/octet-stream")
	req.SetBasicAuth("admin", testAdminPassword)
	resp, err := http.DefaultClient.Do(req)
	if err != nil {
		return err
	}
	defer func() { _ = resp.Body.Close() }()
	w.status, w.header = resp.StatusCode, resp.Header
	w.body, err = io.ReadAll(resp.Body)
	return err
}

// deployTree deploys every file under a scenario directory to the same
// relative path under target, stopping at the first that is not created.
func (w *world) deployTree(root, target string) error {
	dir, err := w.filesDir()
	if err != nil {
		return err
	}
	base := filepath.Join(dir, root)
	return filepath.WalkDir(base, func(p string, d fs.DirEntry, err error) error {
		if err != nil || d.IsDir() {
			return err
		}
		rel, err := filepath.Rel(base, p)
		if err != nil {
			return err
		}
		name := filepath.ToSlash(filepath.Join(root, rel))
		if err := w.deployFile(name, path.Join(target, filepath.ToSlash(rel))); err != nil {
			return err
		}
		if w.status != http.StatusCreated {
			return fmt.Errorf("deploy %s: status %d: %s", name, w.status, w.body)
		}
		return nil
	})
}

// writeGoModule writes a module the way a GOPROXY serves it, under root:
// <module>/@v/list, and the version's .info, .mod and .zip. The zip holds
// go.mod and one source file under <module>@<version>/, as go requires.
func (w *world) writeGoModule(module, version, root string) error {
	gomod := "module " + module + "\n\ngo 1.22\n"
	var buf bytes.Buffer
	zw := zip.NewWriter(&buf)
	for name, content := range map[string]string{
		"go.mod": gomod,
		"lib.go": "package lib\n\n// Greeting is what the acceptance module exports.\nconst Greeting = \"hello\"\n",
	} {
		f, err := zw.Create(module + "@" + version + "/" + name)
		if err != nil {
			return err
		}
		if _, err := io.WriteString(f, content); err != nil {
			return err
		}
	}
	if err := zw.Close(); err != nil {
		return err
	}
	at := root + "/" + module + "/@v/"
	for name, content := range map[string]string{
		"list":            version + "\n",
		version + ".info": `{"Version":"` + version + `","Time":"2026-10-16T00:00:00Z"}`,
		version + ".mod":  gomod,
		version + ".zip":  buf.String(),
	} {
		if err := w.writeFile(at+name, &godog.DocString{Content: content}); err != nil {
			return err
		}
	}
	return nil
}

// writeNPMPackage writes a package's tarball and the packument a registry
// would serve for it, under root as <name>.json and <name>-<version>.tgz.
// The packument's dist.tarball is the binary's URL for tarballPath, with
// the shasum and sha512 integrity npm checks.
func (w *world) writeNPMPackage(name, version, tarballPath, root string) error {
	manifest := map[string]any{"name": name, "version": version, "main": "index.js"}
	pkgJSON, err := json.MarshalIndent(manifest, "", "  ")
	if err != nil {
		return err
	}
	var buf bytes.Buffer
	gz := gzip.NewWriter(&buf)
	tw := tar.NewWriter(gz)
	for file, content := range map[string]string{
		"package/package.json": string(pkgJSON) + "\n",
		"package/index.js":     "module.exports = 'hello from artifactweave';\n",
	} {
		if err := tw.WriteHeader(&tar.Header{
			Name: file, Mode: 0o644, Size: int64(len(content)), Typeflag: tar.TypeReg,
		}); err != nil {
			return err
		}
		if _, err := io.WriteString(tw, content); err != nil {
			return err
		}
	}
	if err := tw.Close(); err != nil {
		return err
	}
	if err := gz.Close(); err != nil {
		return err
	}
	tgz := buf.Bytes()
	sha1sum := sha1.Sum(tgz) //nolint:gosec // npm's dist.shasum is SHA-1 by definition
	sha512sum := sha512.Sum512(tgz)
	version1 := map[string]any{
		"name": name, "version": version, "main": "index.js",
		"dist": map[string]any{
			"tarball":   w.url + w.expand(tarballPath),
			"shasum":    hex.EncodeToString(sha1sum[:]),
			"integrity": "sha512-" + base64.StdEncoding.EncodeToString(sha512sum[:]),
		},
	}
	packument, err := json.Marshal(map[string]any{
		"name":      name,
		"dist-tags": map[string]string{"latest": version},
		"versions":  map[string]any{version: version1},
	})
	if err != nil {
		return err
	}
	if err := w.writeFile(
		root+"/"+name+".json",
		&godog.DocString{Content: string(packument)},
	); err != nil {
		return err
	}
	return w.writeFile(root+"/"+name+"-"+version+".tgz", &godog.DocString{Content: string(tgz)})
}

// copyMavenProject copies a project from testdata/maven into the scenario's
// files, so a build writes its target/ there and never into the tree.
func (w *world) copyMavenProject(project, dest string) error {
	src := filepath.Join("testdata", "maven", project)
	return filepath.WalkDir(src, func(p string, d fs.DirEntry, err error) error {
		if err != nil || d.IsDir() {
			return err
		}
		rel, err := filepath.Rel(src, p)
		if err != nil {
			return err
		}
		content, err := os.ReadFile(p)
		if err != nil {
			return err
		}
		return w.writeFile(filepath.Join(dest, rel), &godog.DocString{Content: string(content)})
	})
}

func (w *world) fileExists(name string) error {
	dir, err := w.filesDir()
	if err != nil {
		return err
	}
	if _, err := os.Stat(filepath.Join(dir, name)); err != nil {
		return fmt.Errorf("%s: %w", name, err)
	}
	return nil
}

func (w *world) fileContains(name, want string) error {
	dir, err := w.filesDir()
	if err != nil {
		return err
	}
	content, err := os.ReadFile(filepath.Join(dir, name))
	if err != nil {
		return err
	}
	if !strings.Contains(string(content), want) {
		return fmt.Errorf("%s does not contain %q:\n%s", name, want, content)
	}
	return nil
}
