package puppethttp

import (
	"bytes"
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/http"
	"net/url"
	"reflect"
	"slices"
	"strconv"
	"strings"

	"github.com/weaveplatform/artifactweave/internal/access/api"
	"github.com/weaveplatform/artifactweave/internal/artifacts/adapters/http/pkgroute"
	"github.com/weaveplatform/artifactweave/internal/artifacts/api/repo"
	"github.com/weaveplatform/artifactweave/internal/artifacts/core/remote/source"
	puppetfmt "github.com/weaveplatform/artifactweave/internal/artifacts/ecosystem/puppet"
	"github.com/weaveplatform/artifactweave/internal/kernel"
	"github.com/weaveplatform/artifactweave/internal/platform/httpkit/apierror"
)

const (
	maxMetadataBytes = 16 << 20
	maxVersions      = 4096
	// upstreamPage is the page of releases a remote asks its upstream for:
	// 20, the Forge's own default page (its v3 API description;
	// RTFACT-14274). A page carries each release's readme, changelog and
	// license, which a remote's records answer, and the Forge's 100 a page
	// of theforeman-foreman came to 9,947,601 bytes on 2026-11-12, near
	// maxMetadataBytes, while puppetlabs-apache's README, CHANGELOG and
	// license in its sources come to about 240 KB a release without
	// REFERENCE.md, so 100 of them would pass it. Twenty a page keeps such a
	// module under it with room to spare (#1300). The Forge serves at most
	// 100 whatever limit is asked (it answered limit=1000 with "limit": 100
	// on 2026-11-12), so a smaller page costs only more requests. What 7.x's
	// remote asks is [I]; RTFACT-13687 shows a 5.1 remote asking its
	// upstream's search for limit=100.
	upstreamPage = 20
)

// releaseExclusions are the Forge release fields a remote asks its upstream
// to leave out of each release page, as exclude_fields: the ones no record
// answers, among them REFERENCE.md's text, which runs to 400 KB a release
// in puppetlabs-apache's sources (#1300). readme, changelog and license
// stay, since a remote's records relay them (#1288). The Forge leaves out
// what the parameter names (its v3 API description gives "readme changelog
// license reference" as the example), and a name it does not know leaves
// nothing out. Whether 7.x's remote asks the same is [I].
var releaseExclusions = []string{
	"reference", "tasks", "plans", "pdk", "validation_score", "malware_scan", "deleted_for",
}

// moduleExclusions are the Forge module fields a remote asks its upstream
// to leave out of a module record: the current release, with its readme,
// changelog, license and reference, which a remote answers from the
// release's own record instead. The release list stays: a module its
// release pages leave out is answered from it (#1370), and it holds only
// each release's version, address, size and dates. The public Forge
// answers its whole module record whatever exclude_fields asks (it
// answered puppetlabs-sqlserver's current_release and releases with
// exclude_fields=current_release releases on 2026-11-12), so the parameter
// trims only what an upstream that honours it sends.
var moduleExclusions = []string{"current_release"}

type upstreamVersion struct {
	Version    string           `json:"version"`
	FileURI    string           `json:"file_uri"`
	FileSHA256 string           `json:"file_sha256"`
	FileSize   int64            `json:"file_size"`
	Metadata   upstreamManifest `json:"metadata"`
	// Tags are relayed as the upstream gives them [I]: the Forge's include
	// tags its metadata.json does not (puppetlabs-stdlib's on 2026-11-12),
	// which a remote has no other source for.
	Tags json.RawMessage `json:"tags"`
	// The rest are relayed to the remote's records as the upstream gave
	// them, as 7.x relays the Forge's (RTFACT-19130 quotes a 6.8 remote's
	// release record carrying the Forge's module owner, gravatar_id and
	// deprecated_at). Each is read raw, so a value of another type leaves
	// that field as a hosted release has it rather than refusing the page.
	Module    json.RawMessage `json:"module"`
	Supported json.RawMessage `json:"supported"`
	FileMD5   json.RawMessage `json:"file_md5"`
	Downloads json.RawMessage `json:"downloads"`
	Readme    json.RawMessage `json:"readme"`
	Changelog json.RawMessage `json:"changelog"`
	License   json.RawMessage `json:"license"`
	CreatedAt json.RawMessage `json:"created_at"`
	UpdatedAt json.RawMessage `json:"updated_at"`
	DeletedAt json.RawMessage `json:"deleted_at"`
}

// upstreamManifest is an upstream release's metadata: the validated
// manifest, the whole document, which the release's record answers
// (#1325), and the project_page and issues_url a module record answers.
type upstreamManifest struct {
	puppetfmt.Manifest
	document               json.RawMessage
	projectPage, issuesURL string
}

