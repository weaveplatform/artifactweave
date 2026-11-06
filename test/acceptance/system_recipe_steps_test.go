package acceptance

import (
	"archive/tar"
	"archive/zip"
	"bytes"
	"compress/gzip"
	"context"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"net/url"
	"os"
	"os/exec"
	"path/filepath"
	"runtime"
	"strings"
	"time"

	"github.com/cucumber/godog"

	"github.com/weaveplatform/artifactweave/migration/artifacts/adapters/upstream/fakeupstream"
)

func (w *world) registerSystemRecipes(sc *godog.ScenarioContext) {
	sc.Step(`^I download the VCS recipe with token "([^"]*)"$`, w.downloadVCSRecipe)
	sc.Step(`^the VCS recipe archive contains marker "([^"]*)"$`, w.vcsRecipeArchive)
	sc.Step(`^a controlled Bazel recipe module is ready$`, w.prepareBazelRecipe)
	sc.Step(`^I build the Bazel public recipe from fresh state$`, w.buildBazelRecipe)
	sc.Step(`^the JVM recipe client image is available$`, w.jvmRecipeAvailable)
	sc.Step(
		`^a controlled (ivy|gradle) recipe repository of class "([^"]*)" is ready$`,
		w.jvmRecipeRepository,
	)
	sc.Step(`^I resolve the (ivy|gradle) recipe with token "([^"]*)"$`, w.resolveJVMRecipe)
	sc.Step(`^the JVM recipe retrieved the controlled artifact$`, w.jvmRecipeArtifact)
	sc.Step(`^I publish the Gradle recipe with token "([^"]*)"$`, w.publishGradleRecipe)
	sc.Step(`^the P2 recipe passes anonymous requests to the registry$`, func() {
		if w.vars == nil {
			w.vars = map[string]string{}
		}
		w.vars["p2-recipe-anonymous"] = "true"
	})
}

func (w *world) jvmRecipeAvailable() error {
	image := os.Getenv("ARTIFACTWEAVE_MAVEN_IMAGE")
	if image == "" {
		if dockerRequired() {
			return fmt.Errorf("JVM recipes require ARTIFACTWEAVE_MAVEN_IMAGE")
		}
		return godog.ErrSkip
	}
	w.mavenImage = image
	if runtime.GOOS != "darwin" {
		return w.clientReachable("Maven")
	}
	ctx, cancel := context.WithTimeout(w.t.Context(), time.Minute)
	defer cancel()
	return exec.CommandContext(ctx, "docker", "image", "inspect", image).Run()
}

func (w *world) jvmRecipeRepository(kind, class string) error {
	if w.vars == nil {
		w.vars = map[string]string{}
	}
	w.vars["recipe-repo"] = kind + "-recipe-" + class
	w.vars["recipe-class"] = class
	var archive bytes.Buffer
	z := zip.NewWriter(&archive)
	entry, err := z.Create("recipe.txt")
	if err != nil {
		return err
	}
	if _, err = io.WriteString(entry, "native recipe artifact\n"); err != nil {
		return err
	}
	if err = z.Close(); err != nil {
		return err
	}
	files := map[string][]byte{}
	layout := "maven-2-default"
	if kind == "ivy" {
		layout = "ivy-default"
		files["org.acme/widget/1.0.0/ivys/ivy-1.0.0.xml"] = []byte(
			`<ivy-module version="2.0"><info organisation="org.acme" module="widget" revision="1.0.0"/><publications><artifact name="widget" type="jar" ext="jar"/></publications></ivy-module>`,
		)
		files["org.acme/widget/1.0.0/jars/widget-1.0.0.jar"] = archive.Bytes()
	} else {
		files["org/acme/widget/1.0.0/widget-1.0.0.pom"] = []byte(
			`<project><modelVersion>4.0.0</modelVersion><groupId>org.acme</groupId><artifactId>widget</artifactId><version>1.0.0</version></project>`,
		)
		files["org/acme/widget/1.0.0/widget-1.0.0.jar"] = archive.Bytes()
	}
	key := w.vars["recipe-repo"]
	config := map[string]any{"rclass": class, "packageType": kind, "repoLayoutRef": layout}
	if class == "remote" {
		w.remote = fakeupstream.New(w.t, fakeupstream.Options{})
		w.closers = append(w.closers, w.remote.Close)
		for path, data := range files {
			w.remote.Set("/"+path, fakeupstream.Resource{Body: data})
		}
		config["url"] = w.remote.URL
	}
	if class == "virtual" {
		member := key + "-member"
		if err = w.recipeRepoPut(
			member,
			map[string]any{"rclass": "local", "packageType": kind, "repoLayoutRef": layout},
		); err != nil {
			return err
		}
		if err = w.recipeDeploy(member, files); err != nil {
			return err
		}
		config["repositories"] = []string{member}
	}
	if err = w.recipeRepoPut(key, config); err != nil {
		return err
	}
	if class == "local" || class == "federated" {
		if err = w.recipeDeploy(key, files); err != nil {
			return err
		}
	}
	return nil
}

