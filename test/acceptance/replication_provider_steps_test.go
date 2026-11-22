package acceptance

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"net/http"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"time"
)

func (w *world) qualifyProviderReplication() (result error) {
	program := os.Getenv("ARTIFACTWEAVE_TERRAFORM_BIN")
	if program == "" {
		var err error
		program, err = exec.LookPath("terraform")
		if err != nil {
			return fmt.Errorf("required replication provider qualification needs pinned Terraform")
		}
	}
	dir, err := w.filesDir()
	if err != nil {
		return err
	}
	work := filepath.Join(dir, "replication-provider")
	if err := os.MkdirAll(work, 0o700); err != nil {
		return fmt.Errorf("create owned provider workspace: %w", err)
	}
	const scenario = "deploy/artifactweave/terraform/single-replication"
	peer := w.replicationPeer
	if peer == nil {
		return fmt.Errorf("provider qualification requires two owned applications")
	}
	for _, key := range []string{"provider-target", "provider-upstream"} {
		if _, err := replicationFixtureRequest(
			peer,
			http.MethodPut,
			"/artifactory/api/repositories/"+key,
			`{"rclass":"local","packageType":"generic"}`,
			http.StatusOK,
		); err != nil {
			return err
		}
	}
	secrets := []string{testAdminPassword}
	for revision := 1; revision <= 2; revision++ {
		for _, role := range []string{"target", "upstream"} {
			name := fmt.Sprintf("replicator-%d", revision)
			if role == "upstream" {
				name = fmt.Sprintf("replication-upstream-user-%d", revision)
			}
			password := fmt.Sprintf("Terraform-replication-%s-%d", role, revision)
			secrets = append(secrets, password)
			body, _ := json.Marshal(map[string]any{"name": name, "email": name + "@example.invalid", "password": password, "admin": false})
			if _, err := replicationFixtureRequest(peer, http.MethodPut, "/artifactory/api/security/users/"+name, string(body), http.StatusCreated); err != nil {
				return err
			}
			body, _ = json.Marshal(
				map[string]any{"name": name, "repositories": []string{"provider-" + role}, "principals": map[string]any{"users": map[string][]string{name: {"r", "w", "n", "d"}}}},
			)
			if _, err := replicationFixtureRequest(peer, http.MethodPut, "/artifactory/api/security/permissions/"+name, string(body), http.StatusOK); err != nil {
				return err
			}
		}
	}
	tokenBody, err := replicationFixtureRequest(w, http.MethodPost, "/access/api/v1/tokens", `{"scope":"applied-permissions/admin","expires_in":3600}`, http.StatusOK)
	if err != nil {
		return err
	}
	var token struct {
		Value string `json:"access_token"`
	}
	if json.Unmarshal(tokenBody, &token) != nil || token.Value == "" {
		return fmt.Errorf("owned provider token admission failed")
	}
	secrets = append(secrets, token.Value)
	secrets = append(secrets, testMasterKey)
	for _, instance := range []*world{w, peer} {
		for _, entry := range instance.env {
			if value, found := strings.CutPrefix(entry, "ARTIFACTWEAVE_DATABASE_DSN="); found && value != "" {
				secrets = append(secrets, value)
			}
		}
	}
	defer func() { result = errors.Join(result, w.replicationProviderEvidence(work, secrets)) }()
	env := map[string]string{
		"PATH":           filepath.Dir(program) + string(os.PathListSeparator) + os.Getenv("PATH"),
		"TERRAFORM_WORK": work, "TF_CLI_CONFIG_FILE": filepath.Join(work, "terraformrc"), "TF_IN_AUTOMATION": "1", "TF_INPUT": "0",
		"TF_VAR_url": w.url, "TF_VAR_access_token": token.Value, "TF_VAR_repository_prefix": "p11-provider",
		"TF_VAR_target_url": peer.url + "/artifactory/provider-target", "TF_VAR_upstream_url": peer.url + "/artifactory/provider-upstream",
		"TF_VAR_proxy_key": "p11-provider-proxy", "TF_VAR_revision": "1", "TF_VAR_target_password": "Terraform-replication-target-1",
		"TF_VAR_upstream_password": "Terraform-replication-upstream-1", "TF_VAR_sync_statistics": "true",
	}
	if mirror := os.Getenv("ARTIFACTWEAVE_TERRAFORM_PROVIDER_MIRROR"); mirror != "" {
		env["TERRAFORM_PROVIDER_MIRROR"] = mirror
	}
	log := filepath.Join(work, "provider.log")
	if err := os.WriteFile(env["TF_CLI_CONFIG_FILE"], nil, 0o600); err != nil {
		return fmt.Errorf("initialize owned Terraform configuration: %w", err)
	}
	run := func(command string, args ...string) ([]byte, error) {
		return w.replicationProviderCommand(command, args, env, secrets, log)
	}
	version, err := run(program, "version", "-json")
	if err != nil {
		return err
	}
	var versions struct {
		Version   string            `json:"terraform_version"`
		Providers map[string]string `json:"provider_selections"`
	}
	if json.Unmarshal(version, &versions) != nil || versions.Version != "1.17.1" {
		return fmt.Errorf("replication qualification requires pinned Terraform1.17.1")
	}
	// The existing bootstrap checks release SHA256s and initializes the copied
	// lockfile through its private mirror. Validate mode starts no application.
	tfDir, err := prepareReplicationProviderWorkspace(work, scenario, env, run)
	if err != nil {
		return err
	}
	tf := func(args ...string) ([]byte, error) {
		return run(program, append([]string{"-chdir=" + tfDir}, args...)...)
	}
	version, err = tf("version", "-json")
	if err != nil {
		return err
	}
	if json.Unmarshal(version, &versions) != nil || versions.Providers["registry.terraform.io/jfrog/artifactory"] != "12.11.14" {
		return fmt.Errorf("replication qualification did not install pinned JFrog provider12.11.14")
	}
	if _, err := replicationFixtureRequest(w, http.MethodPatch, "/artifactory/api/system/configuration",
		`{"proxies":{"p11-provider-proxy":{"host":"127.0.0.1","port":9}}}`, http.StatusOK, "application/yaml"); err != nil {
		return err
	}
	// Cleanup always addresses only this Terraform state's resources while the
	// two fixture processes are still live; no ambient state is loaded.
	defer func() { _, err := tf("destroy", "-auto-approve", "-no-color"); result = errors.Join(result, err) }()
	for revision := 1; revision <= 2; revision++ {
		env["TF_VAR_revision"] = fmt.Sprint(revision)
		env["TF_VAR_target_password"] = fmt.Sprintf("Terraform-replication-target-%d", revision)
		env["TF_VAR_upstream_password"] = fmt.Sprintf("Terraform-replication-upstream-%d", revision)
		if _, err := tf("apply", "-auto-approve", "-no-color"); err != nil {
			return err
		}
		if _, err := tf("plan", "-detailed-exitcode", "-no-color"); err != nil {
			return err
		}
		want := http.StatusConflict
		if revision == 2 {
			want = http.StatusNotImplemented
		} // Stored statistics merge is outside ordinary P11 transfer.
		if _, err := replicationFixtureRequest(w, http.MethodPost, "/artifactory/api/replication/execute/p11-provider-local/release", "[]", want); err != nil {
			return err
		}
	}
	env["TF_VAR_sync_statistics"] = "false"
	if _, err := tf("apply", "-auto-approve", "-no-color"); err != nil {
		return err
	}
	if _, err := tf("plan", "-detailed-exitcode", "-no-color"); err != nil {
		return err
	}
	for _, source := range []struct {
		instance           *world
		repository, marker string
	}{
		{w, "p11-provider-local", "provider push bytes"}, {peer, "provider-upstream", "provider pull bytes"},
	} {
		for _, path := range []string{"release/current.bin", "release/gone.bin"} {
			if _, err := replicationFixtureRequest(source.instance, http.MethodPut, "/artifactory/"+source.repository+"/"+path, source.marker, http.StatusCreated); err != nil {
				return err
			}
		}
	}
	for _, key := range []string{"p11-provider-local", "p11-provider-remote"} {
		if err := w.executeAndWaitReplicationPath(key, "release"); err != nil {
			return err
		}
	}
	if err := replicationBytes(peer, "provider-target", "release/current.bin", "provider push bytes"); err != nil {
		return err
	}
	if err := replicationBytes(w, "p11-provider-remote-cache", "release/current.bin", "provider pull bytes"); err != nil {
		return err
	}
	for _, source := range []struct {
		instance   *world
		repository string
	}{{w, "p11-provider-local"}, {peer, "provider-upstream"}} {
		if _, err := replicationFixtureRequest(source.instance, http.MethodDelete, "/artifactory/"+source.repository+"/release/gone.bin", "", http.StatusNoContent); err != nil {
			return err
		}
	}
	for _, key := range []string{"p11-provider-local", "p11-provider-remote"} {
		if err := w.executeAndWaitReplicationPath(key, "release"); err != nil {
			return err
		}
	}
	for _, destination := range []struct {
		instance   *world
		repository string
	}{{peer, "provider-target"}, {w, "p11-provider-remote-cache"}} {
		if _, err := replicationFixtureRequest(
			destination.instance,
			http.MethodGet,
			"/artifactory/"+destination.repository+"/release/gone.bin",
			"",
			http.StatusNotFound,
		); err != nil {
			return err
		}
	}
	if _, err := tf("plan", "-detailed-exitcode", "-no-color"); err != nil {
		return err
	}
	if _, err := tf("destroy", "-auto-approve", "-no-color"); err != nil {
		return err
	}
	for _, key := range []string{"p11-provider-local", "p11-provider-remote"} {
		if _, err := replicationFixtureRequest(w, http.MethodGet, "/artifactory/api/replications/"+key, "", http.StatusNotFound); err != nil {
			return err
		}
	}
	w.t.Log("Replication provider proof: Terraform1.17.1, jfrog/artifactory12.11.14, apply/empty-plan/update/execute/verified bytes/deletes/destroy PASS")
	return nil
}