// UnmarshalJSON decodes the manifest as an archive's is decoded, and reads
// its links beside it. The document is held compacted, and refused above
// puppetfmt.MaxManifestBytes, the most an archive's metadata.json may be,
// so the archive it describes could never match it.
func (m *upstreamManifest) UnmarshalJSON(data []byte) error {
	if err := json.Unmarshal(data, &m.Manifest); err != nil {
		return fmt.Errorf("puppet: upstream manifest: %w", err)
	}
	var document bytes.Buffer
	// encoding/json hands UnmarshalJSON one valid JSON value, so it compacts.
	_ = json.Compact(&document, data)
	if document.Len() > puppetfmt.MaxManifestBytes {
		return invalidUpstream()
	}
	m.document = document.Bytes()
	m.projectPage, m.issuesURL = puppetfmt.ManifestLinks(data)
	return nil
}

type upstreamPagination struct {
	Limit  int     `json:"limit"`
	Offset int     `json:"offset"`
	Total  int     `json:"total"`
	Next   *string `json:"next"`
}

type upstreamDocument struct {
	Results    []upstreamVersion   `json:"results"`
	Pagination *upstreamPagination `json:"pagination"`
}

// relayed returns the details of upstream release v.
func (v upstreamVersion) relayed() details {
	d := details{
		readme: rawText(v.Readme), changelog: rawText(v.Changelog), license: rawText(v.License),
		created: rawText(v.CreatedAt), updated: rawText(v.UpdatedAt), deleted: rawText(v.DeletedAt),
	}
	_ = json.Unmarshal(v.Supported, &d.supported)
	// A count is relayed only as a whole number that is not negative.
	var downloads int64
	if json.Unmarshal(v.Downloads, &downloads) == nil && downloads > 0 {
		d.downloads = downloads
	}
	if md5 := rawText(v.FileMD5); md5 != nil && validHex(*md5, 32) {
		d.md5 = md5
	}
	var module struct {
		DeprecatedAt json.RawMessage `json:"deprecated_at"`
		Owner        struct {
			GravatarID json.RawMessage `json:"gravatar_id"`
		} `json:"owner"`
	}
	if json.Unmarshal(v.Module, &module) == nil {
		d.deprecatedAt = rawText(module.DeprecatedAt)
		d.gravatarID = rawText(module.Owner.GravatarID)
	}
	d.tags = stringList(v.Tags)
	return d
}

// rawText returns raw's string when raw is a JSON string, and null for
// anything else, null included.
func rawText(raw json.RawMessage) *string {
	var s *string
	if json.Unmarshal(raw, &s) != nil {
		return nil
	}
	return s
}

func invalidUpstream() error {
	return apierror.New(http.StatusBadGateway, "invalid upstream Puppet metadata")
}

func parseVersions(data []byte, name, responseBase string, offset int) ([]release, bool, error) {
	if len(data) == 0 || len(data) > maxMetadataBytes {
		return nil, false, invalidUpstream()
	}
	var doc upstreamDocument
	if json.Unmarshal(data, &doc) != nil || doc.Pagination == nil || len(doc.Results) > 1000 ||
		doc.Pagination.Offset != offset || doc.Pagination.Total < offset+len(doc.Results) ||
		doc.Pagination.Total > maxVersions {
		return nil, false, invalidUpstream()
	}
	base, err := url.Parse(responseBase)
	if err != nil || !validArchiveURL(base) || !strings.HasSuffix(base.Path, "/v3/releases") {
		return nil, false, invalidUpstream()
	}
	out := make([]release, 0, len(doc.Results))
	seen := map[string]bool{}
	for _, v := range doc.Results {
		if seen[v.Version] {
			return nil, false, invalidUpstream()
		}
		seen[v.Version] = true
		rel, err := v.release(name, base, base)
		if err != nil {
			return nil, false, err
		}
		out = append(out, rel)
	}
	more := offset+len(out) < doc.Pagination.Total
	// A page with more after it that holds fewer releases than the remote
	// asked for is valid only when the upstream's own limit says it serves
	// pages that small. Short for any other reason, it leaves the remote
	// unable to tell where the next page starts, since the upstream's
	// offsets may count releases the page left out. A page that holds more
	// than was asked is not short: the next page starts after what it held.
	short := len(out) < upstreamPage &&
		(doc.Pagination.Limit < 1 || len(out) != doc.Pagination.Limit)
	// Never follow an upstream-supplied pagination URL. Its presence must agree
	// with the bounded total; subsequent pages use our canonical query.
	if more && (short || doc.Pagination.Next == nil) ||
		!more && doc.Pagination.Next != nil {
		return nil, false, invalidUpstream()
	}
	return out, more, nil
}

