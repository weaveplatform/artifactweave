package clienttest

import (
	"bytes"
	"encoding/json"
	"os"
	"os/exec"
	"path/filepath"
	"slices"
	"strings"
	"testing"
)

const probeEnv = "ARTIFACTWEAVE_CLIENT_ISOLATION_PROBE"

func TestUnitClientProcessProbe(t *testing.T) {
	if os.Getenv(probeEnv) != "1" {
		return
	}
	cwd, err := os.Getwd()
	if err != nil {
		t.Fatal(err)
	}
	values := map[string]string{"cwd": cwd}
	for _, key := range []string{"JFROG_CLI_HOME_DIR", "JFROG_CLI_DEPENDENCIES_DIR", "JFROG_CLI_TEMP_DIR", "JFROG_CLI_REPORT_USAGE", "JFROG_CLI_AVOID_NEW_VERSION_WARNING", "DOCKER_CONFIG", "COSIGN_PASSWORD", "COSIGN_YES"} {
		values[key] = os.Getenv(key)
	}
	if err := json.NewEncoder(os.Stdout).Encode(values); err != nil {
		t.Fatal(err)
	}
	os.Exit(0)
}

// The child reports what the actual process inherited, rather than merely
// checking an environment slice that exec might deduplicate differently.
func TestUnitJFIsolatesDeveloperStateBetweenScenarios(t *testing.T) {
	exe, err := os.Executable()
	if err != nil {
		t.Fatal(err)
	}
	t.Setenv("ARTIFACTWEAVE_JF_BIN", exe)
	t.Setenv("JFROG_CLI_HOME_DIR", "developer-home")
	t.Setenv("JFROG_CLI_DEPENDENCIES_DIR", "developer-dependencies")
	t.Setenv("JFROG_CLI_TEMP_DIR", "developer-temp")
	for _, docker := range []bool{false, true} {
		t.Run(map[bool]string{false: "plain", true: "docker"}[docker], func(t *testing.T) {
			dir, home := t.TempDir(), t.TempDir()
			config := ""
			if docker {
				config = t.TempDir()
			}
			cmd := JF(t.Context(), dir, home, config, []string{probeEnv + "=1"}, []string{"-test.run=^TestUnitClientProcessProbe$"})
			out, err := cmd.Output()
			if err != nil {
				t.Fatal(err)
			}
			var got map[string]string
			if err := json.Unmarshal(out, &got); err != nil {
				t.Fatal(err)
			}
			for key, want := range map[string]string{"cwd": dir, "JFROG_CLI_HOME_DIR": home, "JFROG_CLI_DEPENDENCIES_DIR": filepath.Join(home, "dependencies"), "JFROG_CLI_TEMP_DIR": filepath.Join(home, "tmp"), "JFROG_CLI_REPORT_USAGE": "false", "JFROG_CLI_AVOID_NEW_VERSION_WARNING": "true"} {
				if got[key] != want {
					t.Errorf("child %s=%q, want %q", key, got[key], want)
				}
			}
			if docker && got["DOCKER_CONFIG"] != config {
				t.Fatal("JF Docker integration borrowed another credential directory")
			}
		})
	}
}

func TestUnitContainerClientsKeepToolsAndProductCachesSeparate(t *testing.T) {
	work, tools := t.TempDir(), t.TempDir()
	stdin := bytes.NewBufferString("fixture input")
	plain := Container(t.Context(), work, "pinned-client", nil, nil, []string{"version"})
	interactive := NPM(t.Context(), work, "pinned-node", stdin, []string{"login"})
	maven := Maven(t.Context(), work, "pinned-maven", tools, []string{"-Dmaven.repo.local=/work/fresh", "package"})
	for _, cmd := range []*exec.Cmd{plain, interactive, maven} {
		args := strings.Join(cmd.Args, " ")
		for _, required := range []string{"--network host", "--user ", work + ":/work", "HOME=/work"} {
			if !strings.Contains(args, required) {
				t.Errorf("missing client isolation %q", required)
			}
		}
	}
	if interactive.Stdin != stdin || !slices.Contains(interactive.Args, "-i") {
		t.Fatal("interactive client lost its input")
	}
	for _, setting := range []string{"npm_config_cache=/work/.npm-cache", "npm_config_audit=false", "npm_config_fund=false", "npm_config_update_notifier=false"} {
		if !slices.Contains(interactive.Args, setting) {
			t.Errorf("npm side-trip/cache setting missing: %s", setting)
		}
	}
	if !slices.Contains(maven.Args, tools+":/m2:ro") || !slices.Contains(maven.Args, "-Dmaven.repo.local.tail=/m2") ||
		!slices.Contains(maven.Args, "-Dmaven.repo.local=/work/fresh") {
		t.Fatal("Maven shared tools or cold product head is writable/incorrect")
	}
	original := []string{"-Dmaven.repo.local=/m2"}
	args := MavenArgs(original)
	if !slices.Contains(args, "-Dmaven.repo.local=/work/maven-publisher") || original[0] != "-Dmaven.repo.local=/m2" {
		t.Fatal("publisher cache overlapped tools or mutated caller arguments")
	}
}

func TestUnitRegistryClientsUsePrivateCredentials(t *testing.T) {
	exe, err := os.Executable()
	if err != nil {
		t.Fatal(err)
	}
	dir, config := t.TempDir(), t.TempDir()
	cmd := Registry(t.Context(), dir, config, exe, []string{"-test.run=^TestUnitClientProcessProbe$"})
	cmd.Env = append(cmd.Env, probeEnv+"=1")
	out, err := cmd.Output()
	if err != nil {
		t.Fatal(err)
	}
	var got map[string]string
	if err := json.Unmarshal(out, &got); err != nil {
		t.Fatal(err)
	}
	if got["DOCKER_CONFIG"] != config || got["cwd"] != dir || got["COSIGN_PASSWORD"] != "" || got["COSIGN_YES"] != "true" {
		t.Fatal("registry tool borrowed developer credentials or interactive settings")
	}
	docker := Docker(t.Context(), dir, config, []string{"pull", "fixture"})
	if docker.Dir != dir || !slices.Equal(docker.Args[1:], []string{"--config", config, "pull", "fixture"}) {
		t.Fatal("Docker did not receive the private registry credentials")
	}
}
