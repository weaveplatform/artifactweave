package specoracle

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"net/http"
	"net/url"
	"os"
	"path"
	"path/filepath"
	"strings"
)

// FamilyRecord says where the page list of one API family came from.
type FamilyRecord struct {
	Index  string `json:"index"`
	Family string `json:"family"`
	// Source is the llms.txt whose links were fetched: Index itself, or the
	// parent index when Index failed. Empty when neither gave the pages.
	Source string `json:"source,omitempty"`
	// Section is the parent index heading the pages were taken from.
	Section string `json:"section,omitempty"`
	// Error says why Index gave no pages. It stays set when the parent index
	// stood in, so the fetch still reports the broken family index.
	Error string `json:"error,omitempty"`
}

// Missing lists the families of which the cache holds no OpenAPI block. Load
// reads only what is cached, so the comparison cannot tell such a family
// from one JFrog stopped documenting.
func (m Manifest) Missing() []FamilyRecord {
	cached := map[string]bool{}
	for _, e := range m.Entries {
		if e.File != "" {
			cached[e.Family] = true
		}
	}
	var out []FamilyRecord
	for _, fam := range m.Families {
		if !cached[fam.Family] {
			out = append(out, fam)
		}
	}
	return out
}

var (
	// ErrMissingFamily is wrapped once for each family CheckFamilies finds
	// missing.
	ErrMissingFamily = errors.New("specoracle: API family missing from the oracle cache")
	// ErrNoFamilies marks a manifest written before Fetch recorded families,
	// which cannot show whether one is missing.
	ErrNoFamilies   = errors.New("specoracle: manifest records no API families")
	errNoKnownPages = errors.New("no page of the family is known from an earlier fetch")
	errNoParent     = errors.New("index has no parent llms.txt")
	errNotInParent  = errors.New("no section of the parent index lists a known page of the family")
)

// ReadManifest reads the manifest Fetch wrote into cacheDir.
func ReadManifest(cacheDir string) (Manifest, error) {
	var m Manifest
	data, err := os.ReadFile(filepath.Join(cacheDir, ManifestFile))
	if err != nil {
		return m, fmt.Errorf("specoracle: read manifest: %w", err)
	}
	if err := json.Unmarshal(data, &m); err != nil {
		return m, fmt.Errorf("specoracle: parse manifest: %w", err)
	}
	return m, nil
}

// CheckFamilies returns an error naming each family, with its index URL and
// what went wrong, that the cache in cacheDir lacks. Every family Fetch is
// given was fetched when it was added, so a missing one is a docs or network
// fault, not a family JFrog dropped. The parity test and the commands that
// rewrite committed files from the cache refuse such a cache: comparing it
// reports the family's baseline entries as improvements, and rewriting the
// baseline from it would delete them (#1308).
func CheckFamilies(cacheDir string) error {
	m, err := ReadManifest(cacheDir)
	if err != nil {
		return err
	}
	if len(m.Families) == 0 {
		return fmt.Errorf("%w in %s; run make oracle-fetch again", ErrNoFamilies, cacheDir)
	}
	var errs []error
	for _, fam := range m.Missing() {
		why := fam.Error
		if why == "" {
			why = "no page it lists gave an OpenAPI block"
		}
		errs = append(
			errs,
			fmt.Errorf("%w: %s, index %s: %s", ErrMissingFamily, fam.Family, fam.Index, why),
		)
	}
	return errors.Join(errs...)
}

// KnownPages reads, from an inventory WriteJSON wrote, the page URLs of each
// family. The inventory is committed, so it records a good fetch even where
// the cache starts empty, as it does each week in CI. A missing file gives no
// pages and no error, which only turns the fallback off.
func KnownPages(inventoryPath string) (map[string][]string, error) {
	data, err := os.ReadFile(inventoryPath)
	if errors.Is(err, os.ErrNotExist) {
		return map[string][]string{}, nil
	}
	if err != nil {
		return nil, fmt.Errorf("specoracle: read inventory: %w", err)
	}
	var inv Inventory
	if err := json.Unmarshal(data, &inv); err != nil {
		return nil, fmt.Errorf("specoracle: parse inventory %s: %w", inventoryPath, err)
	}
	out := map[string][]string{}
	seen := map[string]bool{}
	for _, op := range inv.Operations {
		key := op.Family + " " + op.Source
		if op.Source == "" || seen[key] {
			continue
		}
		seen[key] = true
		out[op.Family] = append(out[op.Family], op.Source)
	}
	return out, nil
}