// release returns upstream release v of module name, read from base: a
// release page, or one release's own record (parseRelease). feed is the
// upstream's release list, v3/releases, under whose Forge root the archive
// must be. A release whose manifest, identity, checksum, size or archive
// address does not validate is invalid upstream metadata.
func (v upstreamVersion) release(name string, base, feed *url.URL) (release, error) {
	if !puppetfmt.ValidateManifest(v.Metadata.Manifest) || v.Metadata.Name != name ||
		v.Version != v.Metadata.Version ||
		!validSHA256(v.FileSHA256) ||
		v.FileSize <= 0 ||
		v.FileSize > puppetfmt.MaxArchiveBytes {
		return release{}, invalidUpstream()
	}
	u, err := url.Parse(v.FileURI)
	if err != nil || v.FileURI == "" {
		return release{}, invalidUpstream()
	}
	u = base.ResolveReference(u)
	if !archiveWithinFeed(feed, u) {
		return release{}, invalidUpstream()
	}
	// ValidateManifest has read a name and version from the document, so it
	// is a JSON object.
	metadata, _, _ := releaseMetadata(v.Metadata.document)
	return release{
		meta: puppetfmt.Metadata{
			Name:        name,
			Version:     v.Version,
			Path:        puppetfmt.Path(name, v.Version),
			SHA256:      v.FileSHA256,
			Size:        v.FileSize,
			Manifest:    v.Metadata.Manifest,
			Document:    v.Metadata.document,
			ProjectPage: v.Metadata.projectPage,
			IssuesURL:   v.Metadata.issuesURL,
		},
		metadata:  metadata,
		details:   v.relayed(),
		remoteURL: u.String(),
	}, nil
}

// parseRelease returns the release version of module name that the
// upstream's record of that one release, read from responseBase, gives. A
// record that is not one JSON object of at most maxMetadataBytes, that
// names another release, or that was read from anywhere but that release's
// record is invalid upstream metadata (502), and so is one a release page
// would refuse.
func parseRelease(data []byte, name, version, responseBase string) (release, error) {
	if len(data) == 0 || len(data) > maxMetadataBytes {
		return release{}, invalidUpstream()
	}
	suffix := "/v3/releases/" + name + "-" + version
	base, err := url.Parse(responseBase)
	if err != nil || !validArchiveURL(base) || !strings.HasSuffix(base.Path, suffix) {
		return release{}, invalidUpstream()
	}
	var v upstreamVersion
	if json.Unmarshal(data, &v) != nil || v.Version != version {
		return release{}, invalidUpstream()
	}
	feed := *base
	feed.Path = strings.TrimSuffix(base.Path, "/"+name+"-"+version)
	feed.RawPath = ""
	return v.release(name, base, &feed)
}

func validSHA256(value string) bool { return validHex(value, 64) }

// validHex reports whether value is a lowercase hex digest of n digits.
func validHex(value string, n int) bool {
	if len(value) != n || strings.ToLower(value) != value {
		return false
	}
	_, err := hex.DecodeString(value)
	return err == nil
}

func validArchiveURL(u *url.URL) bool {
	return u != nil && (u.Scheme == "https" || u.Scheme == "http") &&
		u.Host != "" && u.User == nil && u.Fragment == "" && u.Opaque == ""
}

func (h handler) remoteVersions(r *http.Request, name string, u use) ([]release, error) {
	all := []release{}
	seen := map[string]bool{}
	// The next page starts after the releases this one held, not after the
	// limit asked: an upstream may serve fewer (parseVersions), and found
	// leaves out those the caller may not read.
	for offset := 0; offset < maxVersions; {
		found, held, more, err := h.remotePage(r, name, offset, u)
		if err != nil {
			return nil, err
		}
		offset += held
		for _, rel := range found {
			if seen[rel.meta.Version] {
				return nil, invalidUpstream()
			}
			seen[rel.meta.Version] = true
			all = append(all, rel)
		}
		if !more {
			sortReleases(all)
			return all, nil
		}
	}
	return nil, invalidUpstream()
}

// upstreamURL returns the address of path under remote c's upstream with
// query beside any query its URL has. Forge query parameters identify a
// protocol resource; they are not caller query propagation. An explicit
// same-source URL keeps them in cache identity even when
// propagateQueryParams is false.
func upstreamURL(c repo.Config, path string, query url.Values) (string, error) {
	base, err := url.Parse(c.URL)
	if err != nil || !validArchiveURL(base) {
		return "", invalidUpstream()
	}
	base.Path = strings.TrimRight(base.Path, "/") + path
	base.RawPath = ""
	all := base.Query()
	for key, values := range query {
		all[key] = values
	}
	base.RawQuery = all.Encode()
	return base.String(), nil
}

