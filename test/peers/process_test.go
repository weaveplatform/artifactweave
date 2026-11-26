package peers

import (
	"bytes"
	"fmt"
	"io"
	"os"
	"os/exec"
	"path/filepath"
	"strconv"
	"strings"
	"testing"
)

const coverageProbe = "ARTIFACTWEAVE_PEER_COVERAGE_PROBE"

func TestAccCoverageProcessProbe(t *testing.T) {
	if os.Getenv(coverageProbe) == "" {
		return
	}
	RunInstance(t)
	if _, err := io.Copy(io.Discard, os.Stdin); err != nil {
		t.Fatal(err)
	}
	if os.Getenv(coverageProbe) == "nested" {
		cmd, collect, err := TestCommand(t.Context(), "TestAccCoverageProcessProbe", coverageProbe+"=leaf")
		if err != nil {
			t.Fatal(err)
		}
		out, exit := cmd.CombinedOutput()
		collected := collect()
		if exit != nil || collected != nil {
			t.Fatalf("nested child: %v; coverage: %v; %s", exit, collected, out)
		}
	}
}

func TestAccCoverageProcessIsolation(t *testing.T) {
	if testing.CoverMode() == "" {
		t.Skip("needs coverage instrumentation")
	}
	parent := t.TempDir()
	type child struct {
		cmd     *exec.Cmd
		collect func() error
		input   io.WriteCloser
		output  bytes.Buffer
	}
	children := make([]child, 3)
	dirs := map[string]bool{}
	for i := range children {
		c := &children[i]
		var err error
		probe := "leaf"
		if i == len(children)-1 {
			probe = "nested"
		}
		c.cmd, c.collect, err = testCommand(t.Context(), "TestAccCoverageProcessProbe", parent, coverageProbe+"="+probe)
		if err != nil {
			t.Fatal(err)
		}
		var dir string
		for _, arg := range c.cmd.Args {
			if value, ok := strings.CutPrefix(arg, "-test.gocoverdir="); ok {
				dir = value
			}
		}
		if dir == "" || dir == parent || dirs[dir] {
			t.Fatalf("children share metadata destination %q", dir)
		}
		dirs[dir] = true
		entries, err := os.ReadDir(dir)
		if err != nil || len(entries) != 0 {
			t.Fatalf("child directory is not initially empty: %v (%v)", entries, err)
		}
		c.input, err = c.cmd.StdinPipe()
		if err != nil {
			t.Fatal(err)
		}
		c.cmd.Stdout, c.cmd.Stderr = &c.output, &c.output
		if err := c.cmd.Start(); err != nil {
			t.Fatal(err)
		}
		t.Cleanup(func() {
			if c.cmd.ProcessState == nil {
				_ = c.cmd.Process.Kill()
				_ = c.cmd.Wait()
			}
		})
	}
	results := make(chan error, len(children))
	for i := range children {
		go func(c *child) {
			if err := c.input.Close(); err != nil {
				results <- err
				return
			}
			if err := c.cmd.Wait(); err != nil {
				results <- fmt.Errorf("child: %w: %s", err, &c.output)
				return
			}
			results <- c.collect()
		}(&children[i])
	}
	for range children {
		if err := <-results; err != nil {
			t.Error(err)
		}
	}
	if t.Failed() {
		return
	}
	entries, err := os.ReadDir(parent)
	if err != nil {
		t.Fatal(err)
	}
	for _, c := range children {
		count := 0
		for _, entry := range entries {
			if strings.HasPrefix(entry.Name(), "covcounters.") && strings.Contains(entry.Name(), "."+strconv.Itoa(c.cmd.Process.Pid)+".") {
				count++
			}
		}
		if count != 1 {
			t.Fatalf("child %d has %d counter contributions, want 1", c.cmd.Process.Pid, count)
		}
	}
	const counters = 4
	if len(entries) != counters+1 {
		t.Fatalf("collected %d files, want one metadata file and %d counters including the nested child", len(entries), counters)
	}
	merged := t.TempDir()
	output, err := exec.CommandContext(t.Context(), "go", "tool", "covdata", "merge", "-i="+parent, "-o="+merged).CombinedOutput()
	if err != nil {
		t.Fatalf("merge child coverage: %v: %s", err, output)
	}
	profile := filepath.Join(t.TempDir(), "coverage.out")
	out, err := exec.CommandContext(t.Context(), "go", "tool", "covdata", "textfmt", "-i="+merged, "-o="+profile).CombinedOutput()
	if err != nil {
		t.Fatalf("read collected coverage: %v: %s", err, out)
	}
	data, err := os.ReadFile(profile)
	if err != nil {
		t.Fatal(err)
	}
	want := counters
	if testing.CoverMode() == "set" {
		want = 1
	}
	for _, line := range strings.Split(string(data), "\n") {
		fields := strings.Fields(line)
		if len(fields) == 3 && strings.Contains(fields[0], "test/peers/process.go:") && fields[2] == strconv.Itoa(want) {
			return
		}
	}
	t.Fatalf("merged profile lacks the %d child contributions in peer code:\n%s", want, data)
}
