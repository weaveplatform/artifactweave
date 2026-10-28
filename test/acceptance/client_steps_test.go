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
	"runtime"
	"strings"
	"time"

	"github.com/cucumber/godog"
)

// The package clients P7 and P8 test against (decision 0015): npm, Maven,
// Python and .NET run in containers pinned by digest (clients/*.Dockerfile),
// which the job builds and names in the ARTIFACTWEAVE_*_IMAGE variables; Go
// is the runner's own toolchain, and helm and terraform are pinned binaries.
// The containers use --network host so they reach the binary on 127.0.0.1,
// and the base URL that metadata carries stays valid; they run as the
// runner's user, so what they write into the scenario's directory can be
// removed afterwards.

// clientImages maps each containerised client to the variable naming its
// image. "npm" is Node 26's bundled npm 11, "Maven" the newest Maven 3
// and ".NET" the SDK's LTS line; "npm 12", "Maven 3.9" and ".NET 11" are the
// second runs, which a Scenario Outline selects by name. "Python" holds pip,
// twine, uv and poetry.
var clientImages = map[string]string{
	"Puppet":     "ARTIFACTWEAVE_PUPPET_IMAGE",
	"FrogML":     "ARTIFACTWEAVE_FROGML_IMAGE",
	"npm":        "ARTIFACTWEAVE_NODE_IMAGE",
	"npm 12":     "ARTIFACTWEAVE_NPM12_IMAGE",
	"Maven":      "ARTIFACTWEAVE_MAVEN_IMAGE",
	"Maven 3.9":  "ARTIFACTWEAVE_MAVEN39_IMAGE",
	"Python":     "ARTIFACTWEAVE_PYTHON_IMAGE",
	"LuaRocks":   "ARTIFACTWEAVE_LUAROCKS_IMAGE",
	"Hex":        "ARTIFACTWEAVE_HEX_IMAGE",
	"Bower":      "ARTIFACTWEAVE_BOWER_IMAGE",
	"Composer":   "ARTIFACTWEAVE_COMPOSER_IMAGE",
	"Composer 1": "ARTIFACTWEAVE_COMPOSER_IMAGE",
	"Ruby":       "ARTIFACTWEAVE_RUBY_IMAGE",
	"Alpine":     "ARTIFACTWEAVE_ALPINE_IMAGE",
	"Cargo":      "ARTIFACTWEAVE_CARGO_IMAGE",
	"Debian":     "ARTIFACTWEAVE_DEBIAN_IMAGE",
	"Fedora":     "ARTIFACTWEAVE_FEDORA_IMAGE",
	"Git LFS":    "ARTIFACTWEAVE_GITLFS_IMAGE",
	"Opkg":       "ARTIFACTWEAVE_OPKG_IMAGE",
	"P2":         "ARTIFACTWEAVE_P2_IMAGE",
	"Dart":       "ARTIFACTWEAVE_DART_IMAGE",
	"CocoaPods":  "ARTIFACTWEAVE_COCOAPODS_IMAGE",
	"Vagrant":    "ARTIFACTWEAVE_VAGRANT_IMAGE",
	"JetBrains":  "ARTIFACTWEAVE_JETBRAINS_IMAGE",
	"Conda":      "ARTIFACTWEAVE_CONDA_IMAGE",
	"VS Code":    "ARTIFACTWEAVE_VSCODE_IMAGE",
	"R":          "ARTIFACTWEAVE_R_IMAGE",
	"Ansible":    "ARTIFACTWEAVE_ANSIBLE_IMAGE",
	".NET":       "ARTIFACTWEAVE_DOTNET_IMAGE",
	".NET 11":    "ARTIFACTWEAVE_DOTNET11_IMAGE",
}

// clientBinaries maps each client the job installs as a pinned binary
// (.github/actions/registry-clients) to the variable naming it.
var clientBinaries = map[string]string{
	"helm":      "ARTIFACTWEAVE_HELM_BIN",
	"terraform": "ARTIFACTWEAVE_TERRAFORM_BIN",
}

// imageWait bounds the wait for a client image the job builds in the
// background while the -cover binary compiles.
const imageWait = 10 * time.Minute

