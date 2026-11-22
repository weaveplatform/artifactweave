package acceptance

import (
	"archive/zip"
	"bytes"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"net/url"
	"os"
	"path/filepath"
)

func (w *world) qualifyNativeReplication(direction string) error {
	image := os.Getenv("ARTIFACTWEAVE_MAVEN_IMAGE")
	if image == "" {
		return fmt.Errorf("required native replication qualification needs ARTIFACTWEAVE_MAVEN_IMAGE")
	}
	fixture, err := w.configureReplicationPair(direction, "maven")
	if err != nil {
		return err
	}
	const base = "selected/replication/widget/1.0/widget-1.0"
	const marker = "replicated native Maven content\n"
	// Select the immutable release coordinate. Concurrent generated repository
	// metadata is not part of this native package consumption scenario.
	if _, err := replicationFixtureRequest(w, http.MethodPost, "/artifactory/api/replications/"+fixture.owningRepo,
		`{"includePathPrefixPattern":"selected/replication/widget/1.0/**"}`, http.StatusOK); err != nil {
		return err
	}
	jar, err := mavenVirtualJar(marker)
	if err != nil {
		return fmt.Errorf("build native replication fixture: %w", err)
	}
	pom := `<project><modelVersion>4.0.0</modelVersion><groupId>selected.replication</groupId><artifactId>widget</artifactId><version>1.0</version><packaging>jar</packaging></project>`
	for path, body := range map[string]string{base + ".jar": string(jar), base + ".pom": pom} {
		if _, err := replicationFixtureRequest(fixture.upstream, http.MethodPut, "/artifactory/"+fixture.sourceKey+"/"+path, body, http.StatusCreated); err != nil {
			return err
		}
	}
	if err := w.executeAndWaitReplication(fixture.owningRepo); err != nil {
		return err
	}
	// Check the owning cache alias before a native request can warm it. The
	// independent source then stops, so client success requires replicated data.
	for path, expected := range map[string]string{base + ".jar": string(jar), base + ".pom": pom} {
		if err := replicationBytes(fixture.consumer, fixture.consumerKey, path, expected); err != nil {
			return err
		}
	}
	indexed, err := replicationFixtureRequest(fixture.consumer, http.MethodGet,
		"/artifactory/api/search/gavc?g=selected.replication&a=widget&v=1.0&repos="+fixture.consumerKey, "", http.StatusOK)
	if err != nil {
		return err
	}
	var search struct {
		Results []struct {
			URI string `json:"uri"`
		} `json:"results"`
	}
	if json.Unmarshal(indexed, &search) != nil || len(search.Results) == 0 {
		return fmt.Errorf("replication did not publish the native Maven coordinate index")
	}
	const user = "replication-consumer"
	if _, err := replicationFixtureRequest(fixture.consumer, http.MethodPut, "/artifactory/api/security/users/"+user,
		`{"name":"replication-consumer","email":"consumer@example.invalid","password":"`+replicationTransportPassword+`","admin":false}`, http.StatusCreated); err != nil {
		return err
	}
	permissionRepo := fixture.consumerKey
	if direction == "pull" {
		permissionRepo = fixture.owningRepo
	}
	if _, err := replicationFixtureRequest(fixture.consumer, http.MethodPut, "/artifactory/api/security/permissions/"+user,
		`{"name":"replication-consumer","repositories":["`+permissionRepo+`"],"principals":{"users":{"replication-consumer":["r"]}}}`, http.StatusOK); err != nil {
		return err
	}
	fixture.upstream.stop()
	root := fixture.consumer.recipeClientURL() + "/artifactory/" + fixture.consumerKey
	address, err := url.Parse(root)
	if err != nil {
		return fmt.Errorf("native replication consumer address: %w", err)
	}
	settings := fmt.Sprintf(
		`<ivysettings><settings defaultResolver="replication"/><credentials host="%s" realm="Artifactory Realm" username="%s" passwd="%s"/><resolvers><ibiblio name="replication" root="%s" m2compatible="true" usepoms="true" useMavenMetadata="false"/></resolvers></ivysettings>`,
		address.Hostname(),
		user,
		replicationTransportPassword,
		root,
	)
	dir, err := w.filesDir()
	if err != nil {
		return err
	}
	if err := os.WriteFile(filepath.Join(dir, "replication-ivy.xml"), []byte(settings), 0o600); err != nil {
		return fmt.Errorf("write owned native replication settings: %w", err)
	}
	w.mavenImage = image
	if err := w.runJVMRecipe([]string{"--pull", "never", "--entrypoint", "java"}, []string{
		"-Duser.home=/work", "-jar", "/opt/ivy.jar", "-settings", "/work/replication-ivy.xml",
		"-dependency", "selected.replication", "widget", "1.0", "-confs", "default",
		"-cache", "/work/replication-ivy-cache", "-retrieve", "/work/replication-resolved/[artifact]-[revision].[ext]",
	}); err != nil {
		return fmt.Errorf("run fresh native replication consumer: %w", err)
	}
	if w.lastRC != 0 {
		return fmt.Errorf("fresh native replication consumer failed with exit%d", w.lastRC)
	}
	downloaded, err := os.ReadFile(filepath.Join(dir, "replication-resolved", "widget-1.0.jar"))
	if err != nil || !bytes.Equal(downloaded, jar) {
		return fmt.Errorf("native replication consumer did not resolve the exact replicated JAR")
	}
	reader, err := zip.NewReader(bytes.NewReader(downloaded), int64(len(downloaded)))
	if err != nil {
		return fmt.Errorf("native replication JAR is invalid: %w", err)
	}
	entry, err := reader.Open("origin.txt")
	if err != nil {
		return fmt.Errorf("native replication marker missing: %w", err)
	}
	defer entry.Close()
	actual, err := io.ReadAll(io.LimitReader(entry, 1024))
	if err != nil || string(actual) != marker {
		return fmt.Errorf("native replication consumer resolved another artifact's content")
	}
	return nil
}
