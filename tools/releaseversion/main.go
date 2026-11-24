package main

import (
	"bufio"
	"encoding/json"
	"errors"
	"flag"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"regexp"
	"strings"
)

const (
	manifestFile  = ".release-please-manifest.json"
	configFile    = "release-please-config.json"
	changelogFile = "CHANGELOG.md"
	versionMarker = "x-release-please-version"
)

// semver is the version form release-please writes: no leading v, an
// optional pre-release suffix.
const semver = `(\d+\.\d+\.\d+(?:-[0-9A-Za-z.-]+)?)`

var (
	// release-please titles a manifest-mode release pull request with
	// "chore(<branch>): release <version>"; include-component-in-tag is false,
	// so no component appears.
	titlePattern = regexp.MustCompile(`^chore\(main\): release ` + semver + `$`)
	tagPattern   = regexp.MustCompile(`^v` + semver + `$`)
	// The newest CHANGELOG heading: "## [0.4.0](.../compare/v0.3.0...v0.4.0)
	// (date)", or "## 0.1.0 (date)" for a first release without a compare link.
	headingPattern = regexp.MustCompile(`^## \[?` + semver + `\]?(?:\(([^)]*)\))?`)
	compareHead    = regexp.MustCompile(`\.\.\.v` + semver + `$`)
	markerVersion  = regexp.MustCompile(`v?\d+\.\d+\.\d+(?:-[0-9A-Za-z.-]+)?`)
)

// advice is appended to every mismatch: the fix is never to edit the files
// or the title by hand, because release-please rewrites both from its own
// computation on the next push to main.
const advice = "release-please tags the version in the merged pull request's title " +
	"and commits the version it computed; to release another version, push a commit " +
	"to main with a \"Release-As: x.y.z\" footer and let release-please rewrite the " +
	"pull request, rather than editing its title"

func main() {
	root := flag.String("root", ".", "repository root")
	title := flag.String("title", "", "release-please pull request title")
	tag := flag.String("tag", "", "release tag, such as v0.4.0")
	flag.Parse()
	if err := run(os.Stdout, *root, *title, *tag); err != nil {
		fmt.Fprintln(os.Stderr, err)
		os.Exit(1)
	}
}

func run(out io.Writer, root, title, tag string) error {
	version, source, err := wanted(title, tag)
	if err != nil {
		return err
	}
	if problems := check(root, version); len(problems) > 0 {
		return fmt.Errorf("%s says %s, but:\n  - %s\n%s",
			source, version, strings.Join(problems, "\n  - "), advice)
	}
	_, err = fmt.Fprintf(out, "%s, the manifest, CHANGELOG and version markers all say %s\n", source, version)
	return err
}

// wanted returns the version under check and a name for where it came from.
func wanted(title, tag string) (version, source string, err error) {
	switch {
	case (title == "") == (tag == ""):
		return "", "", errors.New("give exactly one of -title and -tag")
	case title != "":
		m := titlePattern.FindStringSubmatch(title)
		if m == nil {
			// release-please parses the version out of this title when the pull
			// request merges; a title it cannot parse creates no release at all.
			return "", "", fmt.Errorf("title %q is not \"chore(main): release <version>\"; %s", title, advice)
		}
		return m[1], "the pull request title", nil
	default:
		m := tagPattern.FindStringSubmatch(tag)
		if m == nil {
			return "", "", fmt.Errorf("tag %q is not v<version>", tag)
		}
		return m[1], "tag " + tag, nil
	}
}

// check returns every place below root whose version differs from version.
func check(root, version string) []string {
	var problems []string
	if got, err := manifestVersion(root); err != nil {
		problems = append(problems, err.Error())
	} else if got != version {
		problems = append(problems, fmt.Sprintf("%s says %s", manifestFile, got))
	}
	if got, head, err := changelogVersion(root); err != nil {
		problems = append(problems, err.Error())
	} else {
		if got != version {
			problems = append(problems, fmt.Sprintf("%s's newest heading says %s", changelogFile, got))
		}
		if head != "" && head != version {
			problems = append(problems, fmt.Sprintf("%s's newest compare link ends at v%s", changelogFile, head))
		}
	}
	marked, err := markerProblems(root, version)
	if err != nil {
		problems = append(problems, err.Error())
	}
	return append(problems, marked...)
}

