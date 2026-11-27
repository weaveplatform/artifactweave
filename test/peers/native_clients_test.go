package peers_test

import (
	"archive/zip"
	"bytes"
	"context"
	"crypto/rand"
	"encoding/base64"
	"encoding/json"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"runtime"
	"strings"
	"testing"
	"time"

	"github.com/weaveplatform/artifactweave/internal/platform/testkit/repopath"
	"github.com/weaveplatform/artifactweave/pkg/containertest"
	"github.com/weaveplatform/artifactweave/test/peers"
)

// Host clients are an explicit supplementary local run. Required Linux CI
// always uses the digest-pinned images and the checksum-pinned jf action.
type peerNativeClients struct {
	t                   *testing.T
	host                bool
	npm, maven, jf      string
	root, tools, global string
	secrets             []string
}

func newPeerNativeClients(t *testing.T) *peerNativeClients {
	t.Helper()
	required := os.Getenv("ARTIFACTWEAVE_REQUIRE_DOCKER") == "1"
	host := os.Getenv("ARTIFACTWEAVE_PEER_NATIVE_HOST") == "1"
	if required && host {
		t.Fatal("required peer native qualification cannot use host clients")
	}
	if !host && runtime.GOOS != "linux" {
		if required {
			t.Fatal("required peer native qualification needs Linux host networking")
		}
		t.Skip("pinned peer clients require Linux; explicit supplementary host mode is ARTIFACTWEAVE_PEER_NATIVE_HOST=1")
	}
	c := &peerNativeClients{t: t, host: host, root: t.TempDir(), jf: os.Getenv("ARTIFACTWEAVE_JF_BIN")}
	if host {
		var err error
		if c.npm, err = exec.LookPath("npm"); err != nil {
			t.Fatal(err)
		}
		if c.maven, err = exec.LookPath("mvn"); err != nil {
			t.Fatal(err)
		}
	} else {
		c.npm, c.maven = os.Getenv("ARTIFACTWEAVE_NODE_IMAGE"), os.Getenv("ARTIFACTWEAVE_MAVEN_IMAGE")
		if !required && c.npm == "" && c.maven == "" && c.jf == "" {
			t.Skip("peer native clients are not configured")
		}
		if c.npm == "" || c.maven == "" {
			t.Fatal("peer native qualification requires both Node and Maven images")
		}
		if err := containertest.CheckLinux(t.Context()); err != nil {
			t.Fatal(err)
		}
		for _, image := range []string{c.npm, c.maven} {
			c.command("inspect pinned client image", "docker", []string{"image", "inspect", image}, c.root, nil, true)
		}
	}
	if c.jf == "" {
		t.Fatal("peer native qualification requires ARTIFACTWEAVE_JF_BIN from the pinned JFrog CLI action")
	}
	var err error
	if c.jf, err = filepath.Abs(c.jf); err != nil {
		t.Fatal(err)
	}
	cache := os.Getenv("ARTIFACTWEAVE_MAVEN_TOOLS")
	if cache == "" {
		t.Fatal("peer Maven tools are not prepared: set ARTIFACTWEAVE_MAVEN_TOOLS")
	}
	c.tools = filepath.Join(c.root, "tools")
	c.command("seed checksum-pinned Maven tools", "python3", []string{
		repopath.Path(t, "test/acceptance/clients/maven-tools.py"), "seed", "--cache", cache, "--repository", c.tools,
	}, c.root, nil, true)
	settings := repopath.Read(t, "test/acceptance/clients/maven-tools-settings.xml")
	c.global = filepath.Join(c.root, "maven-tools-settings.xml")
	nativeWrite(t, c.global, settings)
	// This tail supplies tools only. Product files always go into a new head.
	if err := filepath.WalkDir(c.tools, func(path string, entry os.DirEntry, err error) error {
		if err != nil {
			return err
		}
		if entry.IsDir() {
			return nil
		}
		return os.Chmod(path, 0o444)
	}); err != nil {
		t.Fatal(err)
	}
	for _, tool := range []string{"npm", "maven", "jf"} {
		dir := c.work()
		arg := "--version"
		if tool == "maven" {
			arg = "-version"
		}
		out, _ := c.run(tool, dir, nil, []string{arg}, true)
		t.Logf("peer native %s identity (host=%t): %s", tool, host, out)
	}
	return c
}

