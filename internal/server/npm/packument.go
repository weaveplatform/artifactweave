package npm

import (
	"crypto/sha1" //nolint:gosec // 7.x's packument ETag is a SHA-1
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
	// rules are the format's settings, by which a read resolves latest.
	rules npmfmt.Settings
}

// version is one indexed version: the index's cleaned version string and
// the document the npm format keeps for it.
type version struct {
	number string
	meta   npmfmt.Metadata
}

const latestTag = npmfmt.TagLatest

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
		writeConditional(w, r, abbreviatedType, doc)
		return
	}
	doc, err := p.full(base)
	if err != nil {
		h.k.Error(w, r, err)
		return
	}
	writeConditional(w, r, jsonType, doc)
}

// writeConditional answers a packument with X-Checksum-Sha1 and an ETag
// equal to it, unquoted, as 7.x sends them (RTFACT-30566), and answers 304
// with both when If-None-Match names that ETag (RTFACT-19423). The checksum
// is of the body sent: a packument is built for each request, from the
// versions its caller may read and the request's base URL, so its bytes
// are what identify it. [I] Both captures are of remote or virtual
// repositories, and what 7.x's checksum covers is unknown (#216).
func writeConditional(w http.ResponseWriter, r *http.Request, contentType string, doc any) {
	body := append(encode(doc), '\n')
	sum := sha1.Sum(body) //nolint:gosec // an ETag, as 7.x computes it, not a security check
	tag := hex.EncodeToString(sum[:])
	w.Header().Set("X-Checksum-Sha1", tag)
	w.Header().Set("ETag", tag)
	if noneMatch(r.Header.Get("If-None-Match"), tag) {
		w.WriteHeader(http.StatusNotModified)
		return
	}
	w.Header().Set("Content-Type", contentType)
	_, _ = w.Write(body)
}