// remotePage returns the releases of the upstream page at offset that the
// caller may read, how many releases the page held, and whether more follow.
func (h handler) remotePage(
	r *http.Request,
	name string,
	offset int,
	u use,
) ([]release, int, bool, error) {
	external, err := upstreamURL(pkgroute.RequestFrom(r).Repo, "/v3/releases", url.Values{
		"module":         {name},
		"limit":          {strconv.Itoa(upstreamPage)},
		"offset":         {strconv.Itoa(offset)},
		"sort_by":        {"version"},
		"exclude_fields": {strings.Join(releaseExclusions, " ")},
	})
	if err != nil {
		return nil, 0, false, err
	}
	resource := source.Resource{
		Kind:              source.ResourceKindMetadata,
		Path:              "v3/releases/" + name,
		ExternalURL:       external,
		NoCrossOriginAuth: true,
		CheckURL: func(_ context.Context, target *url.URL) error {
			return checkRegistryURL(pkgroute.RequestFrom(r).Repo, target)
		},
		Method:           http.MethodGet,
		PermissionFolder: true,
		PermissionPath:   name,
		// Read permission stays on the module folder. A listing checks the
		// download policy where 7.x caches a module's release pages,
		// .puppet/<m>/releases.json (RTFACT-13711), so a pattern on the
		// module's archives leaves the releases listed (R1, R3, R4).
		// Looking a release up to serve its archive is an internal lookup,
		// which only the blackout refuses (R4); the archive route has
		// checked the archive's own path first. The search, a native
		// search route, keeps its answers under a blackout, the blackout
		// 404 for the module folder (R2b), and no pattern refuses it,
		// since its releases stay listed whatever the patterns say of
		// their archives (R3).
		BlackoutOnly: u != listing,
		// v2 since a page keeps each release's whole metadata.json and
		// refuses one above puppetfmt.MaxManifestBytes.
		Variant:             "puppet-forge-v3-releases-v2",
		ExpectedContentType: mediaType,
		ValidateMetadata: func(_ context.Context, body io.ReadSeeker, _ int64, meta source.MetadataResponse) error {
			data, err := io.ReadAll(io.LimitReader(body, maxMetadataBytes+1))
			if err != nil {
				return invalidUpstream()
			}
			if _, _, ok := withinRegistry(pkgroute.RequestFrom(r).Repo, meta.BaseURL); !ok {
				return invalidUpstream()
			}
			_, _, err = parseVersions(data, name, meta.BaseURL, offset)
			return err
		},
	}
	if u != search {
		resource.PolicyPath = ".puppet/" + name + "/releases.json"
	}
	result, err := h.k.Read(r, resource)
	if err != nil {
		return nil, 0, false, fmt.Errorf("puppet: metadata: %w", pkgroute.SourceError(err, false))
	}
	data, err := h.metadataBytes(r, result)
	if err != nil {
		return nil, 0, false, err
	}
	if result.CheckRead == nil || result.CheckPermission == nil || result.Revalidate == nil {
		return nil, 0, false, fmt.Errorf(
			"puppet: missing authority: %w",
			pkgroute.SourceError(source.ErrInvalidResult, false),
		)
	}
	if _, _, ok := withinRegistry(pkgroute.RequestFrom(r).Repo, result.Metadata.BaseURL); !ok {
		return nil, 0, false, invalidUpstream()
	}
	releases, more, err := parseVersions(data, name, result.Metadata.BaseURL, offset)
	if err != nil {
		return nil, 0, false, err
	}
	if err = result.Revalidate(r.Context()); err != nil {
		return nil, 0, false, fmt.Errorf("puppet: authority: %w", pkgroute.SourceError(err, false))
	}
	out := releases[:0]
	for _, rel := range releases {
		// Read permission alone decides whether a release is listed: one
		// whose archive the remote's patterns reject stays listed, and the
		// archive route refuses the archive (R3, R4).
		if !h.k.Can(r, rel.meta.Path, api.ActionRead) {
			continue
		}
		if err = result.CheckPermission(r.Context(), rel.meta.Path); err != nil {
			if errors.Is(err, source.ErrDenied) {
				continue
			}
			return nil, 0, false, fmt.Errorf(
				"puppet: authority: %w",
				pkgroute.SourceError(err, false),
			)
		}
		rel.revalidate = result.Revalidate
		rel.checkPermission = result.CheckPermission
		rel.checkRead = result.CheckRead
		out = append(out, rel)
	}
	return out, len(releases), more, nil
}

