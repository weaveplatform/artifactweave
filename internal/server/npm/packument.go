package npm

import (
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"net/http"
	"net/url"
	"slices"
	"strings"
	"time"

	npmfmt "github.com/weaveplatform/artifactweave/internal/format/npm"
	"github.com/weaveplatform/artifactweave/internal/server/apierror"
	"github.com/weaveplatform/artifactweave/internal/server/baseurl"
	"github.com/weaveplatform/artifactweave/internal/server/pkgroute"
	"github.com/weaveplatform/artifactweave/pkg/store"
	"github.com/weaveplatform/artifactweave/pkg/types"
)

// pkg is a package as one caller sees it: the versions whose tarballs it
// may read, oldest first by SemVer, as the index orders them.
type pkg struct {
	repoKey, name string
	created       time.Time
	versions      []version
}

// version is one indexed version: the index's cleaned version string and
// the document the npm format keeps for it.
type version struct {
	number string
	meta   npmfmt.Metadata
}

const latestTag = "latest"

// packument answers the full packument, or the abbreviated one when the
// request asks for it.
func (h handler) packument(w http.ResponseWriter, r *http.Request, name string) {
	p, ok := h.load(w, r, name)
	if !ok {
		return
	}
	base := tarballBase(r)
	if abbreviated(r) {
		doc, err := p.abbreviated(base)
		if err != nil {
			h.k.Error(w, r, err)
			return
		}
		writeJSON(w, abbreviatedType, doc)
		return
	}
	doc, err := p.full(base)
	if err != nil {
		h.k.Error(w, r, err)
		return
	}
	writeJSON(w, jsonType, doc)
}

// manifest answers one version's document, the version named by its
// number or by a dist-tag.
func (h handler) manifest(w http.ResponseWriter, r *http.Request, name, spec string) {
	p, ok := h.load(w, r, name)
	if !ok {
		return
	}
	v, ok := p.resolve(spec)
	if !ok {
		h.k.Error(w, r, apierror.New(http.StatusNotFound,
			fmt.Sprintf("version not found: %s@%s", name, spec)))
		return
	}
	doc, err := p.document(v, tarballBase(r))
	if err != nil {
		h.k.Error(w, r, err)
		return
	}
	writeJSON(w, jsonType, doc)
}

// load reads the package called name with the versions the caller may
// read, having answered when there is nothing to show it. A caller that
// may not read the package's folder is refused whether or not the package
// exists, as the item routes refuse a path; one that may but finds no
// version it can read gets 404.
func (h handler) load(w http.ResponseWriter, r *http.Request, name string) (pkg, bool) {
	if !h.k.RequireFolder(w, r, name, types.ActionRead) {
		return pkg{}, false
	}
	p, ok, err := h.readable(r, name)
	switch {
	case err != nil:
		h.k.Error(w, r, err)
		return pkg{}, false
	case !ok:
		h.k.Error(w, r, apierror.New(http.StatusNotFound, "package not found: "+name))
		return pkg{}, false
	}
	return p, true
}

// readable reads the package called name from the index, keeping the
// versions whose tarball, at its canonical path, the caller may read; ok
// is false when no version is left.
func (h handler) readable(r *http.Request, name string) (pkg, bool, error) {
	ctx := r.Context()
	st := h.k.DB()
	key := pkgroute.RequestFrom(r).Repo.Key
	found, err := st.Packages().Find(ctx, key, name)
	if errors.Is(err, store.ErrNotFound) {
		return pkg{}, false, nil
	}
	if err != nil {
		return pkg{}, false, fmt.Errorf("npm: package %s: %w", name, err)
	}
	all, err := st.Packages().Versions(ctx, found.ID)
	if err != nil {
		return pkg{}, false, fmt.Errorf("npm: versions of %s: %w", name, err)
	}
	p := pkg{repoKey: key, name: name, created: found.Created}
	for _, v := range all {
		if !h.k.Can(r, npmfmt.CanonicalPath(name, v.Version), types.ActionRead) {
			continue
		}
		m, err := npmfmt.DecodeMetadata(v.Metadata)
		if err != nil {
			return pkg{}, false, fmt.Errorf("npm: %s@%s: %w", name, v.Version, err)
		}
		p.versions = append(p.versions, version{number: v.Version, meta: m})
	}
	return p, len(p.versions) > 0, nil
}

