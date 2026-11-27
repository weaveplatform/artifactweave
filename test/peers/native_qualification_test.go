package peers_test

import (
	"archive/tar"
	"bytes"
	"encoding/base64"
	"fmt"
	"net/http"
	"os"
	"os/exec"
	"path/filepath"
	"runtime"
	"slices"
	"strings"
	"testing"

	"github.com/weaveplatform/artifactweave/internal/platform/testkit/repopath"
	"github.com/weaveplatform/artifactweave/test/clienttest"
	"github.com/weaveplatform/artifactweave/test/peers"
	"github.com/weaveplatform/artifactweave/test/server/support"
)

func TestAccPeerClientsSQLite(t *testing.T)   { t.Parallel(); testPeerClients(t, peers.SQLite) }
func TestAccPeerClientsPostgres(t *testing.T) { t.Parallel(); testPeerClients(t, peers.Postgres) }

func testPeerClients(t *testing.T, driver peers.Driver) {
	if runtime.GOOS != "linux" {
		t.Skip("pinned client containers require Linux host networking")
	}
	for _, key := range []string{"ARTIFACTWEAVE_NODE_IMAGE", "ARTIFACTWEAVE_MAVEN_IMAGE", "ARTIFACTWEAVE_MAVEN_TOOLS", "ARTIFACTWEAVE_JF_BIN"} {
		if os.Getenv(key) == "" {
			if os.Getenv("CI") != "" || os.Getenv("ARTIFACTWEAVE_REQUIRE_DOCKER") == "1" {
				t.Fatalf("required peer client prerequisite missing: %s", key)
			}
			t.Skip("configure pinned native clients to run this scenario")
		}
	}
	_, all := federation(t, driver)
	for _, format := range []string{"generic", "npm", "maven", "docker"} {
		federate(t, all, "native-"+format, format)
	}
	root := repopath.Root(t)
	for i, origin := range all {
		t.Run(origin.Name, func(t *testing.T) {
			// Output may mention configured credentials. Redact before publishing any
			// diagnostic; exit codes still distinguish launch failures from refusals.
			run := func(cmd *exec.Cmd, wantSuccess bool) string {
				t.Helper()
				out, err := cmd.CombinedOutput()
				text := string(out)
				for _, in := range all {
					text = strings.ReplaceAll(text, in.AdminPassword, "[redacted]")
					text = strings.ReplaceAll(text, base64.StdEncoding.EncodeToString([]byte("admin:"+in.AdminPassword)), "[redacted]")
				}
				if wantSuccess && err != nil {
					t.Fatalf("native client failed: %v\n%s", err, text)
				}
				if !wantSuccess {
					if _, ok := err.(*exec.ExitError); !ok {
						t.Fatalf("expected client refusal, got %v\n%s", err, text)
					}
				}
				return text
			}
			write := func(dir, name, body string) {
				t.Helper()
				if err := os.WriteFile(filepath.Join(dir, name), []byte(body), 0o600); err != nil {
					t.Fatal(err)
				}
			}
			// JFrog CLI writes and reads a generic artifact from every origin.
			dir := t.TempDir()
			home := t.TempDir()
			if err := os.Mkdir(filepath.Join(home, "tmp"), 0o700); err != nil {
				t.Fatal(err)
			}
			write(dir, "payload.txt", "native payload "+origin.Name)
			jf := func(in *peers.Instance, args ...string) *exec.Cmd {
				return clienttest.JF(t.Context(), dir, home, "", nil, append(args, "--url="+in.URL+"/artifactory", "--user=admin", "--password="+in.AdminPassword))
			}
			path := fmt.Sprintf("native-generic/origin%d/payload.txt", i)
			run(jf(origin, "rt", "upload", "payload.txt", path, "--flat=true"), true)
			awaitFile(t, all, path, "native payload "+origin.Name)
			for j, in := range all {
				name := fmt.Sprintf("download%d.txt", j)
				run(jf(in, "rt", "download", path, name, "--flat=true"), true)
				data, err := os.ReadFile(filepath.Join(dir, name))
				if err != nil || string(data) != "native payload "+origin.Name {
					t.Fatal("JF download content differs")
				}
			}
			run(jf(origin, "rt", "set-props", path, "qualified="+origin.Name), true)
			for _, in := range all {
				eventually(t, in, "native property", func() (bool, string) {
					got := properties(t, in, path)["qualified"]
					return slices.Equal(got, []string{origin.Name}), fmt.Sprint(got)
				})
			}
			run(jf(origin, "rt", "delete", path, "--quiet=true"), true)
			awaitFile(t, all, path, "")

			// npm publishes from each origin; each consumer starts without a
			// cache and verifies the downloaded tarball's exact bytes.
			name := fmt.Sprintf("peer-origin-%d", i)
			tarball := support.NPMTarballOf(name, "1.0.0")
			if err := os.WriteFile(filepath.Join(dir, "package.tgz"), tarball, 0o600); err != nil {
				t.Fatal(err)
			}
			npmrc := func(in *peers.Instance) string {
				host := strings.TrimPrefix(in.URL, "http://")
				return "registry=" + in.URL + "/artifactory/api/npm/native-npm/\n//" + host + "/artifactory/api/npm/native-npm/:_auth=" + base64.StdEncoding.EncodeToString(
					[]byte("admin:"+in.AdminPassword),
				) + "\n"
			}
			write(dir, ".npmrc", npmrc(origin))
			run(clienttest.NPM(t.Context(), dir, os.Getenv("ARTIFACTWEAVE_NODE_IMAGE"), nil, []string{"publish", "package.tgz", "--ignore-scripts"}), true)
			for _, in := range all {
				consumer := t.TempDir()
				write(consumer, ".npmrc", npmrc(in))
				eventually(t, in, "npm version", func() (bool, string) {
					status, body := in.Do(t, http.MethodGet, "/artifactory/api/npm/native-npm/"+name, "")
					return status == 200 && strings.Contains(body, `"1.0.0"`), fmt.Sprint(status)
				})
				run(clienttest.NPM(t.Context(), consumer, os.Getenv("ARTIFACTWEAVE_NODE_IMAGE"), nil, []string{"pack", name + "@1.0.0", "--ignore-scripts"}), true)
				data, err := os.ReadFile(filepath.Join(consumer, name+"-1.0.0.tgz"))
				if err != nil || !bytes.Equal(data, tarball) {
					t.Fatal("npm tarball content differs")
				}
			}

			// Maven uses the existing tool manifest and blocked external
			// repositories. Product heads are fresh for every consumer.
			tools := filepath.Join(dir, "maven-tools")
			if err := clienttest.SeedMavenTools(root, "--repository", tools); err != nil {
				t.Fatal(err)
			}
			write(dir, "maven-tools-settings.xml", string(repopath.Read(t, "test/acceptance/clients/maven-tools-settings.xml")))
			mvn := func(args ...string) *exec.Cmd {
				if err := clienttest.CheckMavenToolRepository(tools, clienttest.MavenToolLock(root)); err != nil {
					t.Fatal(err)
				}
				return clienttest.Maven(t.Context(), dir, os.Getenv("ARTIFACTWEAVE_MAVEN_IMAGE"), tools, args)
			}
			for j, in := range all {
				write(
					dir,
					fmt.Sprintf("settings%d.xml", j),
					"<settings><servers><server><id>aw</id><username>admin</username><password>"+in.AdminPassword+"</password></server></servers><mirrors><mirror><id>aw</id><mirrorOf>central</mirrorOf><url>"+in.URL+"/artifactory/native-maven</url></mirror></mirrors></settings>",
				)
			}
			artifact := fmt.Sprintf("origin%d", i)
			run(
				mvn(
					"-s",
					fmt.Sprintf("settings%d.xml", i),
					"org.apache.maven.plugins:maven-deploy-plugin:3.1.3:deploy-file",
					"-Dfile=payload.txt",
					"-DgroupId=org.artifactweave.qualification",
					"-DartifactId="+artifact,
					"-Dversion=1.0.0",
					"-Dpackaging=jar",
					"-DrepositoryId=aw",
					"-Durl="+origin.URL+"/artifactory/native-maven",
				),
				true,
			)
			jar := "native-maven/org/artifactweave/qualification/" + artifact + "/1.0.0/" + artifact + "-1.0.0.jar"
			awaitFile(t, all, jar, "native payload "+origin.Name)
			for j, in := range all {
				head := fmt.Sprintf("consumer%d", j)
				run(
					mvn(
						"-s",
						fmt.Sprintf("settings%d.xml", j),
						"-Dmaven.repo.local=/work/"+head,
						"org.apache.maven.plugins:maven-dependency-plugin:3.8.1:get",
						"-Dtransitive=false",
						"-Dartifact=org.artifactweave.qualification:"+artifact+":1.0.0",
						"-DremoteRepositories=aw::default::"+in.URL+"/artifactory/native-maven",
					),
					true,
				)
				data, err := os.ReadFile(filepath.Join(dir, head, "org/artifactweave/qualification", artifact, "1.0.0", artifact+"-1.0.0.jar"))
				if err != nil || string(data) != "native payload "+origin.Name {
					t.Fatal("Maven resolution content differs")
				}
			}
			// A new head must fail after the repository artifact is deleted.
			do(t, origin, http.MethodDelete, "/artifactory/"+jar, "", http.StatusNoContent)
			awaitFile(t, all, jar, "")
			out := run(
				mvn(
					"-s",
					fmt.Sprintf("settings%d.xml", i),
					"-Dmaven.repo.local=/work/deleted",
					"org.apache.maven.plugins:maven-dependency-plugin:3.8.1:get",
					"-Dtransitive=false",
					"-Dartifact=org.artifactweave.qualification:"+artifact+":1.0.0",
					"-DremoteRepositories=aw::default::"+origin.URL+"/artifactory/native-maven",
				),
				false,
			)
			if !strings.Contains(out, artifact) || !strings.Contains(out, "Could not find artifact") {
				t.Fatal("Maven failed without proving the deleted artifact was unavailable")
			}

			qualifyDockerClients(t, all, origin, i, run)
		})
	}
}

