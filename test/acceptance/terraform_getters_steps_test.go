package acceptance

import (
	"archive/tar"
	"archive/zip"
	"bytes"
	"compress/gzip"
	"context"
	"crypto/sha256"
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
	"github.com/dsnet/compress/bzip2"
	"github.com/ulikunitz/xz"

	"github.com/weaveplatform/artifactweave/internal/artifacts/adapters/upstream/fakeupstream"
	"github.com/weaveplatform/artifactweave/internal/platform/sql/sqltest"
)

type terraformGetterPackage struct {
	name, version, path, selector string
	archive                       []byte
}

func (w *world) registerTerraformGetters(sc *godog.ScenarioContext) {
	sc.Step(`^the module getter registry is running on "(sqlite|postgres)"$`, func(backend string) error {
		if w.vars == nil {
			w.vars = map[string]string{}
		}
		w.vars["getter-backend"] = backend
		w.env = append(w.env, "ARTIFACTWEAVE_DATABASE_DRIVER="+backend, "ARTIFACTWEAVE_DATABASE_DSN=")
		if backend == "postgres" {
			dsn := sqltest.ScratchDSN(w.t, sqltest.PostgresDSN(w.t))
			w.env = append(w.env, "ARTIFACTWEAVE_DATABASE_DSN="+dsn)
		}
		return w.start()
	})
	sc.Step(`^"(Terraform|OpenTofu)" installs additional module getters through a "(remote|virtual)" registry cold and offline$`, w.terraformAdditionalGetters)
}

// terraformAdditionalGetters uses the standalone application, its SQL store and
// genuine HTTPS discovery. Native clients receive registry coordinates, never a
// source URL obtained by the test on their behalf.
func (w *world) terraformAdditionalGetters(client, selection string) error {
	envName, wantVersion := "ARTIFACTWEAVE_TERRAFORM_BIN", "1.17.1"
	if client == "OpenTofu" {
		envName, wantVersion = "ARTIFACTWEAVE_TOFU_BIN", "1.13.9"
	}
	program := os.Getenv(envName)
	if program == "" {
		if os.Getenv("CI") == "true" {
			return fmt.Errorf("required pinned %s executable is absent", client)
		}
		return godog.ErrSkip
	}
	if !filepath.IsAbs(program) {
		return fmt.Errorf("%s must name an absolute pinned executable", envName)
	}
	if err := w.remoteUpstream("/ready", "ready"); err != nil {
		return err
	}
	packages, err := w.terraformGetterPackages()
	if err != nil {
		return err
	}
	configure := func(method, key string, doc any) error {
		body, err := json.Marshal(doc)
		if err != nil {
			return err
		}
		if err := w.send(method, "/artifactory/api/repositories/"+key, string(body), getterAdmin); err != nil {
			return err
		}
		return w.statusIs(http.StatusOK)
	}
	remoteConfig := map[string]any{
		"rclass": "remote", "packageType": "terraform",
		"url": w.remote.URL, "terraformRegistryUrl": w.remote.URL,
	}
	if err := configure("PUT", "tf-getters", remoteConfig); err != nil {
		return err
	}
	readRepo := "tf-getters"
	if selection == "virtual" {
		readRepo = "tf-getters-all"
		if err := configure("PUT", readRepo, map[string]any{
			"rclass": "virtual", "packageType": "terraform", "repositories": []string{"tf-getters"},
		}); err != nil {
			return err
		}
	}
	if err := w.send("POST", "/access/api/v1/tokens", `{"expires_in":3600}`, getterAdmin); err != nil {
		return err
	}
	if err := w.statusIs(http.StatusOK); err != nil {
		return err
	}
	var token struct {
		Access string `json:"access_token"`
	}
	if err := json.Unmarshal(w.body, &token); err != nil || token.Access == "" {
		return fmt.Errorf("native getter acceptance did not obtain a scoped access token")
	}
	host, services, discovery := strings.TrimPrefix(w.url, "http://"), "", "HTTPS"
	if runtime.GOOS == "darwin" {
		// The pinned native builds use macOS's system trust store rather than
		// SSL_CERT_FILE. Keep the established loopback override for local runs;
		// Linux CI must perform actual HTTPS discovery, without an override.
		services, err = w.terraformRegistryServices()
		if err != nil {
			return err
		}
		discovery = "local-service-override"
	} else {
		if err := w.frontWithTLSBase(true); err != nil {
			return err
		}
		host = w.vars["tlshost"]
	}
	for _, phase := range []string{"cold", "offline-fresh-client"} {
		if phase != "cold" {
			w.remote.Close()
			remoteConfig["offline"] = true
			if err := configure("POST", "tf-getters", remoteConfig); err != nil {
				return err
			}
		}
		before := len(w.remote.Requests())
		dir := w.t.TempDir()
		config := filepath.Join(dir, "client.rc")
		if err := os.WriteFile(config, fmt.Appendf([]byte(services), "credentials %q { token = %q }\n", host, token.Access), 0o600); err != nil {
			return err
		}
		var root strings.Builder
		for _, p := range packages {
			fmt.Fprintf(&root, "module %q {\n source = %q\n version = %q\n}\n", p.name,
				host+"/"+readRepo+"__acme/getters/aws", "~> "+strings.TrimSuffix(p.version, ".1"))
			fmt.Fprintf(&root, "output %q { value = module.%s.identity }\n", p.name, p.name)
		}
		if err := os.WriteFile(filepath.Join(dir, "main.tf"), []byte(root.String()), 0o600); err != nil {
			return err
		}
		// An allowlist prevents ambient Git, SSH, cloud, netrc, CLI arguments,
		// credentials helpers and global module/plugin caches entering the child.
		env := []string{
			"PATH=" + os.Getenv("PATH"), "HOME=" + dir, "TMPDIR=" + dir,
			"TF_CLI_CONFIG_FILE=" + config, "TOFU_CLI_CONFIG_FILE=" + config,
			"NETRC=" + filepath.Join(dir, "empty.netrc"), "SSL_CERT_FILE=" + w.tlsCA,
			"TF_IN_AUTOMATION=1", "TF_INPUT=0", "CHECKPOINT_DISABLE=1",
		}
		if err := os.WriteFile(filepath.Join(dir, "empty.netrc"), nil, 0o600); err != nil {
			return err
		}
		version, err := w.terraformGetterCommand(program, dir, env, "version", "-json")
		if err != nil {
			return err
		}
		var info struct {
			Version string `json:"terraform_version"`
		}
		if json.Unmarshal(version, &info) != nil || info.Version != wantVersion {
			return fmt.Errorf("%s executable differs from the required %s pin", client, wantVersion)
		}
		w.markCommand()
		if _, err := w.terraformGetterCommand(program, dir, env, "init", "-backend=false", "-input=false", "-no-color"); err != nil {
			return err
		}
		if err := w.answeredNo500(); err != nil {
			return err
		}
		if err := w.terraformGetterRequests(readRepo, packages); err != nil {
			return err
		}
		if _, err := w.terraformGetterCommand(program, dir, env, "apply", "-auto-approve", "-input=false", "-no-color"); err != nil {
			return err
		}
		outputs, err := w.terraformGetterCommand(program, dir, env, "output", "-json")
		if err != nil {
			return err
		}
		if err := w.terraformGetterIdentity(dir, packages, outputs); err != nil {
			return err
		}
		if phase != "cold" && len(w.remote.Requests()) != before {
			return fmt.Errorf("offline fresh-client install contacted the upstream")
		}
		w.t.Logf(
			"NATIVE_GETTERS client=%s version=%s backend=%s selection=%s phase=%s packages=%d discovery=%s versions=PASS sibling=PASS originals=PASS",
			client,
			info.Version,
			w.vars["getter-backend"],
			selection,
			phase,
			len(packages),
			discovery,
		)
	}
	return nil
}

