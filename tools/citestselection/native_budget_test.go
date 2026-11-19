package main

import (
	"context"
	"os"
	"os/exec"
	"path/filepath"
	"runtime"
	"strings"
	"testing"
	"time"

	"gopkg.in/yaml.v3"
)

// The process stays alive until the monitor advances its independent clock.
// No test sleeps for the simulated 27m image preparation or 25m acceptance.
const nativeBudgetMakeScript = `#!/bin/sh
set -eu
printf '%s\n' "$*" > "$OBSERVED/arguments"
printf started > "$OBSERVED/started"
if [ "$MODE" = accept-failure ]; then exit 23; fi
i=0
while [ ! -f "$OBSERVED/release" ]; do
  i=$((i + 1))
  if [ "$i" -ge 2000 ]; then echo 'native fixture was not released' >&2; exit 93; fi
  /bin/sleep 0.001
done
printf finished > "$OBSERVED/finished"
`

const nativeBudgetSleepScript = `#!/bin/sh
set -eu
if [ ! -f "$CLIENT_IMAGES_STATUS" ]; then
  if [ "$MODE" = image-timeout ]; then
    printf 2800 > "$MONITOR_CLOCK"
  else
    printf 1600 > "$MONITOR_CLOCK"
    if [ "$MODE" = image-failure ]; then
      printf 17 > "$CLIENT_IMAGES_STATUS"
    else
      printf 0 > "$CLIENT_IMAGES_STATUS"
    fi
  fi
  exit
fi
i=0
while [ ! -f "$OBSERVED/started" ]; do
  i=$((i + 1))
  if [ "$i" -ge 2000 ]; then echo 'native fixture did not start' >&2; exit 91; fi
  /bin/sleep 0.001
done
if [ "$MODE" = acceptance-timeout ]; then
  printf 4400 > "$MONITOR_CLOCK"
elif [ "$(cat "$MONITOR_CLOCK")" = 1600 ]; then
  printf 2800 > "$MONITOR_CLOCK"
else
  printf 3100 > "$MONITOR_CLOCK"
  touch "$OBSERVED/release"
  i=0
  while [ ! -f "$OBSERVED/finished" ]; do
    i=$((i + 1))
    if [ "$i" -ge 2000 ]; then echo 'native fixture did not finish' >&2; exit 92; fi
    /bin/sleep 0.001
  done
fi
`

func TestUnitNativeAcceptanceHasItsOwnBudgetAfterImagePreparation(t *testing.T) {
	if runtime.GOOS != "linux" {
		t.Skip("the native Actions monitor requires Linux setsid")
	}
	data, err := os.ReadFile("../../.github/workflows/quality-gate.yml")
	if err != nil {
		t.Fatal(err)
	}
	var workflow struct {
		Jobs map[string]struct {
			Steps []struct {
				Name string `yaml:"name"`
				Run  string `yaml:"run"`
			} `yaml:"steps"`
		} `yaml:"jobs"`
	}
	if err := yaml.Unmarshal(data, &workflow); err != nil {
		t.Fatal(err)
	}
	monitor := ""
	for _, step := range workflow.Jobs["acceptance"].Steps {
		if step.Name == "Acceptance features (coverage-instrumented binary)" {
			monitor = step.Run
		}
	}
	if monitor == "" || !strings.Contains(monitor, "SECONDS") {
		t.Fatal("the actual native acceptance monitor is missing")
	}
	// Substitute only the clock, retaining the actual workflow's process,
	// status, deadline and exit handling and its real 35m Go test argument.
	monitor = strings.ReplaceAll(monitor, "SECONDS", `$(cat "$MONITOR_CLOCK")`)
	for _, mode := range []string{"delayed-success", "image-timeout", "acceptance-timeout"} {
		t.Run(mode, func(t *testing.T) {
			observed, bin := t.TempDir(), t.TempDir()
			t.Cleanup(func() {
				// A context failure must also release the detached fixture,
				// which otherwise keeps the command's output pipe open.
				if err := os.WriteFile(filepath.Join(observed, "release"), nil, 0o600); err != nil {
					t.Errorf("release native fixture: %v", err)
				}
			})
			clock := filepath.Join(observed, "clock")
			status := filepath.Join(observed, "images.status")
			if err := os.WriteFile(clock, []byte("0"), 0o600); err != nil {
				t.Fatal(err)
			}
			if err := os.WriteFile(filepath.Join(observed, "client-images.log"), nil, 0o600); err != nil {
				t.Fatal(err)
			}
			for name, script := range map[string]string{"make": nativeBudgetMakeScript, "sleep": nativeBudgetSleepScript} {
				if err := os.WriteFile(filepath.Join(bin, name), []byte(script), 0o700); err != nil {
					t.Fatal(err)
				}
			}
			ctx, cancel := context.WithTimeout(t.Context(), 10*time.Second)
			defer cancel()
			cmd := exec.CommandContext(ctx, "bash", "-ec", monitor)
			cmd.WaitDelay = time.Second
			cmd.Env = append(os.Environ(), "PROFILE=full", "MODE="+mode,
				"CLIENT_IMAGES_STATUS="+status, "RUNNER_TEMP="+observed,
				"OBSERVED="+observed, "MONITOR_CLOCK="+clock,
				"PATH="+bin+string(os.PathListSeparator)+os.Getenv("PATH"))
			output, err := cmd.CombinedOutput()
			if ctx.Err() != nil {
				t.Fatalf("actual monitor did not stop: %v\n%s", err, output)
			}
			if mode != "delayed-success" {
				if err == nil || !strings.Contains(string(output), "Timed out waiting") {
					t.Fatalf("phase watchdog did not reject %s: %v\n%s", mode, err, output)
				}
				return
			}
			if err != nil {
				t.Fatalf("image preparation spent the acceptance budget: %v\n%s", err, output)
			}
			if _, err := os.Stat(filepath.Join(observed, "finished")); err != nil {
				t.Fatalf("native acceptance did not complete: %v", err)
			}
			arguments, err := os.ReadFile(filepath.Join(observed, "arguments"))
			if err != nil || strings.TrimSpace(string(arguments)) != "accept ACCEPT_TIMEOUT=35m" {
				t.Fatalf("configured native test budget changed: %q %v", arguments, err)
			}
		})
	}
}
