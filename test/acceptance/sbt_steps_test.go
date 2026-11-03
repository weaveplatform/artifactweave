package acceptance

import (
	"archive/zip"
	"fmt"
	"io"
	"io/fs"
	"net/url"
	"os"
	"os/exec"
	"path/filepath"
	"runtime"
	"strings"
	"time"

	"github.com/cucumber/godog"
)

func (w *world) registerSbt(sc *godog.ScenarioContext) {
	sc.Step(`^the sbt client can reach the registry$`, w.sbtAvailable)
	sc.Step(`^sbt publishes the controlled recipe to "([^"]*)"$`, w.publishSbtRecipe)
	sc.Step(
		`^sbt resolves the controlled recipe from "([^"]*)" with a fresh home$`,
		func(key string) error {
			return w.resolveSbtRecipe(key, false)
		},
	)
	sc.Step(
		`^sbt cannot resolve the controlled recipe from "([^"]*)" with invalid credentials$`,
		func(key string) error {
			return w.resolveSbtRecipe(key, true)
		},
	)
}

func (w *world) sbtAvailable() error {
	if bin := os.Getenv("ARTIFACTWEAVE_SBT_BIN"); bin != "" {
		_, err := exec.LookPath(bin)
		return err
	}
	image := os.Getenv("ARTIFACTWEAVE_SBT_IMAGE")
	if image == "" {
		if dockerRequired() {
			return fmt.Errorf("sbt client image is not configured")
		}
		return godog.ErrSkip
	}
	if runtime.GOOS == "darwin" && os.Getenv("ARTIFACTWEAVE_SBT_DESKTOP") == "1" {
		if err := dockerAnswers(); err != nil {
			return err
		}
	} else if err := w.dockerReachable(); err != nil {
		return err
	}
	deadline := time.Now().Add(imageWait)
	for exec.Command("docker", "image", "inspect", image).Run() != nil {
		if time.Now().After(deadline) {
			return fmt.Errorf("sbt image %s was not built within %s", image, imageWait)
		}
		time.Sleep(5 * time.Second)
	}
	return nil
}

const sbtRecipeCommon = `
organization := "org.weave.recipe"
version := "1.0.0"
autoScalaLibrary := false
crossPaths := false
credentials += Credentials("Artifactory Realm", sys.env("AW_HOST"), sys.env("AW_USER"), sys.env("AW_TOKEN"))
resolvers := Seq(("artifactweave" at sys.env("AW_REPOSITORY_URL")).withAllowInsecureProtocol(true))
externalResolvers := resolvers.value
`

const sbtRecipePublish = `
name := "widget"
publishTo := Some(("artifactweave" at sys.env("AW_REPOSITORY_URL")).withAllowInsecureProtocol(true))
Compile / packageDoc / publishArtifact := false
Compile / packageSrc / publishArtifact := false
`

const sbtRecipeResolve = `
name := "consumer"
libraryDependencies += "org.weave.recipe" % "widget" % "1.0.0"
`

func (w *world) publishSbtRecipe(key string) error {
	return w.runSbtRecipe(key, sbtRecipePublish, "publish", false)
}

func (w *world) resolveSbtRecipe(key string, denied bool) error {
	return w.runSbtRecipe(key, sbtRecipeResolve, "update", denied)
}