// distTags maps each dist-tag to the version that carries it, the highest
// should several (the format moves a tag, so only a race leaves two), and
// latest to the version decision 0034 resolves on every read: the one
// pinned with npm.disttag=latest, else the highest that is no pre-release,
// else the highest. A tag on a version the caller may not read is not
// shown, and latest then resolves among the versions it may.
func (p pkg) distTags() map[string]string {
	tags := map[string]string{}
	for _, v := range p.versions {
		for _, t := range v.meta.Tags {
			tags[t] = v.number
		}
	}
	if _, pinned := tags[latestTag]; !pinned {
		tags[latestTag] = p.highest()
	}
	return tags
}

// highest is the highest version that is no pre-release, or the highest
// when every version is one. Build metadata is gone from the index's
// versions, so a hyphen can only start a pre-release.
func (p pkg) highest() string {
	for _, v := range slices.Backward(p.versions) {
		if !strings.Contains(v.number, "-") {
			return v.number
		}
	}
	return p.versions[len(p.versions)-1].number
}

// resolve finds the version spec names: a dist-tag, or a version that
// cleans to one, as npm's semver.clean does.
func (p pkg) resolve(spec string) (version, bool) {
	want, ok := p.distTags()[spec]
	if !ok {
		if want, ok = npmfmt.CleanVersion(spec); !ok {
			return version{}, false
		}
	}
	i := slices.IndexFunc(p.versions, func(v version) bool { return v.number == want })
	if i < 0 {
		return version{}, false
	}
	return p.versions[i], true
}

// rev is the packument's _rev (decision 0034): the number of versions and
// the first 32 hex digits of SHA-256 over their strings, sorted and each
// ended by a newline. It moves when a version is published or removed and
// not when a tag or a deprecation changes, and an unpublish (#290) checks
// it against the same versions, those its caller sees.
func (p pkg) rev() string {
	numbers := make([]string, 0, len(p.versions))
	for _, v := range p.versions {
		numbers = append(numbers, v.number)
	}
	slices.Sort(numbers)
	h := sha256.New()
	for _, n := range numbers {
		_, _ = h.Write([]byte(n + "\n"))
	}
	return fmt.Sprintf("%d-%s", len(numbers), hex.EncodeToString(h.Sum(nil))[:32])
}

// modified is when a version shown was last deployed.
func (p pkg) modified() time.Time {
	var at time.Time
	for _, v := range p.versions {
		if v.meta.Published.After(at) {
			at = v.meta.Published
		}
	}
	return at
}

// fullPackument is the full packument (decision 0034). Each version is its
// manifest as document builds it.
//
//nolint:tagliatelle // npm's registry names
type fullPackument struct {
	ID          string                     `json:"_id"`
	Rev         string                     `json:"_rev"`
	Name        string                     `json:"name"`
	Description string                     `json:"description,omitempty"`
	DistTags    map[string]string          `json:"dist-tags"`
	Versions    map[string]json.RawMessage `json:"versions"`
	Time        map[string]string          `json:"time"`
}

func (p pkg) full(base string) (fullPackument, error) {
	tags := p.distTags()
	doc := fullPackument{
		ID: p.name, Rev: p.rev(), Name: p.name, DistTags: tags,
		Versions: map[string]json.RawMessage{},
		Time: map[string]string{
			"created": stamp(p.created), "modified": stamp(p.modified()),
		},
	}
	for _, v := range p.versions {
		b, err := p.document(v, base)
		if err != nil {
			return fullPackument{}, err
		}
		doc.Versions[v.number] = b
		doc.Time[v.number] = stamp(v.meta.Published)
		if v.number == tags[latestTag] {
			doc.Description = description(v.meta.Manifest)
		}
	}
	return doc, nil
}

// abbreviatedPackument is the form npm installs from: per version only
// what resolving and installing it needs (R25 3).
//
//nolint:tagliatelle // npm's registry names
type abbreviatedPackument struct {
	Name     string                     `json:"name"`
	Modified string                     `json:"modified"`
	DistTags map[string]string          `json:"dist-tags"`
	Versions map[string]json.RawMessage `json:"versions"`
}

// abbreviatedFields are the manifest fields an abbreviated version keeps:
// those npm's package-metadata document lists for it, with the
// dependency fields under both spellings of bundled dependencies, and libc,
// which npm checks with os and cpu before installing.
var abbreviatedFields = []string{
	"name", "dependencies", "optionalDependencies", "devDependencies",
	"peerDependencies", "peerDependenciesMeta", "bundleDependencies", "bundledDependencies",
	"acceptDependencies", "bin", "directories", "engines", "os", "cpu", "libc",
}