func (w *world) recipeRepoPut(key string, config map[string]any) error {
	body, err := json.Marshal(config)
	if err != nil {
		return err
	}
	if err = w.send(
		http.MethodPut,
		"/artifactory/api/repositories/"+key,
		string(body),
		func(r *http.Request) {
			r.SetBasicAuth("admin", testAdminPassword)
		},
	); err != nil {
		return err
	}
	return w.statusIs(http.StatusOK)
}

func (w *world) recipeDeploy(key string, files map[string][]byte) error {
	for path, data := range files {
		if err := w.send(
			http.MethodPut,
			"/artifactory/"+key+"/"+path,
			string(data),
			func(r *http.Request) {
				r.SetBasicAuth("admin", testAdminPassword)
			},
		); err != nil {
			return err
		}
		if err := w.statusIs(http.StatusCreated); err != nil {
			return err
		}
	}
	return nil
}

func (w *world) recipeClientURL() string {
	if runtime.GOOS == "darwin" {
		return strings.Replace(w.url, "127.0.0.1", "host.docker.internal", 1)
	}
	return w.url
}

func (w *world) runSystemRecipe(image string, flags, argv []string) error {
	dir, err := w.filesDir()
	if err != nil {
		return err
	}
	ctx, cancel := context.WithTimeout(w.t.Context(), 3*time.Minute)
	defer cancel()
	args := []string{
		"run",
		"--rm",
		"--network",
		"host",
		"--platform",
		"linux/amd64",
		"--user",
		fmt.Sprintf("%d:%d", os.Getuid(), os.Getgid()),
		"-v",
		dir + ":/work",
		"-w",
		"/work",
		"-e",
		"HOME=/work/recipe-home",
	}
	args = append(args, flags...)
	args = append(args, image)
	args = append(args, argv...)
	return w.runClient(exec.CommandContext(ctx, "docker", args...))
}

func (w *world) resolveJVMRecipe(kind, token string) error {
	dir, err := w.filesDir()
	if err != nil {
		return err
	}
	// Every attempt has a separate dependency cache, including denied retries.
	for _, name := range []string{"recipe-home", "recipe-cache", "recipe-retrieved", "recipe-consumer"} {
		if err = os.RemoveAll(filepath.Join(dir, name)); err != nil {
			return err
		}
	}
	base := w.recipeClientURL() + "/artifactory/" + w.vars["recipe-repo"]
	if kind == "ivy" {
		u, parseErr := url.Parse(base)
		if parseErr != nil {
			return parseErr
		}
		settings := fmt.Sprintf(
			`<ivysettings><settings defaultResolver="aw"/><credentials host="%s" realm="Artifactory Realm" username="${aw.user}" passwd="${aw.token}"/><resolvers><url name="aw"><ivy pattern="%s/[organisation]/[module]/[revision]/ivys/ivy-[revision].xml"/><artifact pattern="%s/[organisation]/[module]/[revision]/[type]s/[artifact]-[revision].[ext]"/></url></resolvers></ivysettings>`,
			u.Hostname(),
			base,
			base,
		)
		if err = os.WriteFile(
			filepath.Join(dir, "ivysettings.xml"),
			[]byte(settings),
			0o600,
		); err != nil {
			return err
		}
		return w.runSystemRecipe(
			w.mavenImage,
			[]string{"--entrypoint", "java"},
			[]string{
				"-Duser.home=/work/recipe-home",
				"-Daw.user=admin",
				"-Daw.token=" + w.expand(token),
				"-jar",
				"/opt/ivy.jar",
				"-settings",
				"/work/ivysettings.xml",
				"-cache",
				"/work/recipe-cache",
				"-dependency",
				"org.acme",
				"widget",
				"1.0.0",
				"-retrieve",
				"/work/recipe-retrieved/[artifact]-[revision].[ext]",
			},
		)
	}
	if err = w.writeGradleRecipe(false); err != nil {
		return err
	}
	return w.runGradleRecipe(token, "resolveRecipe")
}