func (w *world) registerClients(sc *godog.ScenarioContext) {
	sc.Step(
		`^the (npm|npm 12|Maven|Maven 3\.9|Python|LuaRocks|Bower|Composer|Composer 1|Ruby|Hex|Alpine|Cargo|Debian|Fedora|Git LFS|Opkg|P2|Puppet|Dart|CocoaPods|Vagrant|JetBrains|Conda|VS Code|R|Ansible|FrogML|\.NET|\.NET 11) client can reach the registry$`,
		w.clientReachable,
	)
	sc.Step(`^(helm|terraform) is available$`, w.binaryAvailable)
	sc.Step(`^I run (python|pip|twine|uv|poetry) "([^"]*)"$`, w.runPython)
	sc.Step(`^I run jf Python "([^"]*)"$`, w.runJFPython)
	sc.Step(`^I run dotnet "([^"]*)"$`, w.runDotnet)
	sc.Step(`^I run helm "([^"]*)"$`, w.runHelm)
	sc.Step(`^I run terraform "([^"]*)"$`, w.runTerraform)
	sc.Step(`^I run luarocks "([^"]*)"$`, w.runLuaRocks)
	sc.Step(`^I run mix "([^"]*)"$`, w.runMix)
	sc.Step(`^a Python wheel "([^"]*)" at "([^"]*)" in "([^"]*)"$`, w.writeWheel)
	sc.Step(`^a NuGet package "([^"]*)" at "([^"]*)" in "([^"]*)"$`, w.writeNupkg)
	sc.Step(`^a zip file "([^"]*)" of the files under "([^"]*)"$`, w.zipTree)
	sc.Step(`^the Go toolchain is available$`, w.goAvailable)
	sc.Step(`^I run npm "([^"]*)"$`, w.runNPM)
	sc.Step(`^I run npm "([^"]*)", answering its prompts with:$`, w.runNPMAnswering)
	sc.Step(`^(npm|gradle) is on the runner's PATH$`, w.onRunnerPath)
	sc.Step(`^jf mvn runs the Maven 3\.9 client's Maven$`, w.jfMavenFromImage)
	sc.Step(`^the lockfile pins "([^"]*)" to the tarball at "([^"]*)"$`, w.lockfilePins)
	sc.Step(`^I run mvn "([^"]*)"$`, w.runMaven)
	sc.Step(`^the registry is fronted by TLS$`, w.frontWithTLS)
	sc.Step(`^the Go proxy is "([^"]*)"$`, w.setGoProxy)
	sc.Step(`^I run go "([^"]*)"$`, w.runGo)
	sc.Step(`^I run go "([^"]*)" in "([^"]*)"$`, w.runGoIn)
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

// clientReachable skips optional local scenarios without a named image or
// reachable Docker daemon. Required CI scenarios fail visibly instead. It
// waits for the image the job may still be building.
func (w *world) clientReachable(client string) error {
	img := os.Getenv(clientImages[client])
	if img == "" {
		if dockerRequired() {
			return fmt.Errorf("%s client image is not configured", client)
		}
		return godog.ErrSkip
	}
	desktopAlpine := client == "Alpine" && runtime.GOOS == "darwin" &&
		os.Getenv("ARTIFACTWEAVE_ALPINE_DESKTOP") == "1"
	desktopCargo := client == "Cargo" && runtime.GOOS == "darwin" &&
		os.Getenv("ARTIFACTWEAVE_CARGO_DESKTOP") == "1"
	desktopBower := client == "Bower" && runtime.GOOS == "darwin" &&
		os.Getenv("ARTIFACTWEAVE_BOWER_DESKTOP") == "1"
	desktopDebian := client == "Debian" && runtime.GOOS == "darwin" &&
		os.Getenv("ARTIFACTWEAVE_DEBIAN_DESKTOP") == "1"
	desktopRPM := client == "Fedora" && runtime.GOOS == "darwin" &&
		os.Getenv("ARTIFACTWEAVE_RPM_DESKTOP") == "1"
	desktopOpkg := client == "Opkg" && runtime.GOOS == "darwin" &&
		os.Getenv("ARTIFACTWEAVE_OPKG_DESKTOP") == "1"
	desktopRuby := client == "Ruby" && runtime.GOOS == "darwin" &&
		os.Getenv("ARTIFACTWEAVE_RUBY_DESKTOP") == "1"
	desktopDart := client == "Dart" && runtime.GOOS == "darwin" &&
		os.Getenv("ARTIFACTWEAVE_DART_DESKTOP") == "1"
	desktopPods := client == "CocoaPods" && runtime.GOOS == "darwin" &&
		os.Getenv("ARTIFACTWEAVE_COCOAPODS_DESKTOP") == "1"
	desktopVagrant := client == "Vagrant" && runtime.GOOS == "darwin" &&
		os.Getenv("ARTIFACTWEAVE_VAGRANT_DESKTOP") == "1"
	desktopP2 := client == "P2" && runtime.GOOS == "darwin" &&
		os.Getenv("ARTIFACTWEAVE_P2_DESKTOP") == "1"
	desktopConda := client == "Conda" && runtime.GOOS == "darwin" &&
		os.Getenv("ARTIFACTWEAVE_CONDA_DESKTOP") == "1"
	desktopVSCode := client == "VS Code" && runtime.GOOS == "darwin" &&
		os.Getenv("ARTIFACTWEAVE_VSCODE_DESKTOP") == "1"
	desktopGitLFS := client == "Git LFS" && runtime.GOOS == "darwin" &&
		os.Getenv("ARTIFACTWEAVE_GITLFS_DESKTOP") == "1"
	desktopR := client == "R" && runtime.GOOS == "darwin" &&
		os.Getenv("ARTIFACTWEAVE_R_DESKTOP") == "1"
	desktopPuppet := client == "Puppet" && runtime.GOOS == "darwin" &&
		os.Getenv("ARTIFACTWEAVE_PUPPET_DESKTOP") == "1"
	desktopAnsible := client == "Ansible" && runtime.GOOS == "darwin" &&
		os.Getenv("ARTIFACTWEAVE_ANSIBLE_DESKTOP") == "1"
	desktopFrogML := client == "FrogML" && runtime.GOOS == "darwin" &&
		os.Getenv("ARTIFACTWEAVE_FROGML_DESKTOP") == "1"
	desktop := desktopFrogML || desktopAnsible || desktopAlpine || desktopBower || desktopCargo ||
		desktopDebian || desktopRPM || desktopOpkg || desktopRuby || desktopDart ||
		desktopPods || desktopP2 || desktopConda || desktopVSCode || desktopGitLFS ||
		desktopR || desktopVagrant || desktopPuppet
	if desktop {
		if err := dockerAnswers(); err != nil {
			if dockerRequired() {
				return fmt.Errorf("%s client requires Docker: %w", client, err)
			}
			return godog.ErrSkip
		}
	} else if err := w.dockerReachable(); err != nil {
		return err
	}
	switch {
	case strings.HasPrefix(client, "npm"):
		w.npmImage = img
	case strings.HasPrefix(client, "Maven"):
		w.mavenImage = img
	case client == "Python":
		w.pythonImage = img
	case client == "LuaRocks":
		w.luarocksImage = img
	case client == "Bower":
		w.bowerImage = img
	case client == "Composer" || client == "Composer 1":
		w.composerImage = img
	case client == "Ruby":
		w.rubyImage = img
	case client == "Hex":
		w.hexImage = img
	case client == "Alpine":
		w.alpineImage = img
	case client == "Cargo":
		w.cargoImage = img
	case client == "Debian":
		w.debianImage = img
	case client == "Fedora":
		w.fedoraImage = img
	case client == "Git LFS":
		w.gitLFSImage = img
	case client == "Opkg":
		w.opkgImage = img
	case client == "P2":
		w.p2Image = img
	case client == "Puppet":
		w.puppetImage = img
	case client == "Dart":
		w.dartImage = img
	case client == "CocoaPods":
		w.cocoapodsImage = img
	case client == "Vagrant":
		w.vagrantImage = img
	case client == "Conda":
		w.condaImage = img
	case client == "VS Code":
		w.vscodeImage = img
	case client == "FrogML":
		w.frogMLImage = img
	case client == "Ansible":
		w.ansibleImage = img
	case client == "R":
		w.rImage = img
	default:
		w.dotnetImage = img
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

// jfMavenFromImage gives jf mvn the Maven of the Maven 3.9 client image,
// copied out of it, as M2_HOME, which jf reads before it looks for mvn on
// PATH. The JFrog Maven extractor jf 2.126.0 downloads (2.43.9, the newest)
// cannot start on Maven 3.10, the runner's own: Maven fails with
// NoSuchMethodError on DefaultArtifactResolver's constructor before it
// builds anything. Maven is Java and scripts, so it runs on the runner's
// Java.
func (w *world) jfMavenFromImage() error {
	if err := w.clientReachable("Maven 3.9"); err != nil {
		return err
	}
	dir, err := w.filesDir()
	if err != nil {
		return err
	}
	out, err := exec.Command("docker", "create", w.mavenImage).Output()
	if err != nil {
		return fmt.Errorf("docker create %s: %w", w.mavenImage, err)
	}
	id := strings.TrimSpace(string(out))
	defer func() { _ = exec.Command("docker", "rm", id).Run() }()
	home := filepath.Join(dir, "maven-3.9")
	// MAVEN_HOME in the official maven images.
	cp := exec.Command("docker", "cp", id+":/usr/share/maven", home)
	if out, err := cp.CombinedOutput(); err != nil {
		return fmt.Errorf("docker cp: %w: %s", err, out)
	}
	w.jfEnv = append(w.jfEnv, "M2_HOME="+home)
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
	return w.runContainerWith(nil, image, flags, argv)
}

// runContainerWith is runContainer with stdin attached, for a client that
// prompts; nil attaches none.
func (w *world) runContainerWith(stdin io.Reader, image string, flags, argv []string) error {
	dir, err := w.filesDir()
	if err != nil {
		return err
	}
	args := []string{
		"run", "--rm", "--network", "host",
		"--user", fmt.Sprintf("%d:%d", os.Getuid(), os.Getgid()),
		"-v", dir + ":/work", "-w", "/work", "-e", "HOME=/work",
	}
	if stdin != nil {
		args = append(args, "-i")
	}
	args = append(append(append(args, flags...), image), argv...)
	cmd := exec.Command("docker", args...)
	cmd.Stdin = stdin
	return w.runClient(cmd)
}

// runNPM runs npm in the pinned node image, with its cache in the scenario's
// files and its network side trips (audit, funding, update checks) off.
func (w *world) runNPM(args string) error { return w.runNPMWith(nil, args) }

// runNPMWith is runNPM with stdin attached.
func (w *world) runNPMWith(stdin io.Reader, args string) error {
	return w.runContainerWith(stdin, w.npmImage, []string{
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
	return w.runContainer(w.mavenImage, []string{
		"--entrypoint", "mvn",
		"-v", repo + ":/m2",
		"-e", "MAVEN_OPTS=-Duser.home=/work",
	}, append([]string{"-B", "--no-transfer-progress"}, argv...))
}

// binaryAvailable skips the scenario unless the client's pinned binary is
// named, so the features pass locally without the downloads CI performs.
// helm and terraform run on the runner, so they need no Docker.
func (w *world) binaryAvailable(client string) error {
	if os.Getenv(clientBinaries[client]) == "" {
		return godog.ErrSkip
	}
	return nil
}

// runPython runs python, pip, twine, uv or poetry in the pinned Python image. HOME
// is the scenario's files, so pip.conf, .pypirc, uv.toml and poetry's
// config.toml come from there, and pip, finding the image's site-packages
// read-only, installs into them as a user. The null keyring stops twine and
// poetry asking a keyring for passwords, which the image has no backend to
// answer.
func (w *world) runPython(tool, args string) error {
	return w.runContainer(w.pythonImage, []string{
		"--entrypoint", tool,
		"-e", "PIP_DISABLE_PIP_VERSION_CHECK=1",
		"-e", "PIP_NO_INPUT=1",
		"-e", "POETRY_NO_INTERACTION=1",
		"-e", "POETRY_VIRTUALENVS_IN_PROJECT=true",
		"-e", "UV_PYTHON_DOWNLOADS=never",
		"-e", "PYTHON_KEYRING_BACKEND=keyring.backends.null.Keyring",
	}, w.clientArgs(args))
}

// runJFPython runs the pinned Linux JFrog binary beside the pinned Python
// tools, so jf's subprocesses use the same pip and twine as native scenarios.
// Its configuration and build records stay under the scenario's mounted files.
func (w *world) runJFPython(args string) error {
	if err := w.jfAvailable(); err != nil {
		return err
	}
	bin, err := filepath.Abs(os.Getenv("ARTIFACTWEAVE_JF_BIN"))
	if err != nil {
		return err
	}
	return w.runContainer(w.pythonImage, []string{
		"--entrypoint", "/usr/local/bin/jf",
		"-v", bin + ":/usr/local/bin/jf:ro",
		"-e", "JFROG_CLI_HOME_DIR=/work/.jfrog-cli",
		"-e", "JFROG_CLI_TEMP_DIR=/work/.jfrog-tmp",
		"-e", "JFROG_CLI_REPORT_USAGE=false",
		"-e", "JFROG_CLI_AVOID_NEW_VERSION_WARNING=true",
		"-e", "PIP_DISABLE_PIP_VERSION_CHECK=1",
		"-e", "PIP_NO_INPUT=1",
		"-e", "POETRY_NO_INTERACTION=1",
		"-e", "POETRY_VIRTUALENVS_IN_PROJECT=true",
		"-e", "UV_PYTHON_DOWNLOADS=never",
		"-e", "PYTHON_KEYRING_BACKEND=keyring.backends.null.Keyring",
		"-e", "CI=true",
	}, w.clientArgs(args))
}

// runDotnet runs the dotnet CLI in the pinned .NET SDK image. HOME is the
// scenario's files, so NuGet's user configuration and global packages
// folder are there too. NuGet writes nuget.org into a new user
// configuration, so a scenario's NuGet.Config clears the sources before it
// adds its own. Telemetry, the first-run banner, the workload update check
// and the HTTPS development certificate are off: each would reach the
// network or write outside the scenario.
func (w *world) runDotnet(args string) error {
	argv := w.clientArgs(args)
	flags := []string{
		"--entrypoint", "dotnet",
		"-e", "DOTNET_CLI_TELEMETRY_OPTOUT=1",
		"-e", "DOTNET_NOLOGO=1",
		"-e", "DOTNET_CLI_WORKLOAD_UPDATE_NOTIFY_DISABLE=1",
		"-e", "DOTNET_GENERATE_ASPNET_CERTIFICATE=false",
		"-e", "NUGET_XMLDOC_MODE=skip",
	}
	if len(argv) >= 2 && ((argv[0] == "package" && argv[1] == "search") ||
		(argv[0] == "nuget" && argv[1] == "push")) {
		// The pinned .NET 11 RC2 NativeAOT CLI prints successful search/push
		// results but hangs on exit (push reproduced on ARM64). Its managed
		// fallback preserves the real client request and exit-status assertion:
		// https://github.com/dotnet/core/blob/main/release-notes/11.0/preview/preview7/sdk.md
		flags = append(flags, "-e", "DOTNET_CLI_ENABLEAOT=false")
	}
	return w.runContainer(w.dotnetImage, flags, argv)
}

// runHelm runs the pinned helm in the scenario's files, with its
// configuration, cache and data under them. They are named outright because
// helm's defaults differ by OS (XDG directories on Linux, ~/Library on
// macOS), and the runner's own HELM_* variables are dropped.
func (w *world) runHelm(args string) error {
	dir, err := w.filesDir()
	if err != nil {
		return err
	}
	cmd := exec.Command(os.Getenv(clientBinaries["helm"]), w.clientArgs(args)...)
	cmd.Dir = dir
	cmd.Env = append(environWithout("HELM_"),
		"HOME="+dir,
		"HELM_CONFIG_HOME="+filepath.Join(dir, ".config", "helm"),
		"HELM_CACHE_HOME="+filepath.Join(dir, ".cache", "helm"),
		"HELM_DATA_HOME="+filepath.Join(dir, ".local", "share", "helm"),
	)
	return w.runClient(cmd)
}

// runTerraform runs the pinned terraform in the scenario's files with HOME
// there, so .terraformrc and .terraform.d/credentials.tfrc.json are the
// scenario's. The runner's TF_* variables are dropped (TF_CLI_ARGS alone
// would change every command); terraform never prompts, and skips its
// version check against HashiCorp.
func (w *world) runTerraform(args string) error {
	return w.runTerraformWithEnv(args, nil)
}

func (w *world) runTerraformWithEnv(args string, extra []string) error {
	dir, err := w.filesDir()
	if err != nil {
		return err
	}
	cmd := exec.Command(os.Getenv(clientBinaries["terraform"]), w.clientArgs(args)...)
	cmd.Dir = dir
	cmd.Env = append(environWithout("TF_"),
		"HOME="+dir,
		"TF_IN_AUTOMATION=1",
		"TF_INPUT=0",
		"CHECKPOINT_DISABLE=1",
	)
	if w.tlsCA != "" {
		cmd.Env = append(cmd.Env, "SSL_CERT_FILE="+w.tlsCA)
	}
	cmd.Env = append(cmd.Env, extra...)
	return w.runClient(cmd)
}

// environWithout is the test's environment less every variable whose name
// starts with prefix.
func environWithout(prefix string) []string {
	var env []string
	for _, kv := range os.Environ() {
		if !strings.HasPrefix(kv, prefix) {
			env = append(env, kv)
		}
	}
	return env
}

// frontWithTLS puts a TLS-terminating reverse proxy in front of the binary,
// as a deployment's load balancer does, and names it {tlsauthurl} with the
// seeded admin's credentials. The go command sends credentials only over
// HTTPS (cmd/go/internal/web refuses them for an http:// proxy), and the
// binary serves plain HTTP. go trusts the front through SSL_CERT_FILE, which
// macOS and Windows honour from Go 1.27.
func (w *world) frontWithTLS() error { return w.frontWithTLSBase(false) }

func (w *world) frontWithTLSBase(override bool) error {
	target, err := url.Parse(w.url)
	if err != nil {
		return err
	}
	proxy := httputil.NewSingleHostReverseProxy(target)
	if override {
		original := proxy.Director
		proxy.Director = func(r *http.Request) { original(r); r.Header.Set("X-JFrog-Override-Base-Url", "https://"+r.Host) }
	}
	front := httptest.NewTLSServer(proxy)
	w.t.Cleanup(front.Close)
	cert := pem.EncodeToMemory(&pem.Block{Type: "CERTIFICATE", Bytes: front.Certificate().Raw})
	if err := w.writeFile("tls-front.pem", &godog.DocString{Content: string(cert)}); err != nil {
		return err
	}
	w.tlsCA = filepath.Join(w.dir, "tls-front.pem")
	if w.vars == nil {
		w.vars = map[string]string{}
	}
	// {tlshost} lets a scenario put another user's credentials in the URL.
	w.vars["tlshost"] = strings.TrimPrefix(front.URL, "https://")
	w.vars["tlsauthurl"] = "https://admin:" + testAdminPassword + "@" +
		w.vars["tlshost"] + "/artifactory"
	return nil
}

func (w *world) setGoProxy(proxy string) error {
	w.goProxy = w.expand(proxy)
	return nil
}

// runGo runs the runner's go command against the scenario's proxy, with
// every cache under the scenario's files and nothing reaching the public
// checksum database or a newer toolchain.
func (w *world) runGo(args string) error { return w.runGoIn(args, "") }

// runGoIn runs the go command in a directory of the scenario's files, such
// as a consumer module's.
func (w *world) runGoIn(args, sub string) error {
	dir, err := w.filesDir()
	if err != nil {
		return err
	}
	cache := filepath.Join(dir, ".go")
	cmd := exec.Command("go", w.clientArgs(args)...)
	cmd.Dir = filepath.Join(dir, sub)
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
	return w.deployFileAs(name, target, "admin", testAdminPassword)
}

func (w *world) deployFileAs(name, target, user, password string) error {
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
	req.SetBasicAuth(user, password)
	for k, v := range w.next {
		req.Header.Set(k, v)
	}
	w.next = nil
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
