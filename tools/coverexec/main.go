// Command coverexec runs one test binary for make test (go test -exec),
// moving its coverage output into a directory of its own under the one
// -test.gocoverdir names. make test then merges those directories.
//
// Test binaries built with -coverpkg=<module>/... that link the same
// instrumented packages write the same covmeta.<hash> file name. In one
// shared directory, two that finish together each rename a temporary file
// onto that name (internal/coverage/cfile, emitMetaDataFile), and Windows
// refuses the second rename with "Access is denied" (issue 368).
//
//	coverexec <test binary> [args...]
package main

import (
	"errors"
	"fmt"
	"os"
	"os/exec"
	"strings"
)

const flagPrefix = "-test.gocoverdir="

func main() {
	if len(os.Args) < 2 {
		fmt.Fprintln(os.Stderr, "usage: coverexec <test binary> [args...]")
		os.Exit(2)
	}
	args, err := ownDir(os.Args[2:])
	if err != nil {
		fmt.Fprintln(os.Stderr, "coverexec:", err)
		os.Exit(1)
	}
	cmd := exec.Command(os.Args[1], args...)
	cmd.Stdin, cmd.Stdout, cmd.Stderr = os.Stdin, os.Stdout, os.Stderr
	err = cmd.Run()
	if exit := (*exec.ExitError)(nil); errors.As(err, &exit) {
		os.Exit(exit.ExitCode())
	}
	if err != nil {
		fmt.Fprintln(os.Stderr, "coverexec:", err)
		os.Exit(1)
	}
}

// ownDir points the last -test.gocoverdir in args at a new directory inside
// the one it names; the flag package keeps the last value given. Arguments
// without the flag pass through unchanged.
func ownDir(args []string) ([]string, error) {
	for i := len(args) - 1; i >= 0; i-- {
		parent, ok := strings.CutPrefix(args[i], flagPrefix)
		if !ok {
			continue
		}
		dir, err := os.MkdirTemp(parent, "pkg-")
		if err != nil {
			return nil, err
		}
		out := append([]string(nil), args...)
		out[i] = flagPrefix + dir
		return out, nil
	}
	return args, nil
}
