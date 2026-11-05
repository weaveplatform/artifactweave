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
// alike. A file entry moves no directory that the inventory could name.
func mappingDirs(ms []*mapping) dirMapper {
	return func(dir string) (string, map[string]string, bool) {
		for _, m := range ms {
			switch {
			case m.to == "" || m.file || m.asset:
			case dir == m.from:
				return m.to, m.rename, true
			case m.whole && strings.HasPrefix(dir, m.from+"/"):
				return m.to + dir[len(m.from):], nil, true
			}
		}
		return "", nil, false
	}
}

// ownershipChange is what a change does to the paths that the ownership
// inventory names: the directories it moves and the test files that file
// entries move on their own.
type ownershipChange struct {
	mapDir dirMapper
	// tests are the test files that file entries move.
	tests []fileMove
	// testsLeft reports whether a test file stays directly in a directory
	// once the tests move.
	testsLeft func(dir string) bool
}

// sharedOwner is the owner of a directory whose tests no single format owns.
const sharedOwner = "shared"

// rewriteOwnership applies own to the ownership inventory src. Each test file
// that a file entry moves leaves its old directory's shared_files list (the
// key goes when the list empties, and the directory's packages key with it
// when no test stays there), joins its new directory and takes its format
// dependencies along (docs/testing/server-test-ownership.md, "Moving a root
// test"); then the moved directories are mapped. A package whose new
// directory is no longer an owned test directory loses its keys, with a
// notice. The result is encoded as tools/citestselection writes it:
// two-space indent, keys and lists sorted, a trailing newline, and no empty
// list. src comes back unchanged when nothing moves.
func rewriteOwnership(src []byte, own ownershipChange) ([]byte, []string, error) {
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
	// A key the inventory leaves out decodes as nil, which a moved test
	// would write to.
	if o.Packages == nil {
		o.Packages = map[string]string{}
	}
	if o.SharedFiles == nil {
		o.SharedFiles = map[string][]string{}
	}
	r := &ownershipRewrite{o: &o}
	for _, m := range own.tests {
		r.moveTest(m, own.testsLeft)
	}
	r.mapDirs(own.mapDir)
	if !r.changed {
		return src, nil, nil
	}
	slices.Sort(r.notices)
	return encodeOwnership(o), r.notices, nil
}

// ownershipRewrite is the inventory being rewritten.
type ownershipRewrite struct {
	o       *testOwnership
	changed bool
	notices []string
}

// moveTest moves the test file m.from to m.to in the inventory.
func (r *ownershipRewrite) moveTest(m fileMove, testsLeft func(string) bool) {
	o := r.o
	oldDir, newDir, name := path.Dir(m.from), path.Dir(m.to), path.Base(m.to)
	if list, ok := o.SharedFiles[oldDir]; ok {
		list = slices.DeleteFunc(
			slices.Clone(list),
			func(f string) bool { return f == path.Base(m.from) },
		)
		r.changed = r.changed || len(list) < len(o.SharedFiles[oldDir])
		o.SharedFiles[oldDir] = list
		if len(list) == 0 {
			delete(o.SharedFiles, oldDir)
		}
	}
	if _, ok := o.Packages[oldDir]; ok && o.SharedFiles[oldDir] == nil && !testsLeft(oldDir) {
		delete(o.Packages, oldDir)
		r.changed = true
	}
	var formats []string
	for owner, files := range o.FormatDependencies {
		if slices.Contains(files, m.from) {
			formats = append(formats, owner)
		}
	}
	slices.Sort(formats)
	if !ownership.OwnedTestDir(newDir) {
		r.dropDependencies(m.from, formats, fmt.Sprintf(
			"%s is not a directory whose tests need an owner", newDir))
		return
	}
	owner, exists := o.Packages[newDir]
	if !exists {
		owner = proposedOwner(newDir, formats)
		o.Packages[newDir] = owner
		r.changed = true
		if owner == sharedOwner && formatOf(newDir) != "" {
			r.notices = append(r.notices, fmt.Sprintf(
				"%s: %s is recorded as shared, so every change below it runs full CI; "+
					"give it its format as owner once its tests are reviewed",
				ownershipPath, newDir))
		}
	}
	switch {
	case owner != sharedOwner:
		for _, f := range formats {
			if f == owner {
				// The format's narrow plan runs the whole package, so the
				// file needs no dependency entry of its own.
				r.renameDependency(f, m.from, "")
			} else {
				r.dropDependencies(
					m.from,
					[]string{f},
					fmt.Sprintf("%s is owned by %s", newDir, owner),
				)
			}
		}
	case !exists || o.SharedFiles[newDir] != nil:
		o.SharedFiles[newDir] = append(o.SharedFiles[newDir], name)
		slices.Sort(o.SharedFiles[newDir])
		r.changed = true
		for _, f := range formats {
			r.renameDependency(f, m.from, m.to)
		}
	default:
		r.dropDependencies(m.from, formats, fmt.Sprintf(
			"%s is shared as a whole and lists no file", newDir))
	}
}