func prepareReplicationProviderWorkspace(
	work, scenario string,
	env map[string]string,
	run func(string, ...string) ([]byte, error),
) (string, error) {
	entries, err := os.ReadDir(work)
	if err != nil {
		return "", fmt.Errorf("inspect provider workspace before bootstrap: %w", err)
	}
	previous := make(map[string]bool, len(entries))
	for _, entry := range entries {
		previous[entry.Name()] = true
	}
	output, err := run("bash", filepath.Join(repoRoot, "test/terraform/run.sh"), "validate", scenario)
	if err != nil {
		return "", err
	}
	dir, config, err := replicationProviderWorkspace(work, scenario, output, previous)
	if err != nil {
		return "", err
	}
	// The driver retains a fresh run.* child, not the requested parent. Both
	// continuation paths must use that same initialized workspace; provider.log
	// and sanitized evidence deliberately stay in the parent.
	env["TF_CLI_CONFIG_FILE"] = config
	return dir, nil
}

func replicationProviderWorkspace(work, scenario string, output []byte, previous map[string]bool) (string, string, error) {
	invalid := fmt.Errorf("provider bootstrap did not report one fresh private workspace")
	const prefix = "Terraform provider workspace: "
	var reported string
	var seen bool
	for _, line := range strings.Split(string(output), "\n") {
		if path, found := strings.CutPrefix(line, prefix); found {
			if seen {
				return "", "", invalid
			}
			seen = true
			reported = strings.TrimSuffix(path, "\r")
		}
	}
	name := filepath.Base(reported)
	if !filepath.IsAbs(reported) || !strings.HasPrefix(name, "run.") || name == "run." || previous[name] {
		return "", "", invalid
	}
	info, err := os.Lstat(reported)
	if err != nil || !info.IsDir() || info.Mode().Perm()&0o077 != 0 {
		return "", "", invalid
	}
	parent, err := filepath.EvalSymlinks(work)
	if err != nil {
		return "", "", invalid
	}
	actual, err := filepath.EvalSymlinks(reported)
	if err != nil || filepath.Dir(actual) != parent {
		return "", "", invalid
	}
	dir := filepath.Join(actual, "scenarios", scenario)
	config := filepath.Join(actual, "terraformrc")
	for path, directory := range map[string]bool{dir: true, config: false} {
		resolved, err := filepath.EvalSymlinks(path)
		if err != nil || resolved != path {
			return "", "", invalid
		}
		info, err := os.Stat(path)
		if err != nil || (directory && !info.IsDir()) || (!directory && !info.Mode().IsRegular()) {
			return "", "", invalid
		}
	}
	return dir, config, nil
}

