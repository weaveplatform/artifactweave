package pkgmove

import (
	"context"
	"fmt"
	"io"
	"os"
	"os/exec"
	"slices"
	"strings"
)

// runner runs one regeneration command in the repository root, with its
// output going to the caller's diagnostics. Tests replace it with a fake that
// records the commands.
type runner func(root string, args ...string) error

// execRunner runs commands as processes. GOWORK=off matches the Makefile,
// so a local go.work never changes what a step generates.
func execRunner(ctx context.Context, out io.Writer) runner {
	return func(root string, args ...string) error {
		//nolint:gosec // args are one of the fixed commands of regenSteps
		cmd := exec.CommandContext(ctx, args[0], args[1:]...)
		cmd.Dir = root
		cmd.Stdout = out
		cmd.Stderr = out
		cmd.Env = append(os.Environ(), "GOWORK=off")
		if err := cmd.Run(); err != nil {
			return fmt.Errorf("%s: %w", strings.Join(args, " "), err)
		}
		return nil
	}
}

// step is one regeneration command and the reason it runs.
type step struct {
	args []string
	why  string
}

// Files whose content decides a regeneration step, besides those under api/.
const (
	inventoryPath = "docs/implementation/package-format-inventory.json"
	recipeSources = "docs/client-recipes/src/"
	evidenceLock  = "docs/client-recipes/evidence-lock.json"
	apiLedgerPath = "docs/research/api-reliability/operations.json"
)

// regenInputs are the facts of a change that decide its regeneration steps.
type regenInputs struct {
	// changed and moved are the old paths of the rewritten and the moved
	// files.
	changed, moved []string
	// generatedNamesOld reports that a generated Go file names an old path.
	generatedNamesOld bool
	// mentions reports whether a tracked file names an old path.
	mentions func(string) bool
}

// regenSteps returns the regeneration steps a change needs, in the order
// they must run:
//
//   - make generate when a file under api/ changed, when a stubgen source or
//     a generated file moved or changed, or when generated code names an old
//     path (generated files are regenerated, never edited, decision 0002);
//   - go run ./tools/formatinventory when the format inventory changed;
//   - make client-recipes when a recipe source changed or the evidence lock
//     names an old path;
//   - make api-ledger when the API reliability ledger names an old path;
//   - make fmt always.
func regenSteps(in regenInputs) []step {
	var steps []step
	touched := slices.Concat(in.changed, in.moved)
	switch {
	case slices.ContainsFunc(in.changed, func(p string) bool { return strings.HasPrefix(p, "api/") }):
		steps = append(
			steps,
			step{
				[]string{"make", "generate"},
				"an API specification or its generator configuration changed",
			},
		)
	case slices.ContainsFunc(touched, func(p string) bool {
		return strings.Contains("/"+p, "/stubgen/") || strings.HasSuffix(p, ".gen.go")
	}):
		steps = append(
			steps,
			step{[]string{"make", "generate"}, "a generator or a generated file moved or changed"},
		)
	case in.generatedNamesOld:
		steps = append(
			steps,
			step{[]string{"make", "generate"}, "generated code names a moved path"},
		)
	}
	if slices.Contains(in.changed, inventoryPath) {
		steps = append(
			steps,
			step{[]string{"go", "run", "./tools/formatinventory"}, "the format inventory changed"},
		)
	}
	if slices.ContainsFunc(
		in.changed,
		func(p string) bool { return strings.HasPrefix(p, recipeSources) },
	) ||
		in.mentions(evidenceLock) {
		steps = append(
			steps,
			step{[]string{"make", "client-recipes"}, "client recipe evidence names a moved path"},
		)
	}
	if in.mentions(apiLedgerPath) {
		steps = append(
			steps,
			step{[]string{"make", "api-ledger"}, "the API reliability ledger names a moved path"},
		)
	}
	return append(steps, step{[]string{"make", "fmt"}, "format the changed files"})
}

// names reports whether the tracked file at p names an old path of mt.
func (t *tree) names(mt *matcher, p string) bool {
	if !t.tracked[p] {
		return false
	}
	src, err := t.read(p)
	return err == nil && len(mt.find(string(src))) > 0
}

// regenerate runs the steps in order and stops at the first that fails.
func (e *env) regenerate(root string, steps []step) error {
	for _, s := range steps {
		fmt.Fprintf(e.stdout, "run %s (%s)\n", strings.Join(s.args, " "), s.why)
		if err := e.run(root, s.args...); err != nil {
			return fmt.Errorf("regenerate: %w", err)
		}
	}
	return nil
}
