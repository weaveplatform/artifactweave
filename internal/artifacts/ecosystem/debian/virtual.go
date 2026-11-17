package debian

import (
	"bytes"
	"context"
	"crypto/sha256"
	"errors"
	"fmt"
	"slices"
	"strings"
	"time"

	"github.com/weaveplatform/artifactweave/internal/artifacts/core/virtual"
	"github.com/weaveplatform/artifactweave/internal/kernel"
)

// ErrIndexTooLarge is a merged Packages index over the Merger's Limit.
var ErrIndexTooLarge = errors.New("debian: Packages index exceeds limit")

// MemberIndex is one binary Packages index a member of a virtual repository
// serves: its uncompressed path, dists/<distribution>/<component>/
// binary-<architecture>/Packages, and the rows the caller may read in it.
type MemberIndex struct {
	Path string
	Rows []PackageRecord
}

// Merger is the Debian virtual.Merger for one distribution: a virtual
// repository's Packages index of each component and architecture lists
// its members' rows, one per Filename, from the first member in the walk
// that lists it, so the row names the copy a GET of the file through the
// virtual repository serves (decision 0008, "Per-format merges"). 7.x
// calculates a virtual repository's Release, Packages indexes and their
// by-hash copies itself (RTFACT-22893, RTFACT-31082, RTFACT-29321), so the
// merge writes them all; the caller signs Release with the virtual
// repository's own key pair, never a member's.
type Merger struct {
	// Distribution is the distribution the indexes belong to.
	Distribution string
	// Formats are the virtual repository's
	// optionalIndexCompressionFormats: the JFrog guide gives a virtual
	// repository its own Optional Index Compression Formats, and
	// RTFACT-24246 (7.15.3) put the setting in its REST configuration, so
	// the merged index takes the virtual repository's forms, not its
	// members'.
	Formats []string
	// Date dates the Release.
	Date time.Time
	// Limit bounds each merged index's plain form, in bytes.
	Limit int
}

// Merge returns the virtual repository's metadata files by their path in
// it: each Packages index in its plain form and every form Formats names,
// a by-hash copy of each, and the unsigned Release that lists them. Parts
// that list no row give kernel.ErrNotFound, as a distribution no member
// serves.
func (m Merger) Merge(_ context.Context, parts []virtual.Part[[]MemberIndex]) (map[string][]byte, error) {
	indexes := map[string][]PackageRecord{}
	selected := map[string]map[string]bool{}
	for _, part := range parts {
		for _, index := range part.Doc {
			if selected[index.Path] == nil {
				selected[index.Path] = map[string]bool{}
			}
			for _, row := range index.Rows {
				if selected[index.Path][row.Filename] {
					continue
				}
				selected[index.Path][row.Filename] = true
				indexes[index.Path] = append(indexes[index.Path], row)
			}
		}
	}
	if len(indexes) == 0 {
		return nil, fmt.Errorf("debian: no member indexes %s: %w", m.Distribution, kernel.ErrNotFound)
	}
	base := "dists/" + m.Distribution + "/"
	files := map[string][]byte{}
	var entries []ReleaseEntry
	components, architectures := map[string]bool{}, map[string]bool{}
	for _, indexPath := range sortedKeys(indexes) {
		rows := indexes[indexPath]
		slices.SortFunc(rows, func(a, b PackageRecord) int {
			if n := strings.Compare(a.Name, b.Name); n != 0 {
				return n
			}
			if n := strings.Compare(a.Version, b.Version); n != 0 {
				return n
			}
			return strings.Compare(a.Filename, b.Filename)
		})
		var plain bytes.Buffer
		for _, row := range rows {
			if plain.Len()+len(row.Raw) > m.Limit {
				return nil, ErrIndexTooLarge
			}
			plain.Write(row.Raw)
		}
		forms, err := IndexForms(plain.Bytes(), m.Formats)
		if err != nil {
			return nil, fmt.Errorf("debian: virtual Packages forms: %w", err)
		}
		folder := strings.TrimSuffix(indexPath, "/Packages")
		for _, form := range forms {
			entry := ReleaseEntry{Path: strings.TrimPrefix(indexPath, base) + form.Suffix, Body: form.Body}
			files[base+entry.Path] = entry.Body
			entries = append(entries, entry)
			files[fmt.Sprintf("%s/by-hash/SHA256/%x", folder, sha256.Sum256(entry.Body))] = entry.Body
		}
		parts := strings.Split(indexPath, "/")
		components[parts[2]] = true
		architectures[strings.TrimPrefix(parts[3], "binary-")] = true
	}
	release, err := Release(m.Distribution, sortedKeys(components), sortedKeys(architectures),
		entries, m.Date)
	if err != nil {
		return nil, fmt.Errorf("debian: virtual Release: %w", err)
	}
	files[base+"Release"] = release
	return files, nil
}

// IndexArchitecture is the architecture of the binary Packages index at
// path, a PackagesIndexPath, and "" for any other path.
func IndexArchitecture(path string) string {
	if !PackagesIndexPath(path) {
		return ""
	}
	return strings.TrimPrefix(strings.Split(path, "/")[3], "binary-")
}

func sortedKeys[V any](set map[string]V) []string {
	out := make([]string, 0, len(set))
	for key := range set {
		out = append(out, key)
	}
	slices.Sort(out)
	return out
}