// remoteModule returns the details of module name that the remote's
// upstream's module record, v3/modules/<m>, gives, and false when the
// upstream answers 404 for it (#1326). A remote relays the upstream's
// module-wide fields, its downloads, dates, endorsement and module group,
// rather than taking them from the releases [I]. RTFACT-19130 reports a
// 6.8.6 remote answering 404 for puppetlabs-sqlserver, a module whose
// upstream record the Forge served, while it served the module's releases;
// that is the fault the report names, so a remote answers the record its
// upstream serves. The record is read with the release pages' authority:
// Read on the module folder, the download policy where 7.x caches the
// module's release pages for a listing, and the blackout alone for the
// search (remotePage). Where 7.x caches the record is [I].
func (h handler) remoteModule(
	r *http.Request,
	name string,
	u use,
) (upstreamModuleRead, bool, error) {
	c := pkgroute.RequestFrom(r).Repo
	// The remote has read its release pages first, from the same address,
	// so the address is valid here.
	external, _ := upstreamURL(c, "/v3/modules/"+name, url.Values{
		"exclude_fields": {strings.Join(moduleExclusions, " ")},
	})
	resource := source.Resource{
		Kind:              source.ResourceKindMetadata,
		Path:              "v3/modules/" + name,
		ExternalURL:       external,
		NoCrossOriginAuth: true,
		CheckURL: func(_ context.Context, target *url.URL) error {
			return checkRegistryURL(c, target)
		},
		Method:           http.MethodGet,
		PermissionFolder: true,
		PermissionPath:   name,
		BlackoutOnly:     u != listing,
		// v2 since the record keeps its release list (moduleExclusions).
		Variant:             "puppet-forge-v3-module-v2",
		ExpectedContentType: mediaType,
		ValidateMetadata: func(_ context.Context, body io.ReadSeeker, _ int64, meta source.MetadataResponse) error {
			data, err := io.ReadAll(io.LimitReader(body, maxMetadataBytes+1))
			if err != nil {
				return invalidUpstream()
			}
			if _, _, ok := withinRegistry(c, meta.BaseURL); !ok {
				return invalidUpstream()
			}
			_, err = parseModule(data, name, meta.BaseURL)
			return err
		},
	}
	if u != search {
		resource.PolicyPath = ".puppet/" + name + "/releases.json"
	}
	result, err := h.k.Read(r, resource)
	if missingRecord(err) {
		return upstreamModuleRead{}, false, nil
	}
	if err != nil {
		return upstreamModuleRead{}, false, fmt.Errorf(
			"puppet: module record: %w",
			pkgroute.SourceError(err, false),
		)
	}
	data, err := h.metadataBytes(r, result)
	if err != nil {
		return upstreamModuleRead{}, false, err
	}
	if result.Revalidate == nil {
		return upstreamModuleRead{}, false, fmt.Errorf(
			"puppet: missing authority: %w",
			pkgroute.SourceError(source.ErrInvalidResult, false),
		)
	}
	if _, _, ok := withinRegistry(c, result.Metadata.BaseURL); !ok {
		return upstreamModuleRead{}, false, invalidUpstream()
	}
	read, err := parseModule(data, name, result.Metadata.BaseURL)
	if err != nil {
		return upstreamModuleRead{}, false, err
	}
	if err = result.Revalidate(r.Context()); err != nil {
		return upstreamModuleRead{}, false, fmt.Errorf(
			"puppet: authority: %w",
			pkgroute.SourceError(err, false),
		)
	}
	read.checkPermission = result.CheckPermission
	return read, true, nil
}

