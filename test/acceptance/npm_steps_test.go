package acceptance

import (
	"io"
	"os/exec"
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

// npmOnPath skips the scenario unless the runner has an npm of its own, which
// the JFrog CLI's npm commands run.
func (w *world) npmOnPath() error {
	if _, err := exec.LookPath("npm"); err != nil {
		return godog.ErrSkip
	}
	return nil
}