func (w *world) replicationProviderCommand(program string, args []string, overrides map[string]string, secrets []string, logPath string) ([]byte, error) {
	ctx, cancel := context.WithTimeout(w.t.Context(), 3*time.Minute)
	defer cancel()
	command := exec.CommandContext(ctx, program, args...)
	command.Dir = repoRoot
	for _, entry := range os.Environ() {
		name, _, _ := strings.Cut(entry, "=")
		if _, exists := overrides[name]; exists {
			continue
		}
		exclude := false
		for _, prefix := range []string{"TF_", "TERRAFORM_", "ARTIFACTORY_", "JFROG_", "ARTIFACTWEAVE_", "AWS_", "AZURE_", "GOOGLE_", "GCP_", "GITHUB_TOKEN", "GH_TOKEN"} {
			exclude = exclude || strings.HasPrefix(name, prefix)
		}
		if !exclude {
			command.Env = append(command.Env, entry)
		}
	}
	for name, value := range overrides {
		command.Env = append(command.Env, name+"="+value)
	}
	output, err := command.CombinedOutput()
	sanitized := replicationSanitized(string(output), secrets)
	file, openErr := os.OpenFile(logPath, os.O_CREATE|os.O_WRONLY|os.O_APPEND, 0o600)
	if openErr != nil {
		return nil, fmt.Errorf("open owned provider evidence: %w", openErr)
	}
	_, writeErr := file.WriteString(sanitized)
	closeErr := file.Close()
	if writeErr != nil || closeErr != nil {
		return nil, fmt.Errorf("write owned provider evidence: %w", errors.Join(writeErr, closeErr))
	}
	if err != nil {
		return nil, fmt.Errorf("replication provider command failed; sanitized evidence %s: %w", logPath, err)
	}
	return output, nil
}

