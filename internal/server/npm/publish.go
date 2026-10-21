package npm

import (
	"bytes"
	"crypto/sha1" //nolint:gosec // npm's dist.shasum is SHA-1 by definition
	"encoding/base64"
	"encoding/hex"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"slices"
	"strings"

	npmfmt "github.com/weaveplatform/artifactweave/internal/format/npm"
	"github.com/weaveplatform/artifactweave/internal/server/apierror"
	"github.com/weaveplatform/artifactweave/internal/server/pkgroute"
	"github.com/weaveplatform/artifactweave/pkg/blob"
	"github.com/weaveplatform/artifactweave/pkg/repo"
	"github.com/weaveplatform/artifactweave/pkg/types"
)

// write answers a PUT in the registry. PUT /<name> is npm publish, or a
// packument update when the body has no attachments, and
// PUT /<name>/-rev/<rev> the step of npm unpublish that removes versions
// (decision 0034). npm's other PUTs, such as logins, answer 405 until
// their tickets serve them.
func (h handler) write(w http.ResponseWriter, r *http.Request) {
	if h.remoteReadOnly(w, r) {
		return
	}
	p, err := pkgroute.ParsePath(pkgroute.RequestFrom(r).Rest)
	if err != nil {
		h.k.Error(w, r, err)
		return
	}
	name, rest, ok := splitName(p.Segments)
	switch {
	case ok && len(rest) == 0:
		h.publish(w, r, name)
	case ok && len(rest) == 2 && rest[0] == "-rev":
		h.unpublishVersions(w, r, name)
	default:
		h.k.Error(w, r, apierror.New(http.StatusMethodNotAllowed,
			"method not allowed: "+r.Method+" "+r.URL.Path))
	}
}

// publish stores the one version a publish body carries as its tarball at
// the canonical path, through the item service, whose npm format indexes
// it. The tarball streams from the body into the blob store, and is
// checked against the body before anything is stored at the path. A body
// without attachments is a packument update instead (update).
func (h handler) publish(w http.ResponseWriter, r *http.Request, name string) {
	// Refused before the body is read, so a caller who may change nothing
	// in the package uploads no tarball first. Only the body tells a
	// publish, which needs Deploy, from a packument update, which needs
	// Annotate, so either lets the body be read. The canonical path is
	// checked once the body has named the version.
	if !h.k.CanFolder(r, name, types.ActionAnnotate) &&
		!h.k.RequireFolder(w, r, name, types.ActionWrite) {
		return
	}
	if r.ContentLength > h.maxPublish {
		h.k.Error(w, r, tooLarge(h.maxPublish))
		return
	}
	b, err := readPublish(limited{http.MaxBytesReader(w, r.Body, h.maxPublish)},
		func(data io.Reader) (blob.Info, error) { return h.k.Upload(r, data) })
	if err != nil {
		h.k.Error(w, r, err)
		return
	}
	if !b.attached {
		h.update(w, r, name, b)
		return
	}
	enforce := pkgroute.RequestFrom(r).Repo.ChecksumPolicyType != repo.ServerGeneratedChecksums
	v, err := b.version(name, enforce)
	if err != nil {
		h.k.Error(w, r, err)
		return
	}
	t, ok, err := npmfmt.ReadStored(r.Context(), h.k.Blobs(), b.info.Digests.SHA256)
	switch {
	case err != nil:
		h.k.Error(w, r, err)
		return
	case !ok:
		h.k.Error(w, r, badRequest(
			"the tarball attachment is no package tarball with a readable package.json"))
		return
	}
	clean, valid := npmfmt.CleanVersion(v.number)
	if t.Name != name || !valid || t.Version != clean {
		h.k.Error(w, r, badRequest(fmt.Sprintf(
			"the tarball's package.json is %s@%s, but the body publishes %s@%s",
			t.Name, t.Version, name, v.number)))
		return
	}
	// [I] Artifactory's message is unknown (research 25 section 10); this
	// is the public registry's.
	exists := func() error {
		return apierror.New(http.StatusForbidden, fmt.Sprintf(
			"You cannot publish over the previously published versions: %s.", t.Version))
	}
	if _, err := h.k.StoreNew(r, npmfmt.CanonicalPath(t.Name, t.Version), b.info,
		v.props(t.Version), v.declared, exists); err != nil {
		h.k.Error(w, r, err)
		return
	}
	// 201 as 7.x answers (RTFACT-24072, KB 000004147). [I] Its body is
	// unknown (#216); npm reads none.
	writeJSON(w, http.StatusCreated, jsonType, map[string]bool{"ok": true})
}

// published is the version a publish body names, with the dist-tags it
// gives that version and the tarball checksums its dist declares.
type published struct {
	number   string
	tags     map[string]string
	declared types.Checksums
}

