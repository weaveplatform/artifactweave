package maven

import (
	"bytes"
	"context"
	"encoding/xml"
	"errors"
	"fmt"
	"regexp"
	"slices"
	"strconv"
	"strings"
	"time"

	"github.com/weaveplatform/artifactweave/internal/artifacts/core/model"
	"github.com/weaveplatform/artifactweave/internal/artifacts/core/virtual"
	"github.com/weaveplatform/artifactweave/internal/kernel"
)

// Settings holds Maven's server options. A nil switch retains 7.x's default.
type Settings struct{ MetadataVersion3Enabled *bool }

// MetadataDocument is a parsed member document or the result of a merge.
// Its model stays private so callers cannot bypass parsing or mutate a merge.
type MetadataDocument struct {
	model    *metadata
	Modified time.Time
}

// Bytes serializes even a single member; a virtual response never returns its
// uploaded XML verbatim. The first document owns the root's model version.
func (d MetadataDocument) Bytes() []byte { return d.model.write(d.model.ModelVersion) }

var errMetadataLarge = errors.New("maven metadata exceeds limit")

var legacyJavaAgent = regexp.MustCompile(`^[Jj]ava/.+$`)

var xmlReference = regexp.MustCompile(`^&(?:amp|lt|gt|apos|quot|#[0-9]+|#x[0-9a-fA-F]+);`)

// ParseMetadata tolerates the raw ampersands accepted by MavenModelUtils while
// preserving already escaped references. It bounds malformed member input.
func ParseMetadata(body []byte, modified time.Time) (MetadataDocument, error) {
	if len(body) > maxMetadata {
		return MetadataDocument{}, errMetadataLarge
	}
	var escaped bytes.Buffer
	for i := 0; i < len(body); i++ {
		if body[i] == '&' && !xmlReference.Match(body[i:]) {
			escaped.WriteString("&amp;")
		} else {
			escaped.WriteByte(body[i])
		}
	}
	var m metadata
	if err := xml.Unmarshal(escaped.Bytes(), &m); err != nil {
		return MetadataDocument{}, fmt.Errorf("parse maven metadata: %w", err)
	}
	return MetadataDocument{model: &m, Modified: modified}, nil
}

// Merger follows MergeableMavenMetadata: union versions and plugin prefixes,
// retain the first member's coordinates, and prefer the larger snapshot build.
// SnapshotVersions is set only for a snapshot path and an M3-capable client.
type Merger struct{ SnapshotVersions bool }

// Merge returns the first member reserialized, with subsequent members merged
// in walk order. It never mutates the supplied documents.
func (m Merger) Merge(_ context.Context, parts []virtual.Part[MetadataDocument]) (MetadataDocument, error) {
	if len(parts) == 0 {
		return MetadataDocument{}, kernel.ErrNotFound
	}
	// Parse again to own every slice and pointer; neither a merge nor a caller
	// serializing its result may change a member used by another request.
	out, err := ParseMetadata(parts[0].Doc.Bytes(), parts[0].Doc.Modified)
	if err != nil {
		return MetadataDocument{}, err
	}
	if v := out.model.Versioning; v != nil && !m.SnapshotVersions {
		v.SnapshotVersions = nil
	}
	for _, part := range parts[1:] {
		next, err := ParseMetadata(part.Doc.Bytes(), part.Doc.Modified)
		if err != nil {
			return MetadataDocument{}, err
		}
		if next.Modified.After(out.Modified) {
			out.Modified = next.Modified
		}
		out.model.Plugins = virtual.UnionBy([][]plugin{out.model.Plugins, next.model.Plugins}, func(p plugin) string { return p.Prefix })
		if next.model.Versioning != nil {
			if out.model.Versioning == nil {
				out.model.Versioning = &versioning{}
			}
			mergeVersioning(out.model.Versioning, next.model.Versioning, m.SnapshotVersions)
		}
		if v := out.model.Versioning; v != nil && len(v.Versions) > 0 {
			sortMetadataVersions(v.Versions)
			v.Latest = v.Versions[len(v.Versions)-1]
			for _, version := range v.Versions {
				if !strings.HasSuffix(version, "-SNAPSHOT") {
					v.Release = version
				}
			}
		}
	}
	return out, nil
}

