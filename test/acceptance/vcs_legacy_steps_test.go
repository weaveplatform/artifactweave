package acceptance

import (
	"archive/zip"
	"bytes"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"os"
	"path/filepath"
	"strings"

	"github.com/cucumber/godog"

	"github.com/weaveplatform/artifactweave/migration/artifacts/adapters/upstream/fakeupstream"
)

func (w *world) registerVCSLegacy(sc *godog.ScenarioContext) {
	sc.Step(
		`^a "(STASH|OLDSTASH|ARTIFACTORY)" VCS upstream is "(public|private)" with marker "([^"]*)"$`,
		w.vcsLegacyUpstream,
	)
	sc.Step(`^I run the pinned VCS client for "([^"]*)" with token "([^"]*)"$`, w.vcsLegacyClient)
	sc.Step(`^the mutable peer VCS selected-file cache is removed$`, func() error {
		if w.vars["vcs-provider"] != "ARTIFACTORY" {
			return nil
		}
		if err := w.deleteAsAdmin(
			"/artifactory/vcs-legacy-cache/team/project/refs/heads/main/index.js",
		); err != nil {
			return err
		}
		return w.statusIs(http.StatusNoContent)
	})
	sc.Step(`^the pinned VCS file "([^"]*)" contains exactly "([^"]*)"$`, w.vcsLegacyFileContent)
	sc.Step(
		`^the pinned VCS client received "(tags|branches|refs|archive|file)" with marker "([^"]*)"$`,
		w.vcsLegacyClientResult,
	)
	sc.Step(`^the VCS upstream advances to marker "([^"]*)"$`, func(marker string) error {
		return w.vcsLegacyContent(marker)
	})
	sc.Step(
		`^the VCS "(refs|archive|file)" upstream response becomes "(malformed|unavailable|redirect|wrong-commit)"$`,
		w.vcsLegacyFault,
	)
	sc.Step(`^the VCS upstream received (\d+) reference requests$`, func(want int) error {
		if got := w.remote.Count("GET", w.vars["vcs-refs-path"]); got != want {
			return fmt.Errorf("VCS ref fetches = %d, want %d", got, want)
		}
		return nil
	})
}

func (w *world) vcsLegacyFileContent(filename, want string) error {
	dir, err := w.filesDir()
	if err != nil {
		return err
	}
	data, err := os.ReadFile(filepath.Join(dir, w.vars["vcs-result-file"]))
	if err != nil {
		return err
	}
	zr, err := zip.NewReader(bytes.NewReader(data), int64(len(data)))
	if err != nil {
		return err
	}
	var selected *zip.File
	for _, file := range zr.File {
		if file.FileInfo().IsDir() && strings.HasPrefix(filename, file.Name) {
			continue
		}
		if selected != nil || file.Name != filename {
			return fmt.Errorf("wrong selected VCS file")
		}
		selected = file
	}
	if selected == nil {
		return fmt.Errorf("missing selected VCS file")
	}
	r, err := selected.Open()
	if err != nil {
		return err
	}
	defer r.Close()
	body, err := io.ReadAll(io.LimitReader(r, 64<<10))
	if err != nil {
		return err
	}
	if string(body) != want+"\n" {
		return fmt.Errorf("selected VCS file has incorrect bytes")
	}
	return nil
}

func (w *world) vcsLegacyUpstream(provider, visibility, marker string) error {
	options := fakeupstream.Options{}
	if visibility == "private" {
		options.Auth = fakeupstream.Auth{Username: "vcs-fixture", Password: "private-fixture-pass"}
	}
	w.remote = fakeupstream.New(w.t, options)
	w.closers = append(w.closers, w.remote.Close)
	if w.vars == nil {
		w.vars = make(map[string]string)
	}
	w.vars["upstream"] = w.remote.URL
	w.vars["vcs-provider"] = provider
	w.vars["vcs-url"] = w.remote.URL + "/git"
	w.vars["vcs-refs-path"] = "/git/scm/team/project.git/info/refs"
	w.vars["vcs-archive-path"] = "/git/rest/api/1.0/projects/team/repos/project/archive"
	if provider == "OLDSTASH" {
		w.vars["vcs-archive-path"] = "/git/plugins/servlet/archive/projects/team/repos/project"
	}
	if provider == "ARTIFACTORY" {
		w.vars["vcs-url"] = w.remote.URL + "/artifactory/api/vcs/peer-vcs"
		w.vars["vcs-refs-path"] = "/artifactory/api/vcs/tags/peer-vcs/team/project"
		w.vars["vcs-file-path"] = "/artifactory/api/vcs/downloadBranchFile/peer-vcs/team/project/main!index.js"
	}
	return w.vcsLegacyContent(marker)
}