func nativeWrite(t *testing.T, path string, data []byte) {
	t.Helper()
	if err := os.MkdirAll(filepath.Dir(path), 0o700); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(path, data, 0o600); err != nil {
		t.Fatal(err)
	}
}

func (c *peerNativeClients) work() string {
	c.t.Helper()
	dir, err := os.MkdirTemp(c.root, "client-")
	if err != nil {
		c.t.Fatal(err)
	}
	return dir
}

func (c *peerNativeClients) command(label, binary string, argv []string, dir string, env []string, success bool) ([]byte, error) {
	c.t.Helper()
	ctx, cancel := context.WithTimeout(c.t.Context(), 2*time.Minute)
	defer cancel()
	cmd := exec.CommandContext(ctx, binary, argv...)
	cmd.Dir = dir
	// Explicit host mode must not inherit a developer's registry, Maven
	// arguments or JFrog profile/token overrides. PATH still locates tools.
	for _, value := range os.Environ() {
		name := strings.ToUpper(strings.SplitN(value, "=", 2)[0])
		if strings.HasPrefix(name, "NPM_CONFIG_") || strings.HasPrefix(name, "JFROG_CLI_") || strings.HasPrefix(name, "MAVEN_") ||
			name == "JAVA_TOOL_OPTIONS" || name == "JDK_JAVA_OPTIONS" || name == "_JAVA_OPTIONS" {
			continue
		}
		cmd.Env = append(cmd.Env, value)
	}
	cmd.Env = append(cmd.Env, env...)
	out, err := cmd.CombinedOutput()
	clean := string(out)
	for _, secret := range c.secrets {
		clean = strings.ReplaceAll(clean, secret, "[fixture credential]")
	}
	if ctx.Err() != nil || (err == nil) != success {
		c.t.Fatalf("%s success=%t, want %t: %v\n%s", label, err == nil, success, err, clean)
	}
	if err != nil {
		if _, ok := err.(*exec.ExitError); !ok {
			c.t.Fatalf("%s could not run: %v", label, err)
		}
	}
	c.t.Logf("peer native %s success=%t\n%s", label, err == nil, clean)
	return []byte(clean), err
}