func (w *world) runSbtRecipe(key, build, task string, denied bool) error {
	dir, err := w.filesDir()
	if err != nil {
		return err
	}
	project, err := os.MkdirTemp(dir, "sbt-recipe-")
	if err != nil {
		return err
	}
	for name, content := range map[string]string{
		"project/build.properties":       "sbt.version=2.0.12\n",
		"build.sbt":                      sbtRecipeCommon + build,
		"src/main/resources/payload.txt": "recipe fixture\n",
	} {
		if err := os.MkdirAll(filepath.Join(project, filepath.Dir(name)), 0o700); err != nil {
			return err
		}
		if err := os.WriteFile(filepath.Join(project, name), []byte(content), 0o600); err != nil {
			return err
		}
	}
	base := w.url
	if os.Getenv("ARTIFACTWEAVE_SBT_BIN") == "" && runtime.GOOS == "darwin" &&
		os.Getenv("ARTIFACTWEAVE_SBT_DESKTOP") == "1" {
		base = strings.Replace(base, "127.0.0.1", "host.docker.internal", 1)
	}
	u, err := url.Parse(base)
	if err != nil {
		return err
	}
	secret := testAdminPassword
	if denied {
		secret = "invalid-sbt-recipe-credential"
	}
	if bin := os.Getenv("ARTIFACTWEAVE_SBT_BIN"); bin != "" {
		// Seed only the compiler/bootstrap graph. The image's seed is built
		// before any test repository or controlled dependency exists.
		if seed := os.Getenv("ARTIFACTWEAVE_SBT_BOOTSTRAP_CACHE"); seed != "" {
			if err := os.CopyFS(filepath.Join(project, "coursier"), os.DirFS(seed)); err != nil {
				return err
			}
		}
		args := []string{
			"--server", "-batch",
			"-Duser.home=" + filepath.Join(project, "home"),
			"-Dsbt.global.base=" + filepath.Join(project, "global"),
			"-Dsbt.ivy.home=" + filepath.Join(project, "ivy"),
		}
		if boot := os.Getenv("ARTIFACTWEAVE_SBT_BOOT"); boot != "" {
			args = append(args, "-Dsbt.boot.directory="+boot)
		}
		cmd := exec.Command(bin, append(args, task)...)
		cmd.Dir = project
		cmd.Env = append(os.Environ(),
			"HOME="+filepath.Join(project, "home"),
			"COURSIER_CACHE="+filepath.Join(project, "coursier"),
			"AW_REPOSITORY_URL="+base+"/artifactory/"+key,
			"AW_HOST="+u.Hostname(),
			"AW_USER=admin",
			"AW_TOKEN="+secret,
		)
		err = w.runClient(cmd)
	} else {
		err = w.runSbtContainer(project, base, key, u.Hostname(), secret, task)
	}
	if err != nil || w.lastRC != 0 || task != "update" {
		return err
	}
	if err := verifySbtArtifact(filepath.Join(project, "coursier")); err != nil {
		return err
	}
	w.body = append(w.body, []byte("\nverified controlled sbt artifact bytes\n")...)
	return nil
}

func (w *world) runSbtContainer(project, base, key, host, secret, task string) error {
	containerDir := "/work/" + filepath.Base(project)
	return w.runContainer(os.Getenv("ARTIFACTWEAVE_SBT_IMAGE"), []string{
		"-w", containerDir,
		"-e", "HOME=" + containerDir + "/home",
		"-e", "COURSIER_CACHE=" + containerDir + "/coursier",
		"-e", "AW_REPOSITORY_URL=" + base + "/artifactory/" + key,
		"-e", "AW_HOST=" + host,
		"-e", "AW_USER=admin",
		"-e", "AW_TOKEN=" + secret,
	}, []string{
		"sh", "-c", `cp -R /opt/sbt-bootstrap-cache "$COURSIER_CACHE" && exec "$@"`, "sbt-recipe",
		"sbt", "--server", "-batch", "-Dsbt.boot.directory=/opt/sbt-boot",
		"-Duser.home=" + containerDir + "/home",
		"-Dsbt.global.base=" + containerDir + "/global",
		"-Dsbt.ivy.home=" + containerDir + "/ivy",
		task,
	})
}

func verifySbtArtifact(cache string) error {
	var artifact string
	if err := filepath.WalkDir(cache, func(path string, entry fs.DirEntry, err error) error {
		if err != nil {
			return err
		}
		if !entry.IsDir() && entry.Name() == "widget-1.0.0.jar" {
			artifact = path
			return fs.SkipAll
		}
		return nil
	}); err != nil {
		return err
	}
	if artifact == "" {
		return fmt.Errorf("sbt update did not fetch the controlled JAR into its fresh cache")
	}
	archive, err := zip.OpenReader(artifact)
	if err != nil {
		return err
	}
	defer archive.Close()
	payload, err := archive.Open("payload.txt")
	if err != nil {
		return err
	}
	defer payload.Close()
	data, err := io.ReadAll(io.LimitReader(payload, 64))
	if err != nil {
		return err
	}
	if string(data) != "recipe fixture\n" {
		return fmt.Errorf("sbt fetched different controlled artifact bytes")
	}
	return nil
}
