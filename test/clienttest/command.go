// Package clienttest supplies the pinned clients' process and cache isolation
// shared by single-instance acceptance and multi-instance qualification.
package clienttest

import (
	"context"
	"fmt"
	"io"
	"os"
	"os/exec"
	"path/filepath"
)

// Container runs an image against private files, preserving loopback URLs and
// the runner's ownership of files written by the client.
func Container(ctx context.Context, dir, image string, stdin io.Reader, flags, argv []string) *exec.Cmd {
	args := []string{
		"run", "--rm", "--network", "host", "--user", fmt.Sprintf("%d:%d", os.Getuid(), os.Getgid()),
		"-v", dir + ":/work", "-w", "/work", "-e", "HOME=/work",
	}
	if stdin != nil {
		args = append(args, "-i")
	}
	args = append(args, flags...)
	args = append(args, image)
	args = append(args, argv...)
	cmd := exec.CommandContext(ctx, "docker", args...)
	cmd.Stdin = stdin
	return cmd
}

// NPM disables network side trips and keeps the package cache private.
func NPM(ctx context.Context, dir, image string, stdin io.Reader, argv []string) *exec.Cmd {
	return Container(ctx, dir, image, stdin, []string{
		"--entrypoint", "npm",
		"-e", "npm_config_cache=/work/.npm-cache", "-e", "npm_config_update_notifier=false",
		"-e", "npm_config_audit=false", "-e", "npm_config_fund=false",
	}, argv)
}

// Maven mounts only verified tools as a read-only tail, separate from product
// heads. The caller supplies a fresh head for each independent resolution.
func Maven(ctx context.Context, dir, image, tools string, argv []string) *exec.Cmd {
	return Container(ctx, dir, image, nil, []string{
		"--entrypoint", "mvn", "-v", tools + ":/m2:ro",
		"-e", "MAVEN_OPTS=-Duser.home=/work",
	}, MavenArgs(argv))
}

// MavenArgs preserves the acceptance harness's publisher head and tool tail.
func MavenArgs(argv []string) []string {
	args := append([]string(nil), argv...)
	for i, arg := range args {
		if arg == "-Dmaven.repo.local=/m2" {
			args[i] = "-Dmaven.repo.local=/work/maven-publisher"
		}
	}
	return append([]string{
		"-B", "--no-transfer-progress", "--global-settings", "/work/maven-tools-settings.xml",
		"-Dmaven.repo.local=/work/maven-publisher", "-Dmaven.repo.local.tail=/m2",
	}, args...)
}

// JF isolates configuration, dependencies and build information between tests.
func JF(ctx context.Context, dir, home, dockerConfig string, env, argv []string) *exec.Cmd {
	cmd := exec.CommandContext(ctx, os.Getenv("ARTIFACTWEAVE_JF_BIN"), argv...)
	cmd.Dir = dir
	cmd.Env = append(os.Environ(), "JFROG_CLI_HOME_DIR="+home,
		"JFROG_CLI_DEPENDENCIES_DIR="+filepath.Join(home, "dependencies"),
		"JFROG_CLI_TEMP_DIR="+filepath.Join(home, "tmp"), "JFROG_CLI_REPORT_USAGE=false",
		"JFROG_CLI_AVOID_NEW_VERSION_WARNING=true", "CI=true")
	if dockerConfig != "" {
		cmd.Env = append(cmd.Env, "DOCKER_CONFIG="+dockerConfig)
	}
	cmd.Env = append(cmd.Env, env...)
	return cmd
}

// Docker uses a private registry credential directory, shared with JF's Docker
// integration when the caller supplies the same directory.
func Docker(ctx context.Context, dir, config string, argv []string) *exec.Cmd {
	cmd := exec.CommandContext(ctx, "docker", append([]string{"--config", config}, argv...)...)
	cmd.Dir = dir
	return cmd
}