func qualifyDockerClients(t *testing.T, all []*peers.Instance, origin *peers.Instance, index int, run func(*exec.Cmd, bool) string) {
	t.Helper()
	config := t.TempDir()
	dir := t.TempDir()
	var layer bytes.Buffer
	tw := tar.NewWriter(&layer)
	content := "peer " + origin.Name
	if err := tw.WriteHeader(&tar.Header{Name: "qualification.txt", Mode: 0o644, Size: int64(len(content))}); err != nil {
		t.Fatal(err)
	}
	if _, err := tw.Write([]byte(content)); err != nil {
		t.Fatal(err)
	}
	if err := tw.Close(); err != nil {
		t.Fatal(err)
	}
	docker := func(args ...string) *exec.Cmd { return clienttest.Docker(t.Context(), dir, config, args) }
	importCmd := docker("import", "-")
	importCmd.Stdin = bytes.NewReader(layer.Bytes())
	image := strings.TrimSpace(run(importCmd, true))
	t.Cleanup(func() { _ = docker("image", "rm", image).Run() })
	login := func(in *peers.Instance) {
		cmd := docker("login", strings.TrimPrefix(in.URL, "http://"), "--username", "admin", "--password-stdin")
		cmd.Stdin = strings.NewReader(in.AdminPassword)
		run(cmd, true)
	}
	ref := func(in *peers.Instance) string {
		return strings.TrimPrefix(in.URL, "http://") + fmt.Sprintf("/native-docker/qualification:origin%d", index)
	}
	login(origin)
	run(docker("tag", image, ref(origin)), true)
	t.Cleanup(func() { _ = docker("image", "rm", ref(origin)).Run() })
	run(docker("push", ref(origin)), true)
	for _, in := range all {
		eventually(t, in, "Docker manifest", func() (bool, string) {
			status, _, _ := in.DoHeader(
				t,
				http.MethodGet,
				fmt.Sprintf("/v2/native-docker/qualification/manifests/origin%d", index),
				"",
				http.Header{"Accept": []string{"application/vnd.docker.distribution.manifest.v2+json"}},
			)
			return status == 200, fmt.Sprint(status)
		})
		login(in)
		run(docker("pull", ref(in)), true)
		got := strings.TrimSpace(run(docker("image", "inspect", "--format={{.Id}}", ref(in)), true))
		if got != image {
			t.Fatal("Docker resolved different image content")
		}
		in := in
		t.Cleanup(func() { _ = docker("image", "rm", ref(in)).Run() })
	}
}
