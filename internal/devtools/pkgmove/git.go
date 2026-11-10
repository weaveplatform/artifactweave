package pkgmove

import (
	"bytes"
	"context"
	"errors"
	"fmt"
	"os/exec"
	"strconv"
	"strings"
)

// git runs git in the repository root. Every command that lists paths asks
// for NUL-separated output, so a path is never quoted or split.
type git struct {
	ctx  context.Context
	root string
}

// run runs git with args and returns its standard output. The error carries
// git's standard error, which says what went wrong.
func (g git) run(args ...string) ([]byte, error) {
	var stdout, stderr bytes.Buffer
	cmd := exec.CommandContext(g.ctx, "git", args...)
	cmd.Dir = g.root
	cmd.Stdout = &stdout
	cmd.Stderr = &stderr
	if err := cmd.Run(); err != nil {
		return stdout.Bytes(), fmt.Errorf(
			"git %s: %w: %s",
			strings.Join(args, " "),
			err,
			strings.TrimSpace(stderr.String()),
		)
	}
	return stdout.Bytes(), nil
}

// list runs git with args, which must ask for NUL-separated output, and
// returns the non-empty fields.
func (g git) list(args ...string) ([]string, error) {
	out, err := g.run(args...)
	if err != nil {
		return nil, err
	}
	return splitNUL(out), nil
}

func splitNUL(out []byte) []string {
	var fields []string
	for f := range strings.SplitSeq(string(out), "\x00") {
		if f != "" {
			fields = append(fields, f)
		}
	}
	return fields
}

// grepLine is one line that git grep found.
type grepLine struct {
	file string
	line int
	text string
}

// grepArgs are the options every search shares. -I skips binary files and -F
// takes the patterns as fixed strings. The configuration overrides keep a
// user's grep.column or grep.fullName setting from changing the output that
// parseGrep reads.
var grepArgs = []string{
	"-c", "grep.column=false", "-c", "grep.fullName=false",
	"grep", "-I", "-F", "-z", "--no-color",
}

// grep returns the lines of tracked text files that contain any of patterns,
// leaving out the files that match the exclude globs. It returns nothing,
// not an error, when no line matches.
func (g git) grep(patterns, exclude []string) ([]grepLine, error) {
	out, err := g.run(grepCommand("-n", patterns, exclude)...)
	if noMatch(err, out) {
		return nil, nil
	}
	if err != nil {
		return nil, err
	}
	return parseGrep(out)
}

// grepFiles returns the tracked text files that contain any of patterns.
func (g git) grepFiles(patterns []string) ([]string, error) {
	out, err := g.run(grepCommand("-l", patterns, nil)...)
	if noMatch(err, out) {
		return nil, nil
	}
	if err != nil {
		return nil, err
	}
	return splitNUL(out), nil
}

func grepCommand(mode string, patterns, exclude []string) []string {
	args := append(append([]string{}, grepArgs...), mode)
	for _, p := range patterns {
		args = append(args, "-e", p)
	}
	args = append(args, "--", ".")
	for _, x := range exclude {
		args = append(args, ":(exclude,glob)"+x)
	}
	return args
}

// noMatch reports whether err is git grep's exit status 1 with no output,
// which means that nothing matched.
func noMatch(err error, out []byte) bool {
	var exit *exec.ExitError
	return errors.As(err, &exit) && exit.ExitCode() == 1 && len(out) == 0
}

// parseGrep reads git grep -n -z output: file, NUL, line number, NUL, text,
// newline.
func parseGrep(out []byte) ([]grepLine, error) {
	var lines []grepLine
	for rec := range strings.SplitSeq(strings.TrimSuffix(string(out), "\n"), "\n") {
		file, rest, ok1 := strings.Cut(rec, "\x00")
		num, text, ok2 := strings.Cut(rest, "\x00")
		n, err := strconv.Atoi(num)
		if !ok1 || !ok2 || err != nil {
			return nil, fmt.Errorf("%w: git grep printed %q", errUnexpected, rec)
		}
		lines = append(lines, grepLine{file: file, line: n, text: text})
	}
	return lines, nil
}