// dropDependencies removes the file from the format dependencies of formats,
// with a notice that says why.
func (r *ownershipRewrite) dropDependencies(file string, formats []string, why string) {
	for _, f := range formats {
		r.renameDependency(f, file, "")
		r.notices = append(r.notices, fmt.Sprintf(
			"%s: dropped %s from the format dependencies of %s, since %s",
			ownershipPath, file, f, why))
	}
}

// renameDependency replaces from with to in the dependencies of format,
// which list from, or removes it when to is empty; a list that empties loses
// its key.
func (r *ownershipRewrite) renameDependency(format, from, to string) {
	o := r.o
	list := slices.DeleteFunc(slices.Clone(o.FormatDependencies[format]), func(f string) bool {
		return f == from
	})
	if to != "" {
		list = append(list, to)
		slices.Sort(list)
	}
	r.changed = true
	o.FormatDependencies[format] = list
	if len(list) == 0 {
		delete(o.FormatDependencies, format)
	}
}

// proposedOwner returns the owner of a new inventory directory: the format
// whose directory it is (test/server/artifacts/<t> or
// <root>/artifacts/ecosystem/<t>/http), when every format dependency of the
// tests that join it names that format, and shared otherwise. A test with no
// dependency gives no evidence of its format, so it stays shared until a
// review says otherwise.
func proposedOwner(dir string, formats []string) string {
	format := formatOf(dir)
	if format == "" || len(formats) == 0 ||
		slices.ContainsFunc(formats, func(f string) bool { return f != format }) {
		return sharedOwner
	}
	return format
}

// formatOf returns <t> for a format's test directory, test/server/artifacts/<t>
// or <root>/artifacts/ecosystem/<t>/http with <root> migration or internal,
// and empty for any other directory.
func formatOf(dir string) string {
	parts := strings.Split(dir, "/")
	switch {
	case len(parts) == 4 && parts[0] == "test" && parts[1] == "server" && parts[2] == "artifacts":
		return parts[3]
	case len(parts) == 5 && (parts[0] == "migration" || parts[0] == "internal") &&
		parts[1] == "artifacts" && parts[2] == "ecosystem" && parts[4] == "http":
		return parts[3]
	}
	return ""
}

// mapDirs maps the directories and files that the inventory names through
// mapDir. A package whose new directory is no longer an owned test directory
// loses its keys and dependencies, with a notice.
func (r *ownershipRewrite) mapDirs(mapDir dirMapper) {
	o := r.o
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
			r.notices = append(r.notices, fmt.Sprintf(
				"%s: dropped %s, since %s is not a directory whose tests need an owner",
				ownershipPath, dir, to))
		}
		r.changed = true
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
	o.Packages, o.SharedFiles, o.FormatDependencies = packages, shared, deps
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