func (p pkg) abbreviated(base string) (abbreviatedPackument, error) {
	doc := abbreviatedPackument{
		Name: p.name, Modified: stamp(p.modified()), DistTags: p.distTags(),
		Versions: map[string]json.RawMessage{},
	}
	for _, v := range p.versions {
		fields, err := manifestFields(v)
		if err != nil {
			return abbreviatedPackument{}, err
		}
		short := map[string]json.RawMessage{}
		for _, f := range abbreviatedFields {
			if raw, ok := fields[f]; ok {
				short[f] = raw
			}
		}
		if v.meta.HasInstallScript {
			short["hasInstallScript"] = json.RawMessage(`true`)
		}
		p.complete(short, v, base)
		doc.Versions[v.number] = encode(short)
	}
	return doc, nil
}

// document is v's manifest as the full packument and a version read give
// it: the tarball's package.json, with the index's cleaned version, its
// _id, the dist of its tarball, and the deprecation its npm.deprecated
// property holds, whatever the author wrote there.
func (p pkg) document(v version, base string) (json.RawMessage, error) {
	fields, err := manifestFields(v)
	if err != nil {
		return nil, err
	}
	fields["_id"] = jsonString(p.name + "@" + v.number)
	p.complete(fields, v, base)
	return encode(fields), nil
}

// complete sets what both forms of a version take from the index rather
// than from package.json: its version, its dist and its deprecation.
func (p pkg) complete(fields map[string]json.RawMessage, v version, base string) {
	fields["version"] = jsonString(v.number)
	fields["dist"] = encode(dist{
		Shasum: v.meta.Dist.Shasum, Integrity: v.meta.Dist.Integrity,
		Tarball:   base + "/" + escapePath(npmfmt.CanonicalPath(p.name, v.number)),
		FileCount: v.meta.Dist.FileCount, UnpackedSize: v.meta.Dist.UnpackedSize,
	})
	delete(fields, "deprecated")
	if v.meta.Deprecated != "" {
		fields["deprecated"] = jsonString(v.meta.Deprecated)
	}
}

// dist is a version's dist object as npm reads it.
//
//nolint:tagliatelle // npm's registry names
type dist struct {
	Shasum       string `json:"shasum"`
	Integrity    string `json:"integrity"`
	Tarball      string `json:"tarball"`
	FileCount    int    `json:"fileCount"`
	UnpackedSize int64  `json:"unpackedSize"`
}

// manifestFields is v's package.json as its fields.
func manifestFields(v version) (map[string]json.RawMessage, error) {
	fields := map[string]json.RawMessage{}
	if err := json.Unmarshal(v.meta.Manifest, &fields); err != nil {
		return nil, fmt.Errorf("npm: manifest of %s: %w", v.number, err)
	}
	return fields, nil
}

// description is a manifest's description, when it is a string.
func description(manifest json.RawMessage) string {
	var m struct {
		Description any `json:"description"`
	}
	_ = json.Unmarshal(manifest, &m)
	s, _ := m.Description.(string)
	return s
}

// tarballBase is the URL a version's dist.tarball starts with: the
// repository's registry under the request's base URL (#315). It is built
// for each request and never stored, so a client behind any proxy gets a
// URL it can reach.
func tarballBase(r *http.Request) string {
	return baseurl.From(r.Context()) + "/artifactory/api/npm/" +
		url.PathEscape(pkgroute.RequestFrom(r).Repo.Key)
}

// escapePath escapes each segment of a path for a URL. npm names need no
// escaping but for a scope's @, which a path keeps.
func escapePath(p string) string {
	segs := strings.Split(p, "/")
	for i, s := range segs {
		segs[i] = url.PathEscape(s)
	}
	return strings.Join(segs, "/")
}

// abbreviated reports whether r asks for the abbreviated packument: its
// type comes first in Accept, as pacote sends it. A ?write=true read is
// the full one whatever it accepts, since a client that will write the
// packument back needs all of it.
func abbreviated(r *http.Request) bool {
	if r.URL.Query().Get("write") == "true" {
		return false
	}
	first, _, _ := strings.Cut(r.Header.Get("Accept"), ",")
	mediaType, _, _ := strings.Cut(first, ";")
	return strings.EqualFold(strings.TrimSpace(mediaType), abbreviatedType)
}

// stamp is a time as npm's registry writes them: UTC, to the millisecond.
func stamp(t time.Time) string {
	return t.UTC().Format("2006-01-02T15:04:05.000Z")
}