func (w *world) publishGradleRecipe(token string) error {
	if err := w.writeGradleRecipe(true); err != nil {
		return err
	}
	return w.runGradleRecipe(token, "publish")
}

func (w *world) writeGradleRecipe(publish bool) error {
	dir, err := w.filesDir()
	if err != nil {
		return err
	}
	project := filepath.Join(dir, "recipe-consumer")
	if err = os.MkdirAll(filepath.Join(project, "src", "main", "resources"), 0o755); err != nil {
		return err
	}
	if err = os.WriteFile(
		filepath.Join(project, "settings.gradle"),
		[]byte("rootProject.name = 'widget'\n"),
		0o600,
	); err != nil {
		return err
	}
	build := `plugins { id 'base' }
repositories { maven { url = uri(System.getenv('AW_REPO_URL')); allowInsecureProtocol = true; credentials { username = System.getenv('AW_USER'); password = System.getenv('AW_TOKEN') } } }
configurations { recipe }
dependencies { recipe 'org.acme:widget:1.0.0' }
tasks.register('resolveRecipe', Copy) { from configurations.recipe; into '/work/recipe-retrieved' }
`
	if publish {
		build = `plugins { id 'java-library'; id 'maven-publish' }
group = 'org.acme'
version = '1.0.0'
publishing {
 publications { mavenJava(MavenPublication) { from components.java } }
 repositories { maven { url = uri(System.getenv('AW_REPO_URL')); allowInsecureProtocol = true; credentials { username = System.getenv('AW_USER'); password = System.getenv('AW_TOKEN') } } }
}
`
		if err = os.WriteFile(
			filepath.Join(project, "src", "main", "resources", "recipe.txt"),
			[]byte("native Gradle publication\n"),
			0o600,
		); err != nil {
			return err
		}
		w.vars["recipe-expected"] = "native Gradle publication\n"
	}
	return os.WriteFile(filepath.Join(project, "build.gradle"), []byte(build), 0o600)
}

func (w *world) runGradleRecipe(token, task string) error {
	return w.runSystemRecipe(
		w.mavenImage,
		[]string{
			"--entrypoint",
			"gradle",
			"-e",
			"GRADLE_USER_HOME=/work/recipe-cache",
			"-e",
			"AW_REPO_URL=" + w.recipeClientURL() + "/artifactory/" + w.vars["recipe-repo"],
			"-e",
			"AW_USER=admin",
			"-e",
			"AW_TOKEN=" + w.expand(token),
		},
		[]string{
			"--no-daemon",
			"-Duser.home=/work/recipe-home",
			"-p",
			"/work/recipe-consumer",
			task,
		},
	)
}

func (w *world) jvmRecipeArtifact() error {
	dir, err := w.filesDir()
	if err != nil {
		return err
	}
	z, err := zip.OpenReader(filepath.Join(dir, "recipe-retrieved", "widget-1.0.0.jar"))
	if err != nil {
		return err
	}
	defer func() { _ = z.Close() }()
	for _, file := range z.File {
		if file.Name != "recipe.txt" {
			continue
		}
		reader, openErr := file.Open()
		if openErr != nil {
			return openErr
		}
		data, readErr := io.ReadAll(reader)
		_ = reader.Close()
		if readErr != nil {
			return readErr
		}
		expected := w.vars["recipe-expected"]
		if expected == "" {
			expected = "native recipe artifact\n"
		}
		if string(data) != expected {
			return fmt.Errorf("native recipe artifact content mismatch")
		}
		return nil
	}
	return fmt.Errorf("native recipe artifact has no recipe.txt")
}

