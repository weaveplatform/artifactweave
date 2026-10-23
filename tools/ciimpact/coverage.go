package main

import (
	"bufio"
	"fmt"
	"io"
	"os"
	"os/exec"
	"path"
	"strconv"
	"strings"
)

type coverageCount struct{ covered, total int64 }

// A partial PR checks exact statement counts for production packages changed
// on this PR. The release profile separately enforces whole-project coverage.
func checkAffectedCoverage(dir, packages string) error {
	profile, err := os.CreateTemp("", "artifactweave-affected-coverage-*.out")
	if err != nil {
		return err
	}
	defer os.Remove(profile.Name())
	if err := profile.Close(); err != nil {
		return err
	}
	cmd := exec.Command("go", "tool", "covdata", "textfmt", "-i="+dir, "-o="+profile.Name())
	if out, err := cmd.CombinedOutput(); err != nil {
		return fmt.Errorf("read same-run coverage: %w: %s", err, out)
	}
	f, err := os.Open(profile.Name())
	if err != nil {
		return err
	}
	defer f.Close()
	counts, err := parseCoverageProfile(f)
	if err != nil {
		return err
	}
	if len(counts) == 0 {
		return fmt.Errorf("no package coverage in %s", dir)
	}
	for _, pkg := range strings.Split(packages, ",") {
		if pkg == "" {
			continue
		}
		count, ok := counts[pkg]
		if !ok || count.total == 0 {
			return fmt.Errorf("changed package %s is missing from same-run coverage", pkg)
		}
		fmt.Printf(
			"%s: %d/%d statements (affected-package floor 90%%)\n",
			pkg,
			count.covered,
			count.total,
		)
		if count.covered*100 < count.total*90 {
			return fmt.Errorf("changed package %s is below 90%% statement coverage", pkg)
		}
	}
	return nil
}

func parseCoverageProfile(r io.Reader) (map[string]coverageCount, error) {
	counts := map[string]coverageCount{}
	scan := bufio.NewScanner(r)
	for scan.Scan() {
		line := scan.Text()
		if line == "mode: atomic" || line == "mode: count" || line == "mode: set" {
			continue
		}
		file, detail, ok := strings.Cut(line, ":")
		if !ok || !strings.HasSuffix(file, ".go") {
			return nil, fmt.Errorf("invalid coverage row %q", line)
		}
		fields := strings.Fields(detail)
		if len(fields) != 3 {
			return nil, fmt.Errorf("invalid coverage counts %q", line)
		}
		statements, err := strconv.ParseInt(fields[1], 10, 64)
		if err != nil || statements < 0 {
			return nil, fmt.Errorf("invalid statement count %q", line)
		}
		hits, err := strconv.ParseInt(fields[2], 10, 64)
		if err != nil || hits < 0 {
			return nil, fmt.Errorf("invalid hit count %q", line)
		}
		pkg := path.Dir(file)
		count := counts[pkg]
		count.total += statements
		if hits > 0 {
			count.covered += statements
		}
		counts[pkg] = count
	}
	return counts, scan.Err()
}
