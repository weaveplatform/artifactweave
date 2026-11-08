package npm

import (
	"bytes"
	"encoding/base64"
	"encoding/hex"
	"encoding/json"
	"fmt"
	"slices"
	"time"

	"github.com/weaveplatform/artifactweave/pkg/types"
)

// Metadata is a version's document in the package index
// (PackageVersion.Metadata, decision 0034): what packuments and search are
// built from, so a read needs neither the tarball nor its properties.
// Everything in it follows from the tarball, its stored checksums and its
// properties, so a reindex writes the same bytes.
type Metadata struct {
	// Manifest is the tarball's package.json, compacted. Its version is as
	// the author wrote it; the index's version is the cleaned one.
	Manifest json.RawMessage `json:"manifest"`
	Dist     Dist            `json:"dist"`
	// HasInstallScript is Tarball.HasInstallScript.
	HasInstallScript bool `json:"has_install_script,omitempty"`
	// Tags are the dist-tags the tarball carries (npm.disttag), sorted.
	Tags []string `json:"tags,omitempty"`
	// Deprecated is the tarball's npm.deprecated message.
	Deprecated string `json:"deprecated,omitempty"`
	// Published is when the tarball was last deployed, in UTC to the
	// microsecond the stores keep.
	Published time.Time `json:"published"`
}

// Dist is a version's dist object, less the tarball URL, which a packument
// builds from the base URL of each request. 7.x's dist has nothing else
// (RTFACT-30472): npm's own registry adds fileCount and unpackedSize, which
// no JFrog client reads.
type Dist struct {
	// Shasum is the stored SHA-1, in hex.
	Shasum string `json:"shasum"`
	// Integrity is the stored SHA-512 in Subresource Integrity form.
	Integrity string `json:"integrity"`
}

// DecodeMetadata reads a version's Metadata.
func DecodeMetadata(b []byte) (Metadata, error) {
	var m Metadata
	if err := json.Unmarshal(b, &m); err != nil {
		return m, fmt.Errorf("npm: version metadata: %w", err)
	}
	return m, nil
}

// metadataOf is the Metadata of t stored as it with props.
func metadataOf(t *Tarball, it types.Item, props types.Properties) Metadata {
	m := Metadata{
		Manifest:         t.Manifest,
		Dist:             Dist{Shasum: it.Actual.SHA1, Integrity: integrity(it.Actual.SHA512)},
		HasInstallScript: t.HasInstallScript,
		Published:        published(it),
	}
	m.annotate(props)
	return m
}

// annotate takes the version state npm changes after a publish from the
// tarball's properties.
func (m *Metadata) annotate(props types.Properties) {
	m.Tags = nil
	for _, t := range props[PropDistTag] {
		if t != "" && !slices.Contains(m.Tags, t) {
			m.Tags = append(m.Tags, t)
		}
	}
	slices.Sort(m.Tags)
	m.Deprecated = ""
	for _, d := range props[PropDeprecated] {
		if d != "" {
			m.Deprecated = d
			break
		}
	}
}

// encode is m as stored: the same Metadata always gives the same bytes,
// which is what lets a reindex leave the index unchanged.
func (m Metadata) encode() []byte {
	return jsonText(m)
}

// jsonText marshals v without escaping HTML, so a package.json keeps its
// characters, and without the encoder's newline.
func jsonText(v any) []byte {
	var b bytes.Buffer
	e := json.NewEncoder(&b)
	e.SetEscapeHTML(false)
	// Every value given is a Metadata or a []string, which always marshal.
	_ = e.Encode(v)
	return bytes.TrimSuffix(b.Bytes(), []byte("\n"))
}

// published is when the tarball it was last deployed: its version's time.
// A move keeps it and a copy is a deploy at its target.
func published(it types.Item) time.Time {
	return it.Modified.UTC().Truncate(time.Microsecond)
}

// integrity is a SHA-512 as npm's dist.integrity. Every upload path stores
// one, in hex (#296).
func integrity(sha512 string) string {
	raw, _ := hex.DecodeString(sha512)
	return "sha512-" + base64.StdEncoding.EncodeToString(raw)
}
