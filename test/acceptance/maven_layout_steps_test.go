package acceptance

import (
	"archive/zip"
	"bytes"
	"fmt"
	"os"
	"path/filepath"
)

func (w *world) mavenLayoutRecipe() error {
	if w.vars == nil {
		w.vars = map[string]string{}
	}
	w.vars["recipe-repo"] = "gradle-layout-virtual"
	w.vars["recipe-layout"] = "gradle-default"
	w.vars["recipe-expected"] = "native Maven layout publication\n"
	for _, c := range []struct {
		key    string
		config map[string]any
	}{
		{"mvn-layout-local", map[string]any{"rclass": "local", "packageType": "maven", "repoLayoutRef": "maven-2-default"}},
		{"gradle-layout-virtual", map[string]any{"rclass": "virtual", "packageType": "gradle", "repoLayoutRef": "gradle-default", "repositories": []string{"mvn-layout-local"}, "defaultDeploymentRepo": "mvn-layout-local"}},
	} {
		if err := w.recipeRepoPut(c.key, c.config); err != nil {
			return err
		}
	}
	dir, err := w.filesDir()
	if err != nil {
		return err
	}
	var jar bytes.Buffer
	z := zip.NewWriter(&jar)
	f, err := z.Create("recipe.txt")
	if err != nil {
		return err
	}
	if _, err = f.Write([]byte(w.vars["recipe-expected"])); err != nil {
		return err
	}
	if err = z.Close(); err != nil {
		return err
	}
	return os.WriteFile(filepath.Join(dir, "layout-widget.jar"), jar.Bytes(), 0o600)
}

func (w *world) publishMavenLayoutRecipe(token string) error {
	dir, err := w.filesDir()
	if err != nil {
		return err
	}
	settings := `<settings><servers><server><id>aw-layout</id><username>admin</username><password>` + w.expand(token) + `</password></server></servers></settings>`
	if err = os.WriteFile(filepath.Join(dir, "layout-settings.xml"), []byte(settings), 0o600); err != nil {
		return err
	}
	plugins := os.Getenv("ARTIFACTWEAVE_MAVEN_REPO")
	if plugins == "" {
		home, homeErr := os.UserHomeDir()
		if homeErr != nil {
			return homeErr
		}
		plugins = filepath.Join(home, ".m2", "repository")
	}
	// Only build-tool plugins are warmed. The artifact coordinate is published
	// by Maven here and Gradle resolves it from an empty dependency cache.
	return w.runJVMRecipe([]string{"--entrypoint", "mvn", "-v", plugins + ":/m2"}, []string{
		"-B",
		"--no-transfer-progress",
		"-Dmaven.repo.local=/m2",
		"-Duser.home=/work/layout-publish-home",
		"-s",
		"/work/layout-settings.xml",
		"org.apache.maven.plugins:maven-deploy-plugin:3.1.3:deploy-file",
		"-Dfile=/work/layout-widget.jar",
		"-DgroupId=org.acme",
		"-DartifactId=widget",
		"-Dversion=1.0.0",
		"-Dpackaging=jar",
		"-DrepositoryId=aw-layout",
		fmt.Sprintf("-Durl=%s/artifactory/mvn-layout-local", w.recipeClientURL()),
	})
}