func manifestVersion(root string) (string, error) {
	data, err := os.ReadFile(filepath.Join(root, manifestFile))
	if err != nil {
		return "", err
	}
	var manifest map[string]string
	if err := json.Unmarshal(data, &manifest); err != nil {
		return "", fmt.Errorf("%s: %w", manifestFile, err)
	}
	v, ok := manifest["."]
	if !ok {
		return "", fmt.Errorf(`%s has no "." package`, manifestFile)
	}
	return v, nil
}

// changelogVersion returns the version of CHANGELOG's newest heading and the
// version its compare link ends at, empty when it has no link.
func changelogVersion(root string) (version, compare string, err error) {
	f, err := os.Open(filepath.Join(root, changelogFile))
	if err != nil {
		return "", "", err
	}
	defer f.Close()
	s := bufio.NewScanner(f)
	for s.Scan() {
		line := s.Text()
		if !strings.HasPrefix(line, "## ") {
			continue
		}
		m := headingPattern.FindStringSubmatch(line)
		if m == nil {
			return "", "", fmt.Errorf("%s's newest heading %q names no version", changelogFile, line)
		}
		if c := compareHead.FindStringSubmatch(m[2]); c != nil {
			compare = c[1]
		}
		return m[1], compare, nil
	}
	if err := s.Err(); err != nil {
		return "", "", err
	}
	return "", "", fmt.Errorf("%s has no release heading", changelogFile)
}

// markerProblems checks every line marked x-release-please-version in the
// extra-files that release-please-config.json lists: release-please's generic
// updater rewrites each version on such a line, so each must read version.
func markerProblems(root, version string) ([]string, error) {
	data, err := os.ReadFile(filepath.Join(root, configFile))
	if err != nil {
		return nil, err
	}
	var config struct {
		Packages map[string]struct {
			ExtraFiles []json.RawMessage `json:"extra-files"`
		} `json:"packages"`
	}
	if err := json.Unmarshal(data, &config); err != nil {
		return nil, fmt.Errorf("%s: %w", configFile, err)
	}
	var problems []string
	for _, raw := range config.Packages["."].ExtraFiles {
		path, err := extraFilePath(raw)
		if err != nil {
			return problems, fmt.Errorf("%s: %w", configFile, err)
		}
		content, err := os.ReadFile(filepath.Join(root, filepath.FromSlash(path)))
		if err != nil {
			// Name the file as the config does: the OS error carries a native
			// path, which on Windows has no forward slashes to match.
			problems = append(problems, fmt.Sprintf("%s, listed in extra-files: %v", path, err))
			continue
		}
		for i, line := range strings.Split(string(content), "\n") {
			if !strings.Contains(line, versionMarker) {
				continue
			}
			for _, v := range markerVersion.FindAllString(line, -1) {
				if strings.TrimPrefix(v, "v") != version {
					problems = append(problems, fmt.Sprintf("%s:%d says %s", path, i+1, v))
				}
			}
		}
	}
	return problems, nil
}

// extraFilePath reads an extra-files entry, which release-please accepts as a
// bare path or as an object with a path.
func extraFilePath(raw json.RawMessage) (string, error) {
	var path string
	if err := json.Unmarshal(raw, &path); err == nil {
		return path, nil
	}
	var entry struct {
		Path string `json:"path"`
	}
	if err := json.Unmarshal(raw, &entry); err != nil || entry.Path == "" {
		return "", fmt.Errorf("extra-files entry %s has no path", raw)
	}
	return entry.Path, nil
}