func (w *world) vcsLegacyContent(marker string) error {
	root := filepath.Join(repoRoot, "internal/server/vcs/testdata")
	manifest, err := os.ReadFile(filepath.Join(root, "git-archives.json"))
	if err != nil {
		return err
	}
	var provenance struct {
		Fixtures map[string]struct{ Commit string }
	}
	if err := json.Unmarshal(manifest, &provenance); err != nil {
		return err
	}
	oid := provenance.Fixtures[marker].Commit
	if oid == "" {
		return fmt.Errorf("unknown Git archive fixture %q", marker)
	}
	refs := "001e# service=git-upload-pack\n0000"
	for _, name := range []string{"refs/heads/main", "refs/tags/v1.0.0"} {
		ref := oid + " " + name + "\n"
		refs += fmt.Sprintf("%04x%s", len(ref)+4, ref)
	}
	w.remote.Set(
		w.vars["vcs-refs-path"],
		fakeupstream.Resource{Body: []byte(refs + "0000")},
	)
	if w.vars["vcs-provider"] == "ARTIFACTORY" {
		for _, ref := range []struct {
			action, name string
			branch       bool
		}{{"tags", "v1.0.0", false}, {"branches", "main", true}} {
			data, err := json.Marshal(
				[]map[string]any{{"name": ref.name, "commitId": oid, "isBranch": ref.branch}},
			)
			if err != nil {
				return err
			}
			w.remote.Set(
				"/artifactory/api/vcs/"+ref.action+"/peer-vcs/team/project",
				fakeupstream.Resource{Body: data},
			)
		}
		w.vars["vcs-archive-path"] = "/artifactory/api/vcs/downloadCommit/peer-vcs/team/project/" + oid
	}
	archive, err := os.ReadFile(filepath.Join(root, marker+"-archive.tar.gz"))
	if err != nil {
		return err
	}
	w.remote.Set(
		w.vars["vcs-archive-path"],
		fakeupstream.Resource{Body: archive},
	)
	w.vars["vcs-archive"] = string(archive)
	w.vars["vcs-oid"] = oid
	if w.vars["vcs-provider"] == "ARTIFACTORY" {
		for _, action := range []string{"downloadTagFile", "downloadBranchFile"} {
			ref := "v1.0.0"
			if action == "downloadBranchFile" {
				ref = "main"
			}
			for file, suffix := range map[string]string{"index.js": "index", "README.md": "readme", "sub/README.md": "nested-readme"} {
				data, err := os.ReadFile(filepath.Join(root, marker+"-"+suffix+".zip"))
				if err != nil {
					return err
				}
				w.remote.Set(
					"/artifactory/api/vcs/"+action+"/peer-vcs/team/project/"+ref+"!"+file,
					fakeupstream.Resource{Body: data},
				)
			}
		}
	}
	return nil
}

func (w *world) vcsLegacyFault(kind, fault string) error {
	path := w.vars["vcs-"+kind+"-path"]
	response := fakeupstream.Resource{Body: []byte("invalid-vcs-secret-fixture")}
	switch fault {
	case "wrong-commit":
		data, err := os.ReadFile(
			filepath.Join(repoRoot, "internal/server/vcs/testdata/refreshed-vcs-archive.tar.gz"),
		)
		if err != nil {
			return err
		}
		response.Body = data
	case "unavailable":
		response.Status = http.StatusServiceUnavailable
	case "redirect":
		response.Redirect = w.remote.URL + "/git/other-project/archive?at=other"
	}
	w.remote.Set(path, response)
	return nil
}

func (w *world) vcsLegacyClient(operation, token string) error {
	dir, err := w.filesDir()
	if err != nil {
		return err
	}
	// A unique destination per attempt prevents stale downloads and avoids
	// reusing a path immediately after host-side deletion on Docker bind mounts.
	destination, err := os.CreateTemp(dir, "vcs-legacy-result-")
	if err != nil {
		return err
	}
	if err := destination.Close(); err != nil {
		return err
	}
	w.vars["vcs-result-file"] = filepath.Base(destination.Name())
	return w.runSystemRecipe(w.mavenImage, []string{"--entrypoint", "curl"}, []string{
		"--disable", "--fail", "--silent", "--show-error", "--header",
		"Authorization: Bearer " + w.expand(token),
		w.recipeClientURL() + "/artifactory/api/vcs/" + w.expand(operation),
		"--output", "/work/" + w.vars["vcs-result-file"],
	})
}

func (w *world) vcsLegacyClientResult(kind, marker string) error {
	dir, err := w.filesDir()
	if err != nil {
		return err
	}
	data, err := os.ReadFile(filepath.Join(dir, w.vars["vcs-result-file"]))
	if err != nil {
		return err
	}
	switch kind {
	case "tags", "branches":
		var refs []struct {
			Name     string `json:"name"`
			CommitID string `json:"commitId"`
			Branch   bool   `json:"isBranch"`
		}
		if err := json.Unmarshal(data, &refs); err != nil {
			return err
		}
		name := "v1.0.0"
		if kind == "branches" {
			name = "main"
		}
		if len(refs) != 1 || refs[0].Name != name || refs[0].CommitID != w.vars["vcs-oid"] ||
			refs[0].Branch != (kind == "branches") {
			return fmt.Errorf("unexpected VCS %s result", kind)
		}
	case "refs":
		var refs string
		if err := json.Unmarshal(data, &refs); err != nil {
			return err
		}
		if !strings.Contains(refs, w.vars["vcs-oid"]+" refs/heads/main\n") ||
			!strings.Contains(refs, w.vars["vcs-oid"]+" refs/tags/v1.0.0\n") {
			return fmt.Errorf("missing VCS refs")
		}
	case "archive":
		if !bytes.Equal(data, []byte(w.vars["vcs-archive"])) {
			return fmt.Errorf("VCS client did not receive unchanged upstream archive")
		}
		return nil
	case "file":
		zr, err := zip.NewReader(bytes.NewReader(data), int64(len(data)))
		if err != nil || len(zr.File) != 1 || zr.File[0].Name != "index.js" {
			return fmt.Errorf("invalid VCS file ZIP: %v", err)
		}
		body, err := zr.File[0].Open()
		if err != nil {
			return err
		}
		defer body.Close()
		selected, err := io.ReadAll(io.LimitReader(body, 64<<10))
		if err != nil || string(selected) != fmt.Sprintf("module.exports = %q;\n", marker) {
			return fmt.Errorf("VCS selected file differs from the upstream bytes: %v", err)
		}
	}
	return nil
}