// remoteRelease returns release version of module name as the remote's
// upstream's record of that release, v3/releases/<slug>, gives it, and
// false when the upstream answers 404 for it or the caller may not read it
// (#1344). The release route asks for it only for a release the
// upstream's release pages leave out, so a release they list answers as it
// did. RTFACT-19130 shows a 6.8.6 remote answering
// v3/releases/puppetlabs-sqlserver-2.5.0, a Puppet Enterprise module's
// release, while it answered the module 404; the Forge's module list
// leaves such modules out unless module_groups names them (its v3 API
// description), and a remote does not ask for them (moduleGroups), so the
// release came from a document no list filters, which is taken to be the
// release's own record [I]. It is read with the release pages' authority,
// Read on the module folder and the download policy at
// .puppet/<m>/releases.json, without the fields no record answers
// (releaseExclusions), which the Forge's description lets this operation
// leave out too, and validated as a release page's record is. Where 7.x
// caches it is [I]. To serve the record's archive (u download) it is read
// as the pages are to find a listed release's archive, with the blackout
// alone (R4): the archive route has checked the archive's own path first.
// The release returned carries the read's authority, which the archive
// route checks again before it serves the archive, as for a listed
// release.
func (h handler) remoteRelease(
	r *http.Request,
	name, version string,
	u use,
) (release, bool, error) {
	c := pkgroute.RequestFrom(r).Repo
	slug := name + "-" + version
	// The remote has read its release pages first, from the same address,
	// so the address is valid here.
	external, _ := upstreamURL(c, "/v3/releases/"+slug, url.Values{
		"exclude_fields": {strings.Join(releaseExclusions, " ")},
	})
	resource := source.Resource{
		Kind:              source.ResourceKindMetadata,
		Path:              "v3/releases/" + slug,
		ExternalURL:       external,
		NoCrossOriginAuth: true,
		CheckURL: func(_ context.Context, target *url.URL) error {
			return checkRegistryURL(c, target)
		},
		Method:              http.MethodGet,
		PermissionFolder:    true,
		PermissionPath:      name,
		PolicyPath:          ".puppet/" + name + "/releases.json",
		BlackoutOnly:        u != listing,
		Variant:             "puppet-forge-v3-release-v1",
		ExpectedContentType: mediaType,
		ValidateMetadata: func(_ context.Context, body io.ReadSeeker, _ int64, meta source.MetadataResponse) error {
			data, err := io.ReadAll(io.LimitReader(body, maxMetadataBytes+1))
			if err != nil {
				return invalidUpstream()
			}
			if _, _, ok := withinRegistry(c, meta.BaseURL); !ok {
				return invalidUpstream()
			}
			_, err = parseRelease(data, name, version, meta.BaseURL)
			return err
		},
	}
	result, err := h.k.Read(r, resource)
	if missingRecord(err) {
		return release{}, false, nil
	}
	if err != nil {
		return release{}, false, fmt.Errorf(
			"puppet: release record: %w",
			pkgroute.SourceError(err, false),
		)
	}
	data, err := h.metadataBytes(r, result)
	if err != nil {
		return release{}, false, err
	}
	if result.CheckRead == nil || result.CheckPermission == nil || result.Revalidate == nil {
		return release{}, false, fmt.Errorf(
			"puppet: missing authority: %w",
			pkgroute.SourceError(source.ErrInvalidResult, false),
		)
	}
	if _, _, ok := withinRegistry(c, result.Metadata.BaseURL); !ok {
		return release{}, false, invalidUpstream()
	}
	rel, err := parseRelease(data, name, version, result.Metadata.BaseURL)
	if err != nil {
		return release{}, false, err
	}
	if err = result.Revalidate(r.Context()); err != nil {
		return release{}, false, fmt.Errorf(
			"puppet: authority: %w",
			pkgroute.SourceError(err, false),
		)
	}
	// The release route has checked Read on the release's archive path; the
	// source rechecks it as a release page's releases are rechecked, and a
	// release it denies answers as one the upstream does not hold.
	if err = result.CheckPermission(r.Context(), rel.meta.Path); err != nil {
		if errors.Is(err, source.ErrDenied) {
			return release{}, false, nil
		}
		return release{}, false, fmt.Errorf(
			"puppet: authority: %w",
			pkgroute.SourceError(err, false),
		)
	}
	rel.revalidate = result.Revalidate
	rel.checkPermission = result.CheckPermission
	rel.checkRead = result.CheckRead
	return rel, true, nil
}

// missingRecord reports whether err is the upstream's 404 for a record,
// which the source boundary reports as kernel.ErrNotFound and a remote
// source answers as a 404, rather than the download policy's refusal, whose
// 404 is the remote's own answer and says nothing of the upstream.
func missingRecord(err error) bool {
	if pkgroute.IsRefusal(err) {
		return false
	}
	var failure *apierror.Error
	return errors.Is(err, kernel.ErrNotFound) ||
		errors.As(err, &failure) && failure.Status == http.StatusNotFound
}

