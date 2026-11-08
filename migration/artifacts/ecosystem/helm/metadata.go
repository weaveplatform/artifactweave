package helm

import (
	"bytes"
	"cmp"
	"encoding/json"
	"fmt"
	"time"

	"github.com/weaveplatform/artifactweave/pkg/types"
)

// Metadata is a version's document in the package index
// (PackageVersion.Metadata, decision 0039): what index.yaml's entry for the
// version is built from, so building it opens no archive. A version's files
// are every chart archive in the repository with its name and version; the
// document is the newest one's. Everything in it follows from that archive
// and its item, so a reindex writes the same bytes.
type Metadata struct {
	// Chart is the newest archive's Chart.yaml.
	Chart Chart `json:"chart"`
	// Path is the archive's path from the repository root, which an index
	// entry's URL names.
	Path string `json:"path"`
	// Digest is the archive's SHA-256 in hex, index.yaml's digest.
	Digest string `json:"digest"`
	// Created is when the archive was last deployed, index.yaml's created:
	// its item's last-modified time, which a copy and a move keep, in UTC
	// to the microsecond the stores keep.
	Created time.Time `json:"created"`
}

// DecodeMetadata reads a version's Metadata.
func DecodeMetadata(b []byte) (Metadata, error) {
	var m Metadata
	if err := json.Unmarshal(b, &m); err != nil {
		return m, fmt.Errorf("helm: version metadata: %w", err)
	}
	return m, nil
}

// metadataOf is the Metadata of c read from the archive it.
func metadataOf(c *Chart, it types.Item) Metadata {
	return Metadata{Chart: *c, Path: it.FullPath(), Digest: it.Actual.SHA256, Created: deployed(it)}
}

// encode is m as stored: the same Metadata always gives the same bytes,
// which is what lets a reindex leave the index unchanged.
func (m Metadata) encode() []byte {
	var b bytes.Buffer
	e := json.NewEncoder(&b)
	// A chart's description and URLs keep their characters.
	e.SetEscapeHTML(false)
	// A Chart holds strings, booleans and what jsonable left of
	// import-values, all of which marshal.
	_ = e.Encode(m)
	return bytes.TrimSuffix(b.Bytes(), []byte("\n"))
}

// deployed is when the archive it was last deployed.
func deployed(it types.Item) time.Time {
	return it.Modified.UTC().Truncate(time.Microsecond)
}

// newestFirst orders archives of one version newest first: the one
// deployed later, as 7.x "retains only the latest entry"
// (kubernetes-helm-chart-repositories, "Prevent Duplicate Chart Paths").
// A copy keeps its source's time, so two archives can tie; the item made
// later, with the greater ID, comes first then, which keeps the choice the
// same on every store and in every reindex.
func newestFirst(a, b types.Item) int {
	if c := deployed(b).Compare(deployed(a)); c != 0 {
		return c
	}
	return cmp.Compare(b.ID, a.ID)
}