func getterAdmin(r *http.Request) { r.SetBasicAuth("admin", testAdminPassword) }

func (w *world) terraformGetterCommand(program, dir string, env []string, args ...string) ([]byte, error) {
	ctx, cancel := context.WithTimeout(w.t.Context(), 90*time.Second)
	defer cancel()
	cmd := exec.CommandContext(ctx, program, args...)
	cmd.Dir, cmd.Env, cmd.WaitDelay = dir, env, time.Second
	out, err := cmd.CombinedOutput()
	if err != nil {
		// Client diagnostics may contain signed capabilities. Keep them private
		// rather than exposing them in CI's console or test failure messages.
		diagnostics := os.Getenv("ARTIFACTWEAVE_GETTERS_DIAGNOSTICS")
		if diagnostics == "" {
			diagnostics = w.t.ArtifactDir()
		}
		if err := os.MkdirAll(diagnostics, 0o700); err != nil {
			return nil, fmt.Errorf("native %s failed; could not create private diagnostics: %w", args[0], err)
		}
		file, writeErr := os.CreateTemp(diagnostics, "terraform-getter-*.log")
		if writeErr != nil {
			return nil, fmt.Errorf("native %s failed; could not retain private diagnostics: %w", args[0], writeErr)
		}
		_, writeErr = file.Write(out)
		if writeErr == nil {
			_, writeErr = fmt.Fprintf(file, "\nSanitized server GET records: %v\n", w.requests("GET"))
		}
		closeErr := file.Close()
		if writeErr != nil || closeErr != nil {
			return nil, fmt.Errorf("native %s failed; private diagnostic write failed", args[0])
		}
		return out, fmt.Errorf("native %s failed: %w; private diagnostics: %s", args[0], err, file.Name())
	}
	return out, nil
}

