package pkgmove

import (
	"bytes"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"path"
	"slices"
	"strings"

	"github.com/weaveplatform/artifactweave/tools/citestselection/ownership"
)

// testOwnership is version 2 of tools/citestselection/test_ownership.json.
// Decoding rejects unknown keys, so a later version fails here instead of
// losing what it adds.
type testOwnership struct {
	Version            int                 `json:"version"`
	Packages           map[string]string   `json:"packages"`
	SharedFiles        map[string][]string `json:"shared_files"`
	FormatDependencies map[string][]string `json:"format_dependencies"`
}

// dirMapper returns where a change puts a directory and the file renames of
// its entry, or ok false when the directory does not move.
type dirMapper func(dir string) (to string, rename map[string]string, ok bool)

// mappingDirs is the dirMapper of a set of mappings: a directory moves when it
// is the old path of a mapping, or lies below one whose contents all move
// alike.
func mappingDirs(ms []*mapping) dirMapper {
	return func(dir string) (string, map[string]string, bool) {
		for _, m := range ms {
			switch {
			case m.to == "":
			case dir == m.from:
				return m.to, m.rename, true
			case m.whole && strings.HasPrefix(dir, m.from+"/"):
				return m.to + dir[len(m.from):], nil, true
			}
		}
		return "", nil, false
	}
}

// rewriteOwnership maps the directories and files that the ownership
// inventory names through mapDir. A package whose new directory is no
// longer an owned test directory loses its keys, with a notice. The result is
// encoded as tools/citestselection writes it: two-space indent, keys and lists
// sorted, a trailing newline, and no empty list. src comes back unchanged when
// nothing moves.
func rewriteOwnership(src []byte, mapDir dirMapper) ([]byte, []string, error) {
	var o testOwnership
	dec := json.NewDecoder(bytes.NewReader(src))
	dec.DisallowUnknownFields()
	if err := dec.Decode(&o); err != nil {
		return nil, nil, fmt.Errorf("decode %s: %w", ownershipPath, err)
	}
	if _, err := dec.Token(); !errors.Is(err, io.EOF) {
		return nil, nil, fmt.Errorf(
			"%w: %s continues after its object",
			errUnexpected,
			ownershipPath,
		)
	}
	var notices []string
	changed := false
	dropped := map[string]bool{}
	packages := map[string]string{}
	for dir, owner := range o.Packages {
		to, _, ok := mapDir(dir)
		switch {
		case !ok:
			packages[dir] = owner
			continue
		case ownership.OwnedTestDir(to):
			packages[to] = owner
		default:
			dropped[dir] = true
			notices = append(notices, fmt.Sprintf(
				"%s: dropped %s, since %s is not a directory whose tests need an owner",
				ownershipPath, dir, to))
		}
		changed = true
	}
	mapFile := func(file string) (string, bool) {
		dir, name := path.Split(file)
		dir = path.Clean(dir)
		if dropped[dir] {
			return "", false
		}
		if to, rename, ok := mapDir(dir); ok {
			if renamed, ok := rename[name]; ok {
				name = renamed
			}
			return to + "/" + name, true
		}
		return file, true
	}
	shared := map[string][]string{}
	for dir, names := range o.SharedFiles {
		var kept []string
		for _, name := range names {
			if f, ok := mapFile(dir + "/" + name); ok {
				kept = append(kept, f)
			}
		}
		if len(kept) > 0 {
			to := path.Dir(kept[0])
			for i, f := range kept {
				kept[i] = path.Base(f)
			}
			slices.Sort(kept)
			shared[to] = kept
		}
	}
	deps := map[string][]string{}
	for owner, files := range o.FormatDependencies {
		var kept []string
		for _, file := range files {
			if f, ok := mapFile(file); ok {
				kept = append(kept, f)
			}
		}
		if len(kept) > 0 {
			slices.Sort(kept)
			deps[owner] = kept
		}
	}
	if !changed {
		return src, nil, nil
	}
	o.Packages, o.SharedFiles, o.FormatDependencies = packages, shared, deps
	slices.Sort(notices)
	return encodeOwnership(o), notices, nil
}

// encodeOwnership writes o as tools/citestselection does: two-space indent,
// the four keys in struct order, map keys sorted by encoding/json, and a
// trailing newline from Encode.
func encodeOwnership(o testOwnership) []byte {
	var b bytes.Buffer
	enc := json.NewEncoder(&b)
	enc.SetEscapeHTML(false)
	enc.SetIndent("", "  ")
	// Strings, string slices and string maps always encode, and a
	// bytes.Buffer write cannot fail, so there is no error to handle.
	_ = enc.Encode(o)
	return b.Bytes()
}