func (w *world) prepareBazelRecipe() error {
	if err := dockerAnswers(); err != nil {
		if dockerRequired() {
			return err
		}
		return godog.ErrSkip
	}
	if err := w.recipeRepoPut(
		"bazel-recipe",
		map[string]any{"rclass": "local", "packageType": "bazelmodules"},
	); err != nil {
		return err
	}
	var archive bytes.Buffer
	gz := gzip.NewWriter(&archive)
	tw := tar.NewWriter(gz)
	for name, content := range map[string]string{
		"hello-1.0.0/BUILD.bazel": "filegroup(name = \"all\", srcs = [\"recipe.txt\"], visibility = [\"//visibility:public\"])\n",
		"hello-1.0.0/recipe.txt":  "native Bazel recipe artifact\n",
	} {
		if err := tw.WriteHeader(
			&tar.Header{Name: name, Mode: 0o644, Size: int64(len(content))},
		); err != nil {
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
	return w.recipeDeploy("bazel-recipe", map[string][]byte{
		"modules/hello/1.0.0/MODULE.bazel": []byte(
			"module(name = \"hello\", version = \"1.0.0\")\n",
		),
		"modules/hello/1.0.0/hello-1.0.0.tar.gz": archive.Bytes(),
	})
}

func (w *world) buildBazelRecipe() error {
	dir, err := w.filesDir()
	if err != nil {
		return err
	}
	project, err := os.MkdirTemp(dir, "bazel-consumer-")
	if err != nil {
		return err
	}
	for name, content := range map[string]string{
		"MODULE.bazel": "module(name = \"consumer\")\nbazel_dep(name = \"hello\", version = \"1.0.0\")\n",
		"BUILD.bazel":  "filegroup(name = \"root\", srcs = [\"@hello//:all\"])\n",
	} {
		if err := os.WriteFile(filepath.Join(project, name), []byte(content), 0o600); err != nil {
			return err
		}
	}
	return w.runSystemRecipe(
		"gcr.io/bazel-public/bazel@sha256:d090e62af5f0190bef545f278677bab7175d7db9c028d1e492bb4bfb3e1975fa",
		[]string{"-w", "/work/" + filepath.Base(project)},
		[]string{
			"--output_user_root=/tmp/bazel-output",
			"build",
			"--enable_bzlmod",
			"--registry=" + w.recipeClientURL() + "/artifactory/api/bazelmodules/bazel-recipe",
			"--registry=https://bcr.bazel.build",
			"--lockfile_mode=off",
			"//:root",
		},
	)
}

func (w *world) downloadVCSRecipe(token string) error {
	dir, err := w.filesDir()
	if err != nil {
		return err
	}
	if err := os.Remove(
		filepath.Join(dir, "vcs-recipe.tar.gz"),
	); err != nil &&
		!os.IsNotExist(err) {
		return err
	}
	return w.runSystemRecipe(
		w.mavenImage,
		[]string{"--entrypoint", "curl"},
		[]string{
			"--disable",
			"--fail",
			"--silent",
			"--show-error",
			"--header",
			"Authorization: Bearer " + w.expand(token),
			w.recipeClientURL() + "/artifactory/api/vcs/downloadTag/vcs-recipe/team/project/v1.0.0",
			"--output",
			"/work/vcs-recipe.tar.gz",
		},
	)
}

func (w *world) vcsRecipeArchive(marker string) error {
	dir, err := w.filesDir()
	if err != nil {
		return err
	}
	w.body, err = os.ReadFile(filepath.Join(dir, "vcs-recipe.tar.gz"))
	if err != nil {
		return err
	}
	return w.vcsArchiveContains(marker)
}
