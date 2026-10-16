package docker

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"mime"
	"regexp"
	"strings"

	"github.com/weaveplatform/artifactweave/pkg/aql"
	"github.com/weaveplatform/artifactweave/pkg/store"
	"github.com/weaveplatform/artifactweave/pkg/types"
)

// The manifest media types the registry stores (decision 0031).
const (
	MediaTypeOCIManifest    = "application/vnd.oci.image.manifest.v1+json"
	MediaTypeOCIIndex       = "application/vnd.oci.image.index.v1+json"
	MediaTypeDockerManifest = "application/vnd.docker.distribution.manifest.v2+json"
	MediaTypeDockerList     = "application/vnd.docker.distribution.manifest.list.v2+json"
)

// The image configurations whose labels become properties. Another config
// type is an artifact's (a signature, a Helm chart) and carries no labels.
const (
	mediaTypeOCIConfig    = "application/vnd.oci.image.config.v1+json"
	mediaTypeDockerConfig = "application/vnd.docker.container.image.v1+json"
)

// MaxManifestSize is the largest manifest accepted: the specification asks
// registries to take at least 4 MB, and a larger one answers 413.
const MaxManifestSize = 4 << 20

// maxConfigSize bounds how much of a config blob is read for its labels. A
// config is a few kilobytes; one larger than a manifest may be is not read.
const maxConfigSize = MaxManifestSize

// The stored manifest's file name in its tag or digest folder, which jf
// reads (research 23, 5.1).
const (
	ManifestFile     = "manifest.json"
	ListManifestFile = "list.manifest.json"
)

// The properties set on every stored manifest (decision 0031). jf finds a
// manifest by digest through ManifestDigestProperty and SHA256Property, and
// Docker clients pulling through Artifactory fail with "missing signature
// key" when one is wrong (research 23, 5.2).
const (
	ManifestProperty       = "docker.manifest"
	ManifestDigestProperty = "docker.manifest.digest"
	ManifestTypeProperty   = "docker.manifest.type"
	RepoNameProperty       = "docker.repoName"
	SHA256Property         = "sha256"
	// LabelPropertyPrefix starts the property of each label of an image's
	// config.
	LabelPropertyPrefix = "docker.label."
)

// ErrManifestInvalid is a manifest the registry refuses to store:
// MANIFEST_INVALID.
var ErrManifestInvalid = errors.New("docker: invalid manifest")

// Descriptor points at a blob or a manifest from a manifest.
//
//nolint:tagliatelle // the OCI image specification's field names
type Descriptor struct {
	MediaType    string            `json:"mediaType"`
	Digest       string            `json:"digest"`
	Size         int64             `json:"size"`
	ArtifactType string            `json:"artifactType,omitempty"`
	Annotations  map[string]string `json:"annotations,omitempty"`
}

// Manifest is a parsed image manifest, image index or Docker manifest list.
type Manifest struct {
	// MediaType is the type the manifest is stored as: the Content-Type it
	// was pushed with.
	MediaType string
	// Digest is the SHA-256 of the exact bytes pushed, in hex.
	Digest string
	// Config and Layers are an image manifest's blobs.
	Config *Descriptor
	Layers []Descriptor
	// Manifests are an index's children.
	Manifests []Descriptor
	// Subject is the manifest this one refers to, if any.
	Subject *Descriptor
}

// wire is a manifest's JSON as all four types spell it.
//
//nolint:tagliatelle // the OCI image specification's field names
type wire struct {
	SchemaVersion *int         `json:"schemaVersion"`
	MediaType     string       `json:"mediaType"`
	Config        *Descriptor  `json:"config"`
	Layers        []Descriptor `json:"layers"`
	Manifests     []Descriptor `json:"manifests"`
	Subject       *Descriptor  `json:"subject"`
}

// Index reports whether the manifest is an image index or manifest list.
func (m Manifest) Index() bool {
	return m.MediaType == MediaTypeOCIIndex || m.MediaType == MediaTypeDockerList
}

// File is the name the manifest is stored under in its folder.
func (m Manifest) File() string {
	if m.Index() {
		return ListManifestFile
	}
	return ManifestFile
}

// Blobs are the digests of the blobs that must be in the image before an
// image manifest is stored: its config and its layers, once each, in the
// manifest's order. Non-distributable and foreign layers live elsewhere by
// design and are not required (decision 0031).
func (m Manifest) Blobs() []string {
	var out []string
	seen := map[string]bool{}
	add := func(d Descriptor) {
		if !seen[d.Digest] {
			seen[d.Digest] = true
			out = append(out, d.Digest)
		}
	}
	if m.Config != nil {
		add(*m.Config)
	}
	for _, l := range m.Layers {
		if !elsewhere(l.MediaType) {
			add(l)
		}
	}
	return out
}