func replicationSanitized(value string, secrets []string) string {
	for _, secret := range secrets {
		value = strings.ReplaceAll(value, secret, "[redacted]")
		encoded, _ := json.Marshal(secret)
		value = strings.ReplaceAll(value, strings.Trim(string(encoded), `"`), "[redacted]")
	}
	return value
}

func (w *world) replicationProviderEvidence(work string, secrets []string) error {
	destination := os.Getenv("ARTIFACTWEAVE_REPLICATION_EVIDENCE_DIR")
	if destination == "" {
		return nil
	}
	backend := "sqlite"
	for _, entry := range w.env {
		if entry == "ARTIFACTWEAVE_DATABASE_DRIVER=postgres" {
			backend = "postgres"
		}
	}
	if err := os.MkdirAll(destination, 0o700); err != nil {
		return fmt.Errorf("create private replication evidence directory: %w", err)
	}
	commands, err := os.ReadFile(filepath.Join(work, "provider.log"))
	if err != nil {
		return fmt.Errorf("read provider command evidence: %w", err)
	}
	application, err := replicationApplicationEvidence()
	if err != nil {
		return err
	}
	for kind, content := range map[string]string{"terraform": string(commands), "source": w.stderr.String(), "peer": w.replicationPeer.stderr.String(), "application": application} {
		path := filepath.Join(destination, "replication-"+backend+"-"+kind+".log")
		if err := os.WriteFile(path, []byte(replicationSanitized(content, secrets)), 0o600); err != nil {
			return fmt.Errorf("retain sanitized replication evidence: %w", err)
		}
	}
	return nil
}