func mergeVersioning(dst, src *versioning, snapshots bool) {
	dst.Versions = virtual.UnionBy([][]string{dst.Versions, src.Versions}, func(s string) string { return s })
	if dst.LastUpdated == "null" {
		dst.LastUpdated = ""
	}
	stamp := src.LastUpdated
	if stamp == "" || stamp == "null" {
		stamp = dst.LastUpdated
	}
	newerMetadata := dst.LastUpdated == "" || stamp >= dst.LastUpdated
	if newerMetadata {
		dst.LastUpdated = stamp
		if src.Latest != "" {
			dst.Latest = src.Latest
		}
		if src.Release != "" {
			dst.Release = src.Release
		}
	}
	// Apply the interceptor's intended build-number preference independently
	// of timestamps, including an older document with a newer snapshot build.
	if src.Snapshot != nil {
		switch {
		case dst.Snapshot == nil:
			copy := *src.Snapshot
			dst.Snapshot = &copy
		case src.Snapshot.BuildNumber > dst.Snapshot.BuildNumber:
			dst.Snapshot.Timestamp, dst.Snapshot.BuildNumber = src.Snapshot.Timestamp, src.Snapshot.BuildNumber
			if newerMetadata {
				dst.Snapshot.LocalCopy = src.Snapshot.LocalCopy
			}
		case newerMetadata:
			if src.Snapshot.BuildNumber == dst.Snapshot.BuildNumber {
				dst.Snapshot.Timestamp = src.Snapshot.Timestamp
			}
			dst.Snapshot.LocalCopy = src.Snapshot.LocalCopy
		}
	}
	if !snapshots {
		dst.SnapshotVersions = nil
		return
	}
	if len(dst.SnapshotVersions) == 0 {
		dst.SnapshotVersions = slices.Clone(src.SnapshotVersions)
		return
	}
	for i := range dst.SnapshotVersions {
		old := &dst.SnapshotVersions[i]
		for _, newer := range src.SnapshotVersions {
			if metadataKeyEqual(old.Classifier, newer.Classifier) && metadataKeyEqual(old.Extension, newer.Extension) && snapshotBuild(newer.Value) > snapshotBuild(old.Value) {
				old.Value, old.Updated = newer.Value, newer.Updated
			}
		}
	}
}

func metadataKeyEqual(a, b string) bool {
	return a == b || strings.TrimSpace(a) == "" && strings.TrimSpace(b) == ""
}

func snapshotBuild(v string) int {
	n, _ := strconv.Atoi(v[strings.LastIndex(v, "-")+1:])
	return n
}

func sortMetadataVersions(versions []string) {
	slices.SortStableFunc(versions, model.CompareMercury)
	// Mercury is not transitive for all strings. Go's sorter does not throw
	// Java's comparator-contract exception, so detect a contradictory order
	// and use the same natural-string fallback.
	if !slices.IsSortedFunc(versions, model.CompareMercury) {
		slices.Sort(versions)
	}
}

// SupportsSnapshotVersions matches RequestResponseHelper's legacy clients.
func SupportsSnapshotVersions(agents []string) bool {
	for _, ua := range agents {
		if strings.HasPrefix(ua, "Wharf Ivy/") || strings.HasPrefix(ua, "Apache Ivy/") {
			return false
		}
	}
	return len(agents) == 0 || !legacyJavaAgent.MatchString(agents[0])
}

// SnapshotMetadata identifies the path the 7.x interceptor treats as version
// metadata; artifact-level metadata never carries snapshotVersions.
func SnapshotMetadata(p string) bool { return strings.HasSuffix(p, "-SNAPSHOT/maven-metadata.xml") }