// upstreamModule is the part of an upstream's module record a remote
// relays. Each field is read raw, as a release's are, so a value of another
// type answers as a hosted module's would.
type upstreamModule struct {
	Slug        json.RawMessage `json:"slug"`
	Downloads   json.RawMessage `json:"downloads"`
	CreatedAt   json.RawMessage `json:"created_at"`
	UpdatedAt   json.RawMessage `json:"updated_at"`
	Supported   json.RawMessage `json:"supported"`
	Endorsement json.RawMessage `json:"endorsement"`
	ModuleGroup json.RawMessage `json:"module_group"`
	Owner       json.RawMessage `json:"owner"`
	HomepageURL json.RawMessage `json:"homepage_url"`
	IssuesURL   json.RawMessage `json:"issues_url"`
	// Releases is read only for a module the release pages leave out
	// (recordReleases), so a value the pages make unnecessary never fails
	// a module they list.
	Releases json.RawMessage `json:"releases"`
}

// upstreamModuleRead is what a remote reads from its upstream's module
// record: the details it relays, the record's own release list, and the
// read's check of Read permission on a release the list names.
type upstreamModuleRead struct {
	details         moduleDetails
	releases        json.RawMessage
	checkPermission func(context.Context, string) error
}

// parseModule returns the details the upstream's record of module name
// gives, read from responseBase. A record that is not one JSON object of at
// most maxMetadataBytes, that names another module, or that was read from
// anywhere but that module's record is invalid upstream metadata (502).
func parseModule(data []byte, name, responseBase string) (upstreamModuleRead, error) {
	if len(data) == 0 || len(data) > maxMetadataBytes {
		return upstreamModuleRead{}, invalidUpstream()
	}
	base, err := url.Parse(responseBase)
	if err != nil || !validArchiveURL(base) || !strings.HasSuffix(base.Path, "/v3/modules/"+name) {
		return upstreamModuleRead{}, invalidUpstream()
	}
	var doc upstreamModule
	if json.Unmarshal(data, &doc) != nil {
		return upstreamModuleRead{}, invalidUpstream()
	}
	if slug := rawText(doc.Slug); slug == nil || *slug != name {
		return upstreamModuleRead{}, invalidUpstream()
	}
	// module_group is "base" for every module but a Puppet Enterprise one
	// (the Forge's v3 API description), and a hosted module's in JFrog's
	// sample answer, so a value of another type answers it [I].
	d := moduleDetails{
		created: rawText(doc.CreatedAt), updated: rawText(doc.UpdatedAt),
		endorsement: rawText(doc.Endorsement), group: "base",
		homepage: rawText(doc.HomepageURL), issues: rawText(doc.IssuesURL),
	}
	if group := rawText(doc.ModuleGroup); group != nil {
		d.group = *group
	}
	_ = json.Unmarshal(doc.Supported, &d.supported)
	var downloads int64
	if json.Unmarshal(doc.Downloads, &downloads) == nil && downloads > 0 {
		d.downloads = downloads
	}
	var owner struct {
		GravatarID json.RawMessage `json:"gravatar_id"`
	}
	if json.Unmarshal(doc.Owner, &owner) == nil {
		d.gravatarID = rawText(owner.GravatarID)
	}
	return upstreamModuleRead{details: d, releases: doc.Releases}, nil
}

// recordReleases returns the versions of the releases an upstream's module
// record lists, newest first, leaving out those the record marks deleted:
// the Forge's module record lists a deleted release with its deleted_at,
// which its release list leaves out (puppetlabs-sqlserver 2.0.1 on
// 2026-11-12), so a module answered from the record lists what its release
// pages would [I]. A list that is not one of releases with valid, distinct
// versions, or that names more than a module may list, is invalid upstream
// metadata; no list answers none.
func recordReleases(raw json.RawMessage) ([]string, error) {
	var entries []struct {
		Version   string          `json:"version"`
		DeletedAt json.RawMessage `json:"deleted_at"`
	}
	if len(raw) > 0 && json.Unmarshal(raw, &entries) != nil || len(entries) > maxVersions {
		return nil, invalidUpstream()
	}
	out := make([]string, 0, len(entries))
	seen := map[string]bool{}
	for _, e := range entries {
		if !puppetfmt.ValidVersion(e.Version) || seen[e.Version] {
			return nil, invalidUpstream()
		}
		seen[e.Version] = true
		if rawText(e.DeletedAt) == nil {
			out = append(out, e.Version)
		}
	}
	slices.SortFunc(out, newestFirst)
	return out, nil
}

func (h handler) metadataBytes(r *http.Request, result source.Result) ([]byte, error) {
	var reader io.ReadCloser
	switch {
	case result.Stream != nil:
		reader = result.Stream.Body
	case result.Item != nil && h.blobs != nil:
		rc, _, err := h.blobs.Open(r.Context(), result.Item.Actual.SHA256)
		if err != nil {
			return nil, fmt.Errorf("puppet: open cached metadata: %w", err)
		}
		reader = rc
	default:
		return nil, invalidUpstream()
	}
	defer reader.Close()
	data, err := io.ReadAll(io.LimitReader(reader, maxMetadataBytes+1))
	if err != nil || len(data) > maxMetadataBytes {
		return nil, invalidUpstream()
	}
	return data, nil
}