func (w *world) terraformGetterPackages() ([]terraformGetterPackage, error) {
	putJSON := func(path string, doc any) {
		body, _ := json.Marshal(doc)
		w.remote.Set(path, fakeupstream.Resource{Body: body, Header: http.Header{"Content-Type": {"application/json"}}})
	}
	putJSON("/.well-known/terraform.json", map[string]string{"modules.v1": "/registry/modules/"})
	var packages []terraformGetterPackage
	var versions []map[string]string
	for i, name := range []string{"header", "html", "redirect", "chain", "gnu", "bsd", "standalone", "vanity_checksum", "plain_tar", "tgz", "tar_gz", "tar_bz2", "tbz2", "tar_tbz2", "tar_xz", "txz", "gz", "bz2", "xz"} {
		version := fmt.Sprintf("%d.0.1", i+1)
		format, ext := "zip", ".zip"
		if name == "plain_tar" {
			format, ext = "tar", ".tar"
		} else if name == "tgz" {
			format, ext = "tar.gz", ".tgz"
		} else if strings.HasPrefix(name, "tar_") {
			format = strings.ReplaceAll(name, "_", ".")
			ext = "." + format
			if format == "tar.tbz2" {
				format = "tar.bz2"
			}
		} else if name == "tbz2" {
			format, ext = "tar.bz2", ".tbz2"
		} else if name == "txz" {
			format, ext = "tar.xz", ".txz"
		} else if name == "gz" || name == "bz2" || name == "xz" {
			format, ext = name, "."+name
		}
		archive, err := terraformGetterArchive(name, format)
		if err != nil {
			return nil, err
		}
		path := "/archives/" + version + ext
		selector := "root/modules/demo"
		if format == "gz" || format == "bz2" || format == "xz" {
			path, selector = "/archives/"+version+"/main.tf"+ext, ""
		}
		w.remote.Set(path, fakeupstream.Resource{Body: archive, Header: http.Header{"Content-Type": {"application/octet-stream"}}})
		selected := path
		if selector != "" {
			selected += "//" + selector
		}
		if name == "tgz" {
			selected += "?archive=tgz"
		}
		location := selected
		header := func(next string) fakeupstream.Resource {
			return fakeupstream.Resource{Status: 200, Body: []byte("complete vanity response"), Header: http.Header{"X-Terraform-Get": {next}}}
		}
		switch name {
		case "header", "html", "redirect", "chain":
			location = "/vanity/" + name + "?parent=private"
			resource := header(selected)
			if name == "html" {
				resource = fakeupstream.Resource{
					Body:   []byte(`<head><meta name="terraform-get" content="` + selected + `"></head>`),
					Header: http.Header{"Content-Type": {"text/html"}},
				}
			} else if name == "redirect" {
				resource = fakeupstream.Resource{Redirect: "/effective/vanity"}
				w.remote.Set("/effective/vanity", header(".."+selected))
			} else if name == "chain" {
				resource = header("/vanity/second")
				w.remote.Set("/vanity/second", header(selected))
			}
			w.remote.Set("/vanity/"+name, resource)
		case "gnu", "bsd", "standalone", "vanity_checksum":
			digest := sha256.Sum256(archive)
			record := fmt.Sprintf("%x  %s\n", digest, filepath.Base(path))
			if name == "bsd" {
				record = fmt.Sprintf("SHA256 (%s) = %x\n", filepath.Base(path), digest)
			} else if name == "standalone" {
				record = fmt.Sprintf("%x\n", digest)
			}
			checksumPath := "/checksums/" + name
			w.remote.Set(checksumPath, fakeupstream.Resource{Body: []byte(record), Header: http.Header{"Content-Type": {"text/plain"}}})
			location += "?checksum=" + url.QueryEscape("file:"+w.remote.URL+checksumPath)
			if name == "vanity_checksum" {
				w.remote.Set("/vanity/checksum", header(location))
				location = "/vanity/checksum"
			}
		}
		w.remote.Set("/registry/modules/acme/getters/aws/"+version+"/download", fakeupstream.Resource{Status: 204, Header: http.Header{"X-Terraform-Get": {location}}})
		previous := fmt.Sprintf("%d.0.0", i+1)
		// Direct remote lists validate every version's location before filtering
		// it. An older version is valid metadata, but its archive must never be
		// fetched when the native client selects the newer matching version.
		w.remote.Set(
			"/registry/modules/acme/getters/aws/"+previous+"/download",
			fakeupstream.Resource{Status: 204, Header: http.Header{"X-Terraform-Get": {"/archives/unselected.zip"}}},
		)
		versions = append(versions, map[string]string{"version": previous}, map[string]string{"version": version})
		packages = append(packages, terraformGetterPackage{name: name, version: version, path: path, selector: selector, archive: archive})
	}
	putJSON("/registry/modules/acme/getters/aws/versions", map[string]any{"modules": []any{map[string]any{"versions": versions}}})
	return packages, nil
}