// noneMatch reports whether the If-None-Match header h names tag or is
// "*". Each entry is compared with any weak prefix and quotes taken off,
// since 7.x sends the ETag unquoted and a client or proxy may send it back
// either way.
func noneMatch(h, tag string) bool {
	for entry := range strings.SplitSeq(h, ",") {
		entry = strings.TrimSpace(entry)
		if entry == "*" {
			return true
		}
		entry = strings.Trim(strings.TrimPrefix(entry, "W/"), `"`)
		if entry == tag {
			return true
		}
	}
	return false
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
	fields, err := manifestFields(v)
	if err != nil {
		h.k.Error(w, r, err)
		return
	}
	writeJSON(w, http.StatusOK, jsonType, p.document(fields, v, tarballBase(r)))
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
	p := pkg{repoKey: key, name: name, created: found.Created, rules: h.format}
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
// should several (the format moves a tag, so only a race leaves two). The
// format stores latest as it stores any tag (decision 0034), so a read
// serves it as stored. A tag on a version the caller may not read is not
// shown; when that hides latest, or no version carries it, latest resolves
// among the versions the caller may read as the format settles it, and
// nothing is written.
func (p pkg) distTags() map[string]string {
	tags := map[string]string{}
	for _, v := range p.versions {
		for _, t := range v.meta.Tags {
			tags[t] = v.number
		}
	}
	if _, stored := tags[latestTag]; !stored {
		cs := make([]npmfmt.Candidate, 0, len(p.versions))
		for _, v := range p.versions {
			cs = append(cs, npmfmt.Candidate{Version: v.number, Published: v.meta.Published})
		}
		tags[latestTag] = p.rules.Latest(cs)
	}
	return tags
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

// rev is every packument's _rev, as 7.x answers it in every capture
// (RTFACT-30472, RTFACT-29708; decision 0034). npm sends it back in an
// unpublish's -rev/ path (RTFACT-17749), so being constant it cannot show
// that the client's view is stale. [I] Captures exist only of packages
// with one version (#216).
const rev = "1-0"

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

// fullPackument is the full packument (decision 0034), with the fields of
// 7.x's (RTFACT-30472) in its order. Each version is its manifest as
// document builds it; description, author and license are the latest
// version's, as its package.json has them. omitempty leaves out only a
// field package.json lacks: an empty description is a pointer to "", which
// 7.x sends as "description" : "" (RTFACT-30472's second packument), and
// an empty author or license is the raw "" itself.
//
//nolint:tagliatelle // npm's registry names
type fullPackument struct {
	ID          string                     `json:"_id"`
	Rev         string                     `json:"_rev"`
	Name        string                     `json:"name"`
	Description *string                    `json:"description,omitempty"`
	DistTags    map[string]string          `json:"dist-tags"`
	Versions    map[string]json.RawMessage `json:"versions"`
	Author      json.RawMessage            `json:"author,omitempty"`
	// Time holds unpublished as null, which 7.x sends with no version
	// unpublished; a string is any other entry's value.
	Time map[string]any `json:"time"`
	// Users is always empty: nothing here stars a package.
	Users   struct{}        `json:"users"`
	License json.RawMessage `json:"license,omitempty"`
}

func (p pkg) full(base string) (fullPackument, error) {
	tags := p.distTags()
	doc := fullPackument{
		ID: p.name, Rev: rev, Name: p.name, DistTags: tags,
		Versions: map[string]json.RawMessage{},
		Time: map[string]any{
			"unpublished": nil, "created": stamp(p.created), "modified": stamp(p.modified()),
		},
	}
	for _, v := range p.versions {
		fields, err := manifestFields(v)
		if err != nil {
			return fullPackument{}, err
		}
		if v.number == tags[latestTag] {
			doc.Description = description(fields["description"])
			doc.Author, doc.License = fields["author"], fields["license"]
		}
		doc.Versions[v.number] = p.document(fields, v, base)
		doc.Time[v.number] = stamp(v.meta.Published)
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
// it: fields, the tarball's package.json, with the index's cleaned
// version, its _id, the dist of its tarball, and the deprecation its
// npm.deprecated property holds, whatever the author wrote there. It
// changes fields.
func (p pkg) document(fields map[string]json.RawMessage, v version, base string) json.RawMessage {
	fields["_id"] = jsonString(p.name + "@" + v.number)
	p.complete(fields, v, base)
	return encode(fields)
}

// complete sets what both forms of a version take from the index rather
// than from package.json: its version, its dist and its deprecation.
func (p pkg) complete(fields map[string]json.RawMessage, v version, base string) {
	fields["version"] = jsonString(v.number)
	fields["dist"] = encode(dist{
		Tarball:   base + "/" + escapePath(npmfmt.CanonicalPath(p.name, v.number)),
		Shasum:    v.meta.Dist.Shasum,
		Integrity: v.meta.Dist.Integrity,
	})
	delete(fields, "deprecated")
	if v.meta.Deprecated != "" {
		fields["deprecated"] = jsonString(v.meta.Deprecated)
	}
}

// dist is a version's dist object as 7.x gives it (RTFACT-30472), in both
// forms of the packument. [I] 7.x's abbreviated form has not been
// captured (#216).
type dist struct {
	Tarball   string `json:"tarball"`
	Shasum    string `json:"shasum"`
	Integrity string `json:"integrity"`
}

// manifestFields is v's package.json as its fields.
func manifestFields(v version) (map[string]json.RawMessage, error) {
	fields := map[string]json.RawMessage{}
	if err := json.Unmarshal(v.meta.Manifest, &fields); err != nil {
		return nil, fmt.Errorf("npm: manifest of %s: %w", v.number, err)
	}
	return fields, nil
}

// description is a manifest's description field, nil when it has none or
// one that is not a string.
func description(raw json.RawMessage) *string {
	var s string
	if raw == nil || json.Unmarshal(raw, &s) != nil {
		return nil
	}
	return &s
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
