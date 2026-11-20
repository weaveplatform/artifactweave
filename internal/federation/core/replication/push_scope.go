package replication

import (
	"fmt"
	"strings"
	"unicode/utf8"

	"github.com/weaveplatform/artifactweave/internal/kernel"
	"github.com/weaveplatform/artifactweave/internal/kernel/antpath"
)

// pushScope applies the same repository-relative selection to enumeration and
// exact destination reconciliation. A match grants no source Read or target
// Delete permission and never authorizes a recursive folder deletion.
type pushScope struct {
	root    string
	include antpath.Pattern
	exclude *antpath.Pattern
}

func newPushScope(root, include, exclude string) (pushScope, error) {
	for _, value := range []string{root, include, exclude} {
		if !utf8.ValidString(value) || strings.ContainsRune(value, 0) {
			return pushScope{}, fmt.Errorf("invalid replication path criteria: %w", kernel.ErrInvalid)
		}
	}
	root = strings.Trim(root, "/")
	if root == "." {
		root = ""
	}
	if root != "" {
		if _, _, _, err := kernel.SplitItemPath(root); err != nil {
			return pushScope{}, fmt.Errorf("replication root path: %w: %w", kernel.ErrInvalid, err)
		}
	}
	scope := pushScope{root: root, include: antpath.CompilePattern(include)}
	if exclude != "" {
		pattern := antpath.CompilePattern(exclude)
		scope.exclude = &pattern
	}
	return scope, nil
}

func (s pushScope) matches(path string) bool {
	// Do not clean an untrusted target listing into the selected namespace.
	// Source and destination adapters supply canonical relative file paths.
	if path == "" || !utf8.ValidString(path) || path != strings.Trim(path, "/") {
		return false
	}
	if _, _, _, err := kernel.SplitItemPath(path); err != nil {
		return false
	}
	if s.root != "" && path != s.root && !strings.HasPrefix(path, s.root+"/") {
		return false
	}
	return s.include.Match(path) && (s.exclude == nil || !s.exclude.Match(path))
}