// version checks the body's shape and its tarball attachment against the
// URL's package name and returns the version it publishes. enforce is
// whether the repository's checksum policy refuses a dist that does not
// match the tarball (checkDist).
func (b publishBody) version(name string, enforce bool) (published, error) {
	var bodyName, id string
	if json.Unmarshal(b.fields["name"], &bodyName) != nil || bodyName != name {
		return published{}, badRequest(fmt.Sprintf(
			"the body's name %s is not the package %q the URL names", b.fields["name"], name))
	}
	if raw, ok := b.fields["_id"]; ok && (json.Unmarshal(raw, &id) != nil || id != name) {
		return published{}, badRequest(fmt.Sprintf(
			"the body's _id %s is not the package %q the URL names", raw, name))
	}
	var versions map[string]json.RawMessage
	if json.Unmarshal(b.fields["versions"], &versions) != nil || len(versions) != 1 {
		return published{}, badRequest("the body must name exactly one version")
	}
	if b.tarballs != 1 || !b.stored {
		return published{}, badRequest("exactly one attachment must be a .tgz tarball, with data")
	}
	if b.length != nil {
		var n int64
		if json.Unmarshal(b.length, &n) != nil || n != b.info.Size {
			return published{}, badRequest(fmt.Sprintf(
				"the tarball attachment's length %s is not its %d bytes", b.length, b.info.Size))
		}
	}
	var p published
	var manifest json.RawMessage
	for number, m := range versions {
		p.number, manifest = number, m
	}
	declared, err := checkDist(manifest, b.info.Digests, enforce)
	if err != nil {
		return published{}, err
	}
	p.declared = declared
	var tags map[string]json.RawMessage
	_ = json.Unmarshal(b.fields["dist-tags"], &tags)
	p.tags = map[string]string{}
	for tag, raw := range tags {
		var v string
		if json.Unmarshal(raw, &v) == nil {
			p.tags[tag] = v
		}
	}
	return p, nil
}

// props are the npm.disttag values the publish stores on the tarball of
// version: the body's tags that name it, but latest, which 7.x ignores in
// a publish body (RTFACT-8184, RTFACT-26099). Every npm publish names
// latest unless --tag is given, so the npm format gives latest by its own
// rules instead: a publish without another tag takes it when its version
// outranks the version that has it (decision 0034).
func (p published) props(version string) types.Properties {
	var tags []string
	for tag, v := range p.tags {
		if clean, ok := npmfmt.CleanVersion(v); ok && clean == version &&
			tag != latestTag && tag != "" {
			tags = append(tags, tag)
		}
	}
	if len(tags) == 0 {
		return nil
	}
	slices.Sort(tags)
	return types.Properties{npmfmt.PropDistTag: tags}
}

// checkDist reads the version manifest's dist.shasum, and each hash in
// dist.integrity of an algorithm the blob store computes, as the checksums
// the client declares for the tarball, which it records as its originals.
// 7.x takes dist.shasum as the client's checksum and applies the
// repository's checksum policy to it since 7.31.10 (RTFACT-14280), so under
// client-checksums they are what the tarball's checksum files serve. npm
// sends both; a hash of another algorithm, such as sha384, is neither
// checked nor recorded.
//
// With enforce, a declared checksum that is not the tarball's answers 400.
// [I] 7.x's status for that refusal is unknown (#216). Without it, under
// server-generated-checksums, each is recorded as sent, so a mismatch shows
// in the bad-checksum search as a REST deploy's does; one too malformed to
// be a checksum of its algorithm is dropped, since a stored original must
// be one.
func checkDist(manifest json.RawMessage, d blob.Digests, enforce bool) (types.Checksums, error) {
	var doc struct {
		Dist struct {
			Shasum    string `json:"shasum"`
			Integrity string `json:"integrity"`
		} `json:"dist"`
	}
	if json.Unmarshal(manifest, &doc) != nil {
		return types.Checksums{}, badRequest(
			"the version's manifest is not an object whose dist is an object")
	}
	var declared types.Checksums
	if s := strings.ToLower(doc.Dist.Shasum); s != "" {
		raw, err := hex.DecodeString(s)
		switch {
		case s == d.SHA1:
			declared.SHA1 = s
		case enforce:
			return types.Checksums{}, badRequest(
				fmt.Sprintf(
					"dist.shasum %s is not the tarball's SHA-1 %s",
					doc.Dist.Shasum,
					d.SHA1,
				),
			)
		case err == nil && len(raw) == sha1.Size:
			declared.SHA1 = s
		}
	}
	computed := map[string]struct {
		digest   string
		declared *string
	}{
		"sha512": {d.SHA512, &declared.SHA512},
		"sha256": {d.SHA256, &declared.SHA256},
		"sha1":   {d.SHA1, &declared.SHA1},
	}
	for _, sri := range strings.Fields(doc.Dist.Integrity) {
		alg, value, _ := strings.Cut(sri, "-")
		c, ok := computed[alg]
		if !ok {
			continue
		}
		value, _, _ = strings.Cut(value, "?")
		got, err := base64.StdEncoding.DecodeString(value)
		want, _ := hex.DecodeString(c.digest)
		switch {
		case err == nil && bytes.Equal(got, want):
		case enforce:
			return types.Checksums{}, badRequest(
				fmt.Sprintf("dist.integrity's %s hash does not match the tarball", alg),
			)
		case err != nil || len(got) != len(want):
			continue
		}
		// The first value of an algorithm stands, so dist.shasum, the SHA-1
		// RTFACT-14280 names, outranks an integrity sha1.
		if *c.declared == "" {
			*c.declared = hex.EncodeToString(got)
		}
	}
	return declared, nil
}