func (h handler) remoteArchive(w http.ResponseWriter, r *http.Request, rel release) {
	member := r
	if rel.member != nil {
		member = rel.member
	}
	if rel.checkRead == nil || rel.revalidate == nil {
		h.k.Error(w, r, pkgroute.SourceError(source.ErrInvalidResult, false))
		return
	}
	if err := rel.checkRead(member.Context(), rel.meta.Path); err != nil {
		h.k.Error(w, r, pkgroute.SourceError(err, false))
		return
	}
	resource := source.Resource{
		Kind: source.ResourceKindArtifact, Path: rel.meta.Path, Method: http.MethodGet,
		Variant:   "puppet-forge-v3-archive-v1",
		Checksums: kernel.Checksums{SHA256: rel.meta.SHA256},
		Validate: func(_ context.Context, body io.ReadSeeker, size int64) error {
			if size != rel.meta.Size || size <= 0 || size > puppetfmt.MaxArchiveBytes {
				return source.ErrInvalidResult
			}
			data, err := io.ReadAll(io.LimitReader(body, puppetfmt.MaxArchiveBytes+1))
			if err != nil || len(data) > puppetfmt.MaxArchiveBytes {
				return source.ErrInvalidResult
			}
			{
				actual := sha256.Sum256(data)
				if hex.EncodeToString(actual[:]) != rel.meta.SHA256 {
					return source.ErrInvalidResult
				}
			}
			meta, err := puppetfmt.ReadArchive(bytes.NewReader(data), int64(len(data)))
			if err != nil || meta.Name != rel.meta.Name || meta.Version != rel.meta.Version ||
				!reflect.DeepEqual(meta.Manifest, rel.meta.Manifest) {
				return source.ErrInvalidResult
			}
			return nil
		},
	}
	resource.ExternalURL = rel.remoteURL
	resource.NoCrossOriginAuth = true
	resource.CheckURL = func(_ context.Context, target *url.URL) error {
		return checkRegistryURL(pkgroute.RequestFrom(member).Repo, target)
	}
	result, err := h.k.Read(member, resource)
	if err != nil {
		h.k.Error(w, r, pkgroute.SourceError(err, false))
		return
	}
	if result.Revalidate == nil {
		if result.Stream != nil {
			_ = result.Stream.Body.Close()
		}
		h.k.Error(w, r, pkgroute.SourceError(source.ErrInvalidResult, false))
		return
	}
	prior := result.Revalidate
	result.Revalidate = func(ctx context.Context) error {
		if err := rel.revalidate(ctx); err != nil {
			return err
		}
		if err := rel.checkRead(ctx, rel.meta.Path); err != nil {
			return err
		}
		return prior(ctx)
	}
	h.k.ServeResult(w, member, result)
}

// withinRegistry constrains metadata and archives to the configured upstream root.
func withinRegistry(c repo.Config, target string) (string, string, bool) {
	base, baseErr := url.Parse(strings.TrimRight(c.URL, "/"))
	u, targetErr := url.Parse(target)
	if baseErr != nil || targetErr != nil || base.Scheme != u.Scheme ||
		base.Host != u.Host || u.User != nil || u.Fragment != "" ||
		!strings.HasPrefix(u.Path, strings.TrimRight(base.Path, "/")+"/") {
		return "", "", false
	}
	rel := strings.TrimPrefix(u.Path, strings.TrimRight(base.Path, "/")+"/")
	if rel == "" || strings.HasPrefix(rel, "/") || strings.Contains(rel, "..") {
		return "", "", false
	}
	return rel, u.RawQuery, true
}

func archiveWithinFeed(base, target *url.URL) bool {
	if !validArchiveURL(target) || target.Scheme != base.Scheme || target.Host != base.Host {
		return false
	}
	root := strings.TrimSuffix(base.Path, "/v3/releases") + "/"
	if !strings.HasPrefix(target.Path, root) {
		return false
	}
	for _, segment := range strings.Split(target.Path, "/") {
		if segment == ".." || segment == "." {
			return false
		}
	}
	return true
}

func checkRegistryURL(c repo.Config, target *url.URL) error {
	if target == nil {
		return source.ErrInvalidRequest
	}
	if _, _, ok := withinRegistry(c, target.String()); !ok {
		return source.ErrDenied
	}
	return nil
}