// parentIndex is one parent llms.txt as read, or why it could not be.
type parentIndex struct {
	sections []indexSection
	err      error
}

// indexSection is one "## " heading of an llms.txt with the links under it.
type indexSection struct {
	title string
	links [][2]string
}

// parseSections splits an llms.txt at its "## " headings. Links before the
// first heading belong to no family, so they are dropped.
func parseSections(body string) []indexSection {
	var out []indexSection
	for _, line := range strings.Split(body, "\n") {
		if title, ok := strings.CutPrefix(line, "## "); ok {
			out = append(out, indexSection{title: strings.TrimSpace(title)})
			continue
		}
		if len(out) == 0 {
			continue
		}
		if m := linkLine.FindStringSubmatch(line); m != nil {
			last := &out[len(out)-1]
			last.links = append(last.links, [2]string{m[1], m[2]})
		}
	}
	return out
}

// parentURL is the llms.txt one directory above a family index:
// .../artifactory/reference/llms.txt for
// .../artifactory/reference/jfrog-artifactory-repositories-api/llms.txt. It
// lists every family of that docs project, each under its own "## " heading.
func parentURL(index string) (string, error) {
	u, err := url.Parse(index)
	if err != nil {
		return "", fmt.Errorf("parse index %s: %w", index, err)
	}
	dir := path.Dir(u.Path)
	if path.Base(u.Path) != "llms.txt" || dir == "/" || dir == "." {
		return "", fmt.Errorf("%s: %w", index, errNoParent)
	}
	u.Path = path.Join(path.Dir(dir), "llms.txt")
	return u.String(), nil
}

// parentLinks finds a family's pages in its parent index. The section is
// chosen by the pages an earlier fetch found for the family, not by its
// heading: a rename changes the heading and the family's index URL together,
// which is why the index failed, while the page URLs stay. The section that
// lists the most known pages wins, so a page listed twice cannot pull in
// another family. All of its links are fetched, so pages JFrog added since
// are compared too.
func (f *fetcher) parentLinks(
	ctx context.Context,
	index string,
	known []string,
) (parent, section string, links [][2]string, err error) {
	if len(known) == 0 {
		return "", "", nil, errNoKnownPages
	}
	parent, err = parentURL(index)
	if err != nil {
		return "", "", nil, err
	}
	p, ok := f.parents[parent]
	if !ok {
		p = f.readParent(ctx, parent)
		f.parents[parent] = p
	}
	if p.err != nil {
		return "", "", nil, p.err
	}
	want := map[string]bool{}
	for _, k := range known {
		want[strings.TrimSuffix(k, ".md")] = true
	}
	best, most := -1, 0
	for i, s := range p.sections {
		n := 0
		for _, l := range s.links {
			if want[strings.TrimSuffix(l[1], ".md")] {
				n++
			}
		}
		if n > most {
			best, most = i, n
		}
	}
	if best < 0 {
		return "", "", nil, fmt.Errorf("%s: %w", parent, errNotInParent)
	}
	return parent, p.sections[best].title, p.sections[best].links, nil
}

func (f *fetcher) readParent(ctx context.Context, parent string) parentIndex {
	body, status, err := f.get(ctx, parent)
	if err != nil {
		return parentIndex{err: err}
	}
	if status != http.StatusOK {
		return parentIndex{
			err: fmt.Errorf("parent index %s: status %d: %w", parent, status, ErrBadStatus),
		}
	}
	return parentIndex{sections: parseSections(string(body))}
}