// elsewhere reports whether a layer of media type t is one an image may
// reference without the registry holding it.
func elsewhere(t string) bool {
	return strings.HasPrefix(t, "application/vnd.oci.image.layer.nondistributable.") ||
		t == "application/vnd.docker.image.rootfs.foreign.diff.tar.gzip"
}

// ParseManifest parses body, pushed with contentType, as the manifest whose
// SHA-256 is hex. It refuses with ErrManifestInvalid a schema-1 manifest
// (DV-034), any type but the four stored, a body that does not parse, a
// mediaType field that disagrees with contentType, and descriptors without
// a valid digest. Without a Content-Type the body's own mediaType names
// the type.
func ParseManifest(contentType string, body []byte, hex string) (Manifest, error) {
	t := ""
	if contentType != "" {
		parsed, _, err := mime.ParseMediaType(contentType)
		if err != nil {
			return Manifest{}, fmt.Errorf("%w: Content-Type %q does not parse",
				ErrManifestInvalid, contentType)
		}
		t = parsed
	}
	var w wire
	dec := json.NewDecoder(bytes.NewReader(body))
	if err := dec.Decode(&w); err != nil || dec.More() {
		return Manifest{}, fmt.Errorf("%w: the body is not a JSON manifest", ErrManifestInvalid)
	}
	if t == "" {
		t = w.MediaType
	}
	switch t {
	case MediaTypeOCIManifest, MediaTypeOCIIndex, MediaTypeDockerManifest, MediaTypeDockerList:
	case "application/vnd.docker.distribution.manifest.v1+json",
		"application/vnd.docker.distribution.manifest.v1+prettyjws":
		return Manifest{}, fmt.Errorf(
			"%w: schema 1 manifests are not accepted; push the image with a current client",
			ErrManifestInvalid)
	default:
		return Manifest{}, fmt.Errorf("%w: unsupported manifest media type %q",
			ErrManifestInvalid, t)
	}
	if w.MediaType != "" && w.MediaType != t {
		return Manifest{}, fmt.Errorf("%w: mediaType %q does not match Content-Type %q",
			ErrManifestInvalid, w.MediaType, t)
	}
	if w.SchemaVersion == nil || *w.SchemaVersion != 2 {
		return Manifest{}, fmt.Errorf("%w: schemaVersion must be 2", ErrManifestInvalid)
	}
	m := Manifest{
		MediaType: t, Digest: hex, Config: w.Config, Layers: w.Layers,
		Manifests: w.Manifests, Subject: w.Subject,
	}
	if !m.Index() && m.Config == nil {
		return Manifest{}, fmt.Errorf("%w: an image manifest needs a config", ErrManifestInvalid)
	}
	if m.Index() && (m.Config != nil || len(m.Layers) > 0) {
		return Manifest{}, fmt.Errorf("%w: an index has no config or layers", ErrManifestInvalid)
	}
	for _, d := range m.descriptors() {
		if !ValidDigest(d.Digest) {
			return Manifest{}, fmt.Errorf("%w: invalid descriptor digest %q",
				ErrManifestInvalid, d.Digest)
		}
	}
	return m, nil
}

// descriptors are every descriptor in the manifest.
func (m Manifest) descriptors() []Descriptor {
	var out []Descriptor
	if m.Config != nil {
		out = append(out, *m.Config)
	}
	out = append(out, m.Layers...)
	out = append(out, m.Manifests...)
	if m.Subject != nil {
		out = append(out, *m.Subject)
	}
	return out
}

// ConfigType is the media type of an image manifest's config, "" for an
// index.
func (m Manifest) ConfigType() string {
	if m.Config == nil {
		return ""
	}
	return m.Config.MediaType
}

// Properties are the properties of the manifest stored in folder of the
// image name: the five jf and Docker clients rely on, and a
// docker.label.<label> for each of the image's labels. A label whose name
// no property key may carry is left out.
func Properties(m Manifest, name, folder string, labels map[string]string) types.Properties {
	p := types.Properties{}
	p.Set(ManifestProperty, folder)
	p.Set(ManifestDigestProperty, "sha256:"+m.Digest)
	p.Set(ManifestTypeProperty, m.MediaType)
	p.Set(RepoNameProperty, name)
	p.Set(SHA256Property, m.Digest)
	for k, v := range labels {
		key := LabelPropertyPrefix + k
		if types.ValidatePropertyKey(key) == nil {
			p.Set(key, v)
		}
	}
	return p
}