func (c *peerNativeClients) run(tool, dir string, in *peers.Instance, argv []string, success bool) ([]byte, error) {
	c.t.Helper()
	var flags, env []string
	label, containerName := tool, ""
	bin := map[string]string{"npm": c.npm, "maven": c.maven, "jf": c.jf}[tool]
	home := filepath.Join(dir, "home")
	if err := os.MkdirAll(home, 0o700); err != nil {
		c.t.Fatal(err)
	}
	env = []string{"HOME=" + home, "CI=true"}
	work, tools, global := dir, c.tools, c.global
	if !c.host && tool != "jf" {
		containerName = "artifactweave-peer-native-" + strings.ToLower(rand.Text())
		work, tools, global = "/work", "/tools", "/global-settings.xml"
		flags = []string{
			"run", "--rm", "--name", containerName, "--pull", "never", "--network", "host",
			"--user", fmt.Sprintf("%d:%d", os.Getuid(), os.Getgid()), "-v", dir + ":/work", "-w", "/work", "-e", "HOME=/work/home",
		}
	}
	if in != nil {
		label += " on " + in.Name
		c.secrets = append(c.secrets, in.AdminPassword, base64.StdEncoding.EncodeToString([]byte(peers.AdminUser+":"+in.AdminPassword)))
	}
	switch tool {
	case "npm":
		if in != nil {
			registry := in.URL + "/artifactory/api/npm/" + argv[0] + "/"
			auth := base64.StdEncoding.EncodeToString([]byte(peers.AdminUser + ":" + in.AdminPassword))
			nativeWrite(c.t, filepath.Join(dir, ".npmrc"), []byte("registry="+registry+"\n//"+strings.TrimPrefix(registry, "http://")+":_auth="+auth+"\n"))
			argv = argv[1:]
		}
		npmEnv := []string{
			"npm_config_userconfig=" + work + "/.npmrc", "npm_config_cache=" + work + "/npm-cache",
			"npm_config_update_notifier=false", "npm_config_audit=false", "npm_config_fund=false",
		}
		env = append(env, npmEnv...)
		if !c.host {
			for _, value := range npmEnv {
				flags = append(flags, "-e", value)
			}
			flags = append(flags, "--entrypoint", "npm", bin)
		}
	case "maven":
		if in != nil {
			key := argv[0]
			argv = argv[1:]
			var servers strings.Builder
			for _, id := range []string{"aw", "central"} {
				fmt.Fprintf(&servers, `<server><id>%s</id><username>admin</username><password>%s</password></server>`, id, in.AdminPassword)
			}
			// dependency:get also adds Central independently of the consumer
			// POM. Its exact-ID mirror must therefore be the owned repository;
			// the global external:* block still refuses every public endpoint.
			nativeWrite(
				c.t,
				filepath.Join(dir, "settings.xml"),
				[]byte(
					`<settings><servers>`+servers.String()+`</servers><mirrors><mirror><id>aw</id><mirrorOf>central</mirrorOf><url>`+in.URL+`/artifactory/`+key+`</url></mirror></mirrors></settings>`,
				),
			)
			argv = append([]string{"-s", work + "/settings.xml"}, argv...)
		}
		argv = append([]string{
			"-B", "--no-transfer-progress", "--global-settings", global,
			"-Dmaven.repo.local=" + work + "/product-cache", "-Dmaven.repo.local.tail=" + tools,
		}, argv...)
		env = append(env, "MAVEN_OPTS=-Duser.home="+home, "MAVEN_CONFIG="+home)
		if !c.host {
			flags = append(flags, "-v", c.tools+":/tools:ro", "-v", c.global+":/global-settings.xml:ro",
				"-e", "MAVEN_OPTS=-Duser.home=/work/home", "--entrypoint", "mvn", bin)
		}
	case "jf":
		env = append(env, "JFROG_CLI_HOME_DIR="+home, "JFROG_CLI_TEMP_DIR="+filepath.Join(home, "tmp"),
			"JFROG_CLI_DEPENDENCIES_DIR="+filepath.Join(home, "dependencies"), "JFROG_CLI_REPORT_USAGE=false", "JFROG_CLI_AVOID_NEW_VERSION_WARNING=true")
		if in != nil {
			argv = append(argv, "--url", in.URL+"/artifactory", "--user", peers.AdminUser, "--password", in.AdminPassword)
		}
	}
	if len(flags) != 0 {
		completed := false
		defer func() {
			// Killing a docker client does not stop its container. A failed or
			// timed-out invocation must remove that owned container as well.
			if !completed {
				ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
				defer cancel()
				_ = exec.CommandContext(ctx, "docker", "rm", "--force", containerName).Run()
			}
		}()
		out, err := c.command(label, "docker", append(flags, argv...), dir, nil, success)
		completed = true
		return out, err
	}
	return c.command(label, bin, argv, dir, env, success)
}

type peerNativeArtifact struct{ name, marker string }

func (a peerNativeArtifact) path(key, format string) string {
	if format == "npm" {
		return key + "/" + a.name + "/-/" + a.name + "-1.0.0.tgz"
	}
	return key + "/org/acme/p13/" + a.name + "/1.0.0/" + a.name + "-1.0.0.jar"
}

func (a peerNativeArtifact) jar(t *testing.T) []byte {
	t.Helper()
	var body bytes.Buffer
	w := zip.NewWriter(&body)
	f, err := w.Create("origin.txt")
	if err != nil {
		t.Fatal(err)
	}
	if _, err = f.Write([]byte(a.marker)); err != nil {
		t.Fatal(err)
	}
	if err = w.Close(); err != nil {
		t.Fatal(err)
	}
	return body.Bytes()
}

