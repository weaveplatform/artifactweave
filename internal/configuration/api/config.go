package api

import (
	"context"
	"errors"
	"fmt"
	"strings"
	"time"
)

// ErrInvalid reports a configuration descriptor that cannot be applied:
// Descriptor.Parse and Descriptor.Apply return an error that wraps it and
// names the first problem, and every entry point answers it with 400. Its
// text is part of that answer, so it stays byte-identical.
var ErrInvalid = errors.New("descriptor: invalid configuration descriptor")

// Plan is a parsed configuration descriptor, ready to apply. Only
// Descriptor.Parse makes one, and only the Descriptor implementation whose
// Parse made it can apply it: callers hold it without looking inside.
type Plan interface {
	// DescriptorPlan marks the types Parse returns as plans. It does
	// nothing.
	DescriptorPlan()
}

// Result names what applying a descriptor changed, each entry as
// "section.key".
type Result struct {
	Created, Updated, Deleted, Unchanged []string
	// Changes counts the values in the document, whether or not they differ
	// from what is stored: the number Merged reports.
	Changes int
	// DryRun is set when nothing was written.
	DryRun bool
}

// Merged is the PATCH endpoint's answer, in Artifactory's words. It counts
// the document's values rather than what changed, as Artifactory does, so
// applying the same patch again gives the same number.
func (r Result) Merged() string {
	return fmt.Sprintf("%d changes to config merged successfully", r.Changes)
}

// String is the plain-text summary `config apply` prints.
func (r Result) String() string {
	var b strings.Builder
	if r.DryRun {
		b.WriteString("Dry run: the configuration is valid and nothing was written.\n")
	} else {
		b.WriteString("Applied the configuration descriptor.\n")
	}
	for _, l := range []struct {
		label string
		names []string
	}{
		{"Created", r.Created},
		{"Updated", r.Updated},
		{"Deleted", r.Deleted},
		{"Unchanged", r.Unchanged},
	} {
		if len(l.names) > 0 {
			fmt.Fprintf(&b, "%s: %s\n", l.label, strings.Join(l.names, ", "))
		}
	}
	return b.String()
}

// Descriptor reads and applies Artifactory's YAML configuration descriptor
// (decision 0009): the body of PATCH /api/system/configuration, `config
// apply` and the bootstrap descriptor.
type Descriptor interface {
	// Parse reads doc strictly and resolves its ${NAME} references through
	// env, which answers as os.LookupEnv does. It reads and writes nothing
	// else, so a caller parses before it builds a keyring or opens a store,
	// and a broken descriptor is reported ahead of a key or database error.
	// A document that cannot be applied answers an error wrapping
	// ErrInvalid.
	Parse(doc []byte, env func(name string) (string, bool)) (Plan, error)
	// Apply checks the whole of p, which this Descriptor's Parse returned,
	// against the current state and then, unless dryRun, writes it in one
	// transaction, joining the one ctx carries; a plan with one bad entry, or
	// one whose last write fails, changes nothing. Entries that would not
	// change are left alone, so applying the same plan twice is a no-op the
	// second time. now stamps the documents written and the reindexes
	// queued, and a repository the plan deletes is deleted by ctx's
	// principal, or by no one when ctx carries none. A plan the checks
	// refuse answers an error wrapping ErrInvalid, and a failed write one
	// wrapped as "apply configuration: %w".
	Apply(ctx context.Context, p Plan, now time.Time, dryRun bool) (Result, error)
}

// System is the system configuration that other modules read.
type System interface {
	// DownloadRedirectMinSize returns, in bytes, the minimum size of a file
	// that a repository with downloadRedirect or cdnRedirect redirects, as
	// the descriptor's downloadRedirectConfig.fileMinimumSize set it. ok is
	// false when no descriptor has set it, and the caller then keeps the
	// installation's download.redirect.min_size (decision 0029).
	DownloadRedirectMinSize(ctx context.Context) (n int64, ok bool, err error)
}

// API is the configuration module's published surface: one implementation
// of each contract, as the module binds them.
type API struct {
	Descriptor Descriptor
	System     System
}