// Labels reads the labels of an image config of media type configType
// from r. A config of another type, or one too large or malformed to read,
// has none: labels are metadata, and a push never fails for them.
func Labels(configType string, r io.Reader) map[string]string {
	if configType != mediaTypeOCIConfig && configType != mediaTypeDockerConfig {
		return nil
	}
	raw, err := io.ReadAll(io.LimitReader(r, maxConfigSize+1))
	if err != nil || len(raw) > maxConfigSize {
		return nil
	}
	var c struct {
		Config struct {
			Labels map[string]string `json:"Labels"` //nolint:tagliatelle // Docker's config names it so
		} `json:"config"`
	}
	if json.Unmarshal(raw, &c) != nil {
		return nil
	}
	return c.Config.Labels
}

// digestRE is the specification's digest grammar: an algorithm and its
// encoded value.
var (
	digestRE = regexp.MustCompile(`^[a-z0-9]+(?:[+._-][a-z0-9]+)*:[a-zA-Z0-9=_-]+$`)
	hexRE    = regexp.MustCompile(`^[a-f0-9]{64}$`)
)

// ValidDigest reports whether d is a digest by the specification's
// grammar; a sha256 digest must also be 64 lower-case hex characters.
func ValidDigest(d string) bool {
	if !digestRE.MatchString(d) {
		return false
	}
	alg, enc, _ := strings.Cut(d, ":")
	return alg != "sha256" || hexRE.MatchString(enc)
}

// SHA256Hex returns the hex of a valid sha256 digest; ok is false for
// another algorithm or an invalid digest.
func SHA256Hex(d string) (hex string, ok bool) {
	enc, found := strings.CutPrefix(d, "sha256:")
	if !found || !hexRE.MatchString(enc) {
		return "", false
	}
	return enc, true
}

// tagRE is the specification's tag grammar.
var tagRE = regexp.MustCompile(`^[a-zA-Z0-9_][a-zA-Z0-9._-]{0,127}$`)

// ValidTag reports whether tag is a tag by the specification's grammar.
func ValidTag(tag string) bool { return tagRE.MatchString(tag) }

// ReservedTag reports whether a valid tag would collide with the folders
// the layout keeps beside tag folders: _uploads, as on Artifactory, and a
// digest folder's sha256__ name (decision 0031), which the grammar allows
// as a tag but would read back as a push by digest.
func ReservedTag(tag string) bool {
	return tag == UploadsFolder || strings.HasPrefix(tag, blobPrefix)
}

// DigestFolder is the folder below an image of the manifest pushed by its
// digest, whose SHA-256 is hex.
func DigestFolder(hex string) string { return blobPrefix + hex }

// FindManifest returns the stored manifest whose SHA-256 is hex in one of
// the image name's own folders, a tag folder or a digest folder, each a
// direct child of name, as FindBlob finds blobs. store.ErrNotFound when
// there is none.
func FindManifest(
	ctx context.Context,
	items store.ItemStore,
	repoKey, name, hex string,
) (types.Item, error) {
	q := aql.Query{Criteria: aql.And{
		aql.RepoSet{Refs: []aql.RepoRef{{Key: repoKey}}},
		&aql.FieldCriterion{Field: aql.FieldType, Op: aql.OpEq, Text: "file"},
		&aql.FieldCriterion{Field: aql.FieldSHA256, Op: aql.OpEq, Text: hex},
		aql.Or{
			&aql.FieldCriterion{Field: aql.FieldName, Op: aql.OpEq, Text: ManifestFile},
			&aql.FieldCriterion{Field: aql.FieldName, Op: aql.OpEq, Text: ListManifestFile},
		},
		&aql.FieldCriterion{Field: aql.FieldPath, Op: aql.OpMatch, Text: name + "/*"},
		&aql.FieldCriterion{
			Field: aql.FieldDepth, Op: aql.OpEq, Number: int64(strings.Count(name, "/") + 3),
		},
	}, Limit: 1, HasLimit: true}
	hits, err := items.Search(ctx, &q, aql.Options{})
	if err != nil {
		return types.Item{}, fmt.Errorf("docker: find manifest %s in %s: %w", hex, name, err)
	}
	if len(hits) == 0 {
		return types.Item{}, fmt.Errorf("docker: manifest %s in %s: %w",
			hex, name, store.ErrNotFound)
	}
	return hits[0].Item, nil
}