func (c *peerNativeClients) publish(in *peers.Instance, key, format string, a peerNativeArtifact) {
	c.t.Helper()
	dir := c.work()
	if format == "npm" {
		pkg, err := json.Marshal(map[string]string{"name": a.name, "version": "1.0.0", "main": "index.js"})
		if err != nil {
			c.t.Fatal(err)
		}
		nativeWrite(c.t, filepath.Join(dir, "package.json"), pkg)
		nativeWrite(c.t, filepath.Join(dir, "index.js"), []byte(a.marker))
		c.run("npm", dir, in, []string{key, "publish", ".", "--ignore-scripts"}, true)
		return
	}
	nativeWrite(c.t, filepath.Join(dir, "artifact.jar"), a.jar(c.t))
	c.run("maven", dir, in, []string{
		key, "org.apache.maven.plugins:maven-deploy-plugin:3.1.3:deploy-file",
		"-Dfile=artifact.jar", "-DgroupId=org.acme.p13", "-DartifactId=" + a.name, "-Dversion=1.0.0", "-Dpackaging=jar",
		"-DrepositoryId=aw", "-Durl=" + in.URL + "/artifactory/" + key,
	}, true)
}

func (c *peerNativeClients) consume(all []*peers.Instance, key, format string, a peerNativeArtifact, present bool) {
	c.t.Helper()
	for _, in := range all {
		dir := c.work()
		var out []byte
		var path string
		if format == "npm" {
			out, _ = c.run("npm", dir, in, []string{key, "install", a.name + "@1.0.0", "--ignore-scripts", "--no-package-lock"}, present)
			path = filepath.Join(dir, "node_modules", a.name, "index.js")
		} else {
			out, _ = c.run("maven", dir, in, []string{
				key, "-C", "org.apache.maven.plugins:maven-dependency-plugin:3.8.1:get",
				"-Dartifact=org.acme.p13:" + a.name + ":1.0.0", "-Dtransitive=false", "-DremoteRepositories=aw::default::" + in.URL + "/artifactory/" + key,
			}, present)
			path = filepath.Join(dir, "product-cache", "org/acme/p13", a.name, "1.0.0", a.name+"-1.0.0.jar")
		}
		if !present {
			missing := strings.Contains(string(out), "E404") && strings.Contains(string(out), a.name)
			if format == "maven" {
				missing = mavenMissingProduct(string(out), "org.acme.p13:"+a.name+":jar:1.0.0", in.URL+"/artifactory/"+key)
			}
			if !missing {
				c.t.Fatalf("%s on %s failed without proving missing product: %s", format, in.Name, out)
			}
			continue
		}
		got, err := os.ReadFile(path)
		want := []byte(a.marker)
		if format == "maven" {
			want = a.jar(c.t)
		}
		if err != nil || !bytes.Equal(got, want) {
			c.t.Fatalf("fresh %s consumer on %s downloaded different bytes: %v", format, in.Name, err)
		}
	}
}

func (c *peerNativeClients) property(in *peers.Instance, path, value string) {
	c.run("jf", c.work(), in, []string{"rt", "sp", path, "source=" + value}, true)
}

func (c *peerNativeClients) remove(in *peers.Instance, path string) {
	c.run("jf", c.work(), in, []string{"rt", "del", path, "--quiet"}, true)
}

func mavenMissingProduct(output, coordinate, repository string) bool {
	if strings.Contains(output, "Could not find artifact "+coordinate+" in aw ("+repository+")") {
		return true
	}
	cached := coordinate + " was not found in " + repository + " during a previous attempt."
	for start := 0; start < len(output); {
		offset := strings.Index(output[start:], cached)
		if offset < 0 {
			return false
		}
		offset += start
		// Another Maven group can end in the expected group. Only a whole
		// coordinate token proves the requested product was absent.
		if offset == 0 || strings.ContainsAny(output[offset-1:offset], " \t\r\n") {
			return true
		}
		start = offset + len(cached)
	}
	return false
}
