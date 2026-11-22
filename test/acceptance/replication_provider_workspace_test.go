package acceptance

import (
	"encoding/json"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

func TestUnitReplicationProviderFollowsDriverWorkspace(t *testing.T) {
	work := t.TempDir()
	bin := filepath.Join(work, "bin")
	if err := os.Mkdir(bin, 0o700); err != nil {
		t.Fatal(err)
	}
	write := func(path, value string) {
		t.Helper()
		if err := os.WriteFile(path, []byte(value), 0o700); err != nil {
			t.Fatal(err)
		}
	}
	write(filepath.Join(bin, "go"), "#!/bin/sh\ncase $2 in GOOS) echo darwin;; GOARCH) echo arm64;; *) exit 1;; esac\n")
	program := filepath.Join(bin, "terraform")
	write(program, `#!/usr/bin/env python3
import json, os, pathlib, sys
args = sys.argv[1:]
directory = pathlib.Path(args.pop(0).split("=", 1)[1])
if not directory.is_dir():
    sys.exit("Error handling -chdir option: no such directory")
config = pathlib.Path(os.environ["TF_CLI_CONFIG_FILE"])
assert "filesystem_mirror" in config.read_text(), "caller did not use initialized driver configuration"
if args[0] == "init": (directory / ".terraform").mkdir(exist_ok=True)
if args[0] == "version":
    print(json.dumps({"directory": str(directory), "config": str(config)}))
`)
	config := filepath.Join(work, "terraformrc")
	write(config, "")
	log := filepath.Join(work, "provider.log")
	write(log, "retained parent evidence\n")
	env := map[string]string{
		"PATH":           filepath.Dir(program) + string(os.PathListSeparator) + os.Getenv("PATH"),
		"TERRAFORM_WORK": work, "TERRAFORM_PROVIDER_MIRROR": filepath.Join(work, "mirror"),
		"TF_CLI_CONFIG_FILE": config, "TF_IN_AUTOMATION": "1", "TF_INPUT": "0",
	}
	w := &world{t: t}
	run := func(command string, args ...string) ([]byte, error) {
		return w.replicationProviderCommand(command, args, env, nil, log)
	}
	const scenario = "deploy/artifactweave/terraform/single-replication"
	first, err := prepareReplicationProviderWorkspace(work, scenario, env, run)
	if err != nil {
		t.Fatal(err)
	}
	version, err := run(program, "-chdir="+first, "version", "-json")
	if err != nil {
		data, _ := os.ReadFile(log)
		for _, line := range strings.Split(string(data), "\n") {
			if strings.HasPrefix(line, "Error handling -chdir option:") {
				t.Fatalf("caller continuation after actual driver validation: %v: %s", err, line)
			}
		}
		t.Fatalf("caller continuation after actual driver validation: %v", err)
	}
	var got struct{ Directory, Config string }
	if err := json.Unmarshal(version, &got); err != nil {
		t.Fatal(err)
	}
	if got.Directory != first || got.Config != env["TF_CLI_CONFIG_FILE"] || filepath.Dir(got.Config) == work {
		t.Fatal("caller did not consume the effective private scenario/configuration")
	}
	state := filepath.Join(first, "terraform.tfstate")
	write(state, "previous run state")
	second, err := prepareReplicationProviderWorkspace(work, scenario, env, run)
	if err != nil {
		t.Fatal(err)
	}
	if first == second {
		t.Fatal("caller resumed a prior run's private state")
	}
	if _, err := run(program, "-chdir="+second, "version", "-json"); err != nil {
		t.Fatal(err)
	}
	if data, err := os.ReadFile(state); err != nil || string(data) != "previous run state" {
		t.Fatal("later bootstrap changed earlier state")
	}
	if data, err := os.ReadFile(log); err != nil || !strings.HasPrefix(string(data), "retained parent evidence\n") {
		t.Fatal("caller did not preserve parent provider evidence")
	}
}

func TestUnitReplicationProviderRejectsInvalidWorkspaceOutput(t *testing.T) {
	work := t.TempDir()
	const scenario = "deploy/artifactweave/terraform/single-replication"
	fresh := filepath.Join(work, "run.fresh")
	dir := filepath.Join(fresh, "scenarios", scenario)
	if err := os.MkdirAll(dir, 0o700); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(fresh, "terraformrc"), []byte("private configuration"), 0o600); err != nil {
		t.Fatal(err)
	}
	const prefix = "Terraform provider workspace: "
	outside := filepath.Join(t.TempDir(), "run.outside")
	if err := os.Mkdir(outside, 0o700); err != nil {
		t.Fatal(err)
	}
	alias := filepath.Join(work, "run.alias")
	if err := os.Symlink(fresh, alias); err != nil {
		t.Fatal(err)
	}
	for _, tc := range []struct {
		name, output string
		previous     map[string]bool
	}{
		{name: "missing output", output: "Terraform validated\n"},
		{name: "duplicate output", output: prefix + fresh + "\n" + prefix + fresh + "\n"},
		{name: "empty duplicate output", output: prefix + "\n" + prefix + fresh + "\n"},
		{name: "outside parent", output: prefix + outside + "\n"},
		{name: "stale prior run", output: prefix + fresh + "\n", previous: map[string]bool{"run.fresh": true}},
		{name: "symlink run", output: prefix + alias + "\n"},
		{name: "parent instead of child", output: prefix + work + "\n"},
	} {
		t.Run(tc.name, func(t *testing.T) {
			if _, _, err := replicationProviderWorkspace(work, scenario, []byte(tc.output), tc.previous); err == nil {
				t.Fatal("unowned, ambiguous or stale workspace accepted")
			}
		})
	}
	if err := os.Remove(filepath.Join(fresh, "terraformrc")); err != nil {
		t.Fatal(err)
	}
	if _, _, err := replicationProviderWorkspace(work, scenario, []byte(prefix+fresh+"\n"), nil); err == nil {
		t.Fatal("workspace without initialized CLI configuration accepted")
	}
}