func terraformGetterArchive(identity, format string) ([]byte, error) {
	files := []struct{ name, body string }{
		{"root/modules/demo/main.tf", "module \"sibling\" { source = \"../shared\" }\noutput \"identity\" { value = module.sibling.identity }\n"},
		{"root/modules/shared/main.tf", fmt.Sprintf("output \"identity\" { value = %q }\n", identity)},
		{"root/README.md", "the entire original package remains cached\n"},
	}
	var out bytes.Buffer
	if format == "zip" {
		z := zip.NewWriter(&out)
		for _, file := range files {
			f, err := z.Create(file.name)
			if err != nil {
				return nil, err
			}
			if _, err := io.WriteString(f, file.body); err != nil {
				return nil, err
			}
		}
		if err := z.Close(); err != nil {
			return nil, err
		}
		return out.Bytes(), nil
	}
	var dest io.Writer = &out
	var compressed io.WriteCloser
	var err error
	switch format {
	case "gz", "tar.gz":
		compressed = gzip.NewWriter(&out)
	case "bz2", "tar.bz2":
		compressed, err = bzip2.NewWriter(&out, nil)
	case "xz", "tar.xz":
		compressed, err = xz.NewWriter(&out)
	}
	if err != nil {
		return nil, err
	}
	if compressed != nil {
		dest = compressed
	}
	if format == "gz" || format == "bz2" || format == "xz" {
		if _, err := fmt.Fprintf(dest, "output \"identity\" { value = %q }\n", identity); err != nil {
			return nil, err
		}
		if err := compressed.Close(); err != nil {
			return nil, err
		}
		return out.Bytes(), nil
	}
	tw := tar.NewWriter(dest)
	for _, file := range files {
		if err := tw.WriteHeader(&tar.Header{Name: file.name, Mode: 0o600, Size: int64(len(file.body))}); err != nil {
			return nil, err
		}
		if _, err := io.WriteString(tw, file.body); err != nil {
			return nil, err
		}
	}
	if err := tw.Close(); err != nil {
		return nil, err
	}
	if compressed != nil {
		if err := compressed.Close(); err != nil {
			return nil, err
		}
	}
	return out.Bytes(), nil
}

func (w *world) terraformGetterRequests(repo string, packages []terraformGetterPackage) error {
	if w.remote.Count("GET", "/archives/unselected.zip") != 0 {
		return fmt.Errorf("native version selection fetched an unselected archive")
	}
	if err := w.serverAnswered(200, "GET", "/.well-known/terraform.json"); err != nil {
		return err
	}
	base := "/artifactory/api/terraform/v1/modules/" + repo + "__acme/getters/aws/"
	if err := w.serverAnswered(200, "GET", base+"versions"); err != nil {
		return err
	}
	for _, p := range packages {
		if err := w.serverAnswered(204, "GET", base+p.version+"/download"); err != nil {
			return err
		}
		if w.remote.Count("GET", p.path) < 1 {
			return fmt.Errorf("native install never acquired the selected %s package", p.name)
		}
	}
	return nil
}

func (w *world) terraformGetterIdentity(dir string, packages []terraformGetterPackage, outputs []byte) error {
	var values map[string]struct {
		Value string `json:"value"`
	}
	if err := json.Unmarshal(outputs, &values); err != nil {
		return err
	}
	var manifest struct {
		Modules []struct{ Key, Version, Dir string }
	}
	data, err := os.ReadFile(filepath.Join(dir, ".terraform/modules/modules.json"))
	if err != nil {
		return err
	}
	if err := json.Unmarshal(data, &manifest); err != nil {
		return err
	}
	for _, p := range packages {
		if values[p.name].Value != p.name {
			return fmt.Errorf("native output lost %s module identity", p.name)
		}
		selected, sibling := false, p.selector == ""
		for _, m := range manifest.Modules {
			selected = selected || (m.Key == p.name && m.Version == p.version && strings.HasSuffix(m.Dir, p.selector))
			sibling = sibling || (m.Key == p.name+".sibling" && strings.HasSuffix(m.Dir, "root/modules/shared"))
		}
		if !selected || !sibling {
			return fmt.Errorf("native manifest lost %s selected version/directory or relative sibling", p.name)
		}
		if err := w.send("GET", "/artifactory/tf-getters"+p.path, "", getterAdmin); err != nil {
			return err
		}
		if w.status != 200 || !bytes.Equal(w.body, p.archive) {
			return fmt.Errorf("cached original %s archive differs from the upstream bytes", p.name)
		}
	}
	return nil
}
