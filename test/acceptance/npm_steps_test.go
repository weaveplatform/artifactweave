package acceptance

import (
	"crypto/sha512"
	"encoding/base64"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"time"

	"github.com/cucumber/godog"
)

// promptPause is how long each answer waits before npm gets it. npm reads
// each prompt's answer with a reader of its own (the read package), which
// takes whatever stdin holds and drops what follows the first line, so an
// answer sent before the prompt that reads the previous one has closed is
// lost. Five seconds outlasts npm's start and its web-login attempt, which
// come before the first prompt, and the gap between two prompts.
const promptPause = 5 * time.Second

// runNPMAnswering runs npm as runNPM does, typing input's lines into its
// prompts one at a time, as npm login's user name and password.
func (w *world) runNPMAnswering(args string, input *godog.DocString) error {
	lines := strings.Split(strings.TrimRight(w.expand(input.Content), "\n"), "\n")
	return w.runNPMWith(&pacedLines{lines: lines, pause: promptPause}, args)
}

// pacedLines reads as a person types: each line after a pause, then the end
// of input.
type pacedLines struct {
	lines []string
	pause time.Duration
	// pending is what is left of the line being read.
	pending string
}

func (p *pacedLines) Read(b []byte) (int, error) {
	if p.pending == "" {
		if len(p.lines) == 0 {
			return 0, io.EOF
		}
		time.Sleep(p.pause)
		p.pending, p.lines = p.lines[0]+"\n", p.lines[1:]
	}
	n := copy(b, p.pending)
	p.pending = p.pending[n:]
	return n, nil
}

// onRunnerPath skips the scenario unless the runner has the tool on its own
// PATH, as the JFrog CLI's npm, mvn and gradle commands run it there rather
// than in a client container.
func (w *world) onRunnerPath(tool string) error {
	if _, err := exec.LookPath(tool); err != nil {
		return godog.ErrSkip
	}
	return nil
}

// lockfilePins checks the entry npm install wrote to package-lock.json for
// a package: resolved is the binary's URL for the tarball and integrity the
// SHA-512 of the bytes served there, so an install from the lockfile alone
// fetches and verifies the same file.
func (w *world) lockfilePins(name, tarball string) error {
	dir, err := w.filesDir()
	if err != nil {
		return err
	}
	raw, err := os.ReadFile(filepath.Join(dir, "package-lock.json"))
	if err != nil {
		return err
	}
	var lock struct {
		Packages map[string]struct {
			Resolved  string `json:"resolved"`
			Integrity string `json:"integrity"`
		} `json:"packages"`
	}
	if err := json.Unmarshal(raw, &lock); err != nil {
		return fmt.Errorf("package-lock.json: %w", err)
	}
	entry, ok := lock.Packages["node_modules/"+name]
	if !ok {
		return fmt.Errorf("package-lock.json has no entry for %s:\n%s", name, raw)
	}
	if want := w.url + w.expand(tarball); entry.Resolved != want {
		return fmt.Errorf("%s resolved to %q, want %q", name, entry.Resolved, want)
	}
	if err := w.getAsAdmin(tarball); err != nil {
		return err
	}
	if err := w.statusIs(http.StatusOK); err != nil {
		return err
	}
	sum := sha512.Sum512(w.body)
	if want := "sha512-" + base64.StdEncoding.EncodeToString(sum[:]); entry.Integrity != want {
		return fmt.Errorf("%s has integrity %q, but the tarball served is %q",
			name, entry.Integrity, want)
	}
	return nil
}
