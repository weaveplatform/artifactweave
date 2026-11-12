package puppethttp

import (
	"strings"
	"time"

	"github.com/weaveplatform/artifactweave/internal/artifacts/core/model"
	puppetfmt "github.com/weaveplatform/artifactweave/internal/artifacts/ecosystem/puppet"
)

// The release and module records every Puppet answer gives. Their fields
// are those JFrog's REST API reference gives Get Puppet Releases, Get
// Puppet Release Info, Get Puppet Modules and Get Puppet Module Info
// (jfrog.com/help, archived 2023 to 2025), with the ones 7.x's own answers
// add: a release's slug, file_uri and file_sha256 (RTFACT-26143), its
// module's slug, owner and deprecated_at (RTFACT-19130 quotes a 6.8
// remote's release record), and a module's slug, endorsement, module_group
// and owner slug and gravatar_id (JFrog's sample answer for Get Puppet
// Modules). The fields are in the Forge's order, which 7.x keeps but for
// the module's owner, which it writes before deprecated_at (RTFACT-19130);
// where no answer shows the order, the reference's is taken [I].

// releaseRecord is a release as the release lists, v3/releases/<slug> and
// a module's current_release answer it.
type releaseRecord struct {
	URI        string             `json:"uri"`
	Slug       string             `json:"slug"`
	Module     moduleRef          `json:"module"`
	Version    string             `json:"version"`
	Metadata   puppetfmt.Manifest `json:"metadata"`
	Tags       []string           `json:"tags"`
	Supported  bool               `json:"supported"`
	FileURI    string             `json:"file_uri"`
	FileSize   int64              `json:"file_size"`
	FileMD5    *string            `json:"file_md5"`
	FileSHA256 string             `json:"file_sha256"`
	Downloads  int64              `json:"downloads"`
	Readme     *string            `json:"readme"`
	Changelog  *string            `json:"changelog"`
	License    *string            `json:"license"`
	CreatedAt  *string            `json:"created_at"`
	UpdatedAt  *string            `json:"updated_at"`
	DeletedAt  *string            `json:"deleted_at"`
}

// moduleRef is the module a release record names.
type moduleRef struct {
	URI          string  `json:"uri"`
	Slug         string  `json:"slug"`
	Name         string  `json:"name"`
	Owner        owner   `json:"owner"`
	DeprecatedAt *string `json:"deprecated_at"`
}

// owner is a module's owner: the part of its name before the dash.
type owner struct {
	URI        string  `json:"uri"`
	Slug       string  `json:"slug"`
	Username   string  `json:"username"`
	GravatarID *string `json:"gravatar_id"`
}

// moduleRecord is a module as v3/modules/<m>, the module search and the
// module list answer it.
type moduleRecord struct {
	URI            string        `json:"uri"`
	Slug           string        `json:"slug"`
	Name           string        `json:"name"`
	Downloads      int64         `json:"downloads"`
	CreatedAt      *string       `json:"created_at"`
	UpdatedAt      *string       `json:"updated_at"`
	Supported      bool          `json:"supported"`
	Endorsement    *string       `json:"endorsement"`
	ModuleGroup    string        `json:"module_group"`
	Owner          owner         `json:"owner"`
	CurrentRelease releaseRecord `json:"current_release"`
	Releases       []releaseLink `json:"releases"`
	HomepageURL    *string       `json:"homepage_url"`
	IssuesURL      *string       `json:"issues_url"`
}

// releaseLink is one release a module record lists.
type releaseLink struct {
	URI     string `json:"uri"`
	Version string `json:"version"`
}

// details are a release record's fields beyond what its archive's metadata
// gives. Each is null, zero or false unless the release's source sets it.
type details struct {
	md5                        *string
	downloads                  int64
	supported                  bool
	gravatarID, deprecatedAt   *string
	readme, changelog, license *string
	created, updated, deleted  *string
}

// forgeTimeLayout is how the Forge writes its dates, "2012-09-19 16:49:18
// -0700", and how JFrog's sample answer writes a hosted module's
// updated_at, "2017-07-16 12:07:00 +0300". The sample's created_at,
// "2017-07-16 12:07:715 +0300", writes milliseconds where the seconds go,
// which reads as a fault in 7.x rather than a format to copy.
const forgeTimeLayout = "2006-01-02 15:04:05 -0700"

// hostedDetails are a hosted release's details, from its archive item and
// that item's statistics: its MD5, which 7.x's stored release documents
// carry (RTFACT-26143), the downloads counted, and the item's creation and
// last update, written in UTC [I for the zone and for which item dates]. A
// hosted release has no readme, changelog or license text [I]: 7.x's
// Puppet info reads only an archive's metadata.json.
func hostedDetails(it model.Item, stats model.ItemStats) details {
	d := details{
		downloads: stats.DownloadCount,
		created:   forgeTime(it.Created),
		updated:   forgeTime(it.Updated),
	}
	if it.Actual.MD5 != "" {
		md5 := it.Actual.MD5
		d.md5 = &md5
	}
	return d
}

// forgeTime writes t in forgeTimeLayout, in UTC.
func forgeTime(t time.Time) *string {
	s := t.UTC().Format(forgeTimeLayout)
	return &s
}

// ownerOf returns the owner of module name, with the gravatar_id d gives:
// null for a hosted module, as in JFrog's sample answer.
func ownerOf(name string, d details) owner {
	user, _, _ := strings.Cut(name, "-")
	return owner{URI: "/v3/users/" + user, Slug: user, Username: user, GravatarID: d.gravatarID}
}

// record returns rel's release record.
func record(rel release) releaseRecord {
	meta, d := rel.meta, rel.details
	slug := meta.Name + "-" + meta.Version
	_, module, _ := strings.Cut(meta.Name, "-")
	return releaseRecord{
		URI:  "/v3/releases/" + slug,
		Slug: slug,
		Module: moduleRef{
			URI: "/v3/modules/" + meta.Name, Slug: meta.Name, Name: module,
			Owner: ownerOf(meta.Name, d), DeprecatedAt: d.deprecatedAt,
		},
		Version:    meta.Version,
		Metadata:   meta.Manifest,
		Tags:       []string{},
		Supported:  d.supported,
		FileURI:    "/v3/files/" + slug + ".tar.gz",
		FileSize:   meta.Size,
		FileMD5:    d.md5,
		FileSHA256: meta.SHA256,
		Downloads:  d.downloads,
		Readme:     d.readme,
		Changelog:  d.changelog,
		License:    d.license,
		CreatedAt:  d.created,
		UpdatedAt:  d.updated,
		DeletedAt:  d.deleted,
	}
}

// newModuleRecord returns the module record of releases, newest first. The
// module-wide fields are taken from its releases [I]: downloads is theirs
// summed, created_at the earliest of theirs and updated_at the latest, and
// supported, the owner's gravatar_id and the links are the current
// release's, homepage_url and issues_url being its metadata.json's
// project_page and issues_url, as the Forge answers them. endorsement and
// module_group are a hosted module's in JFrog's sample answer, null and
// "base"; a remote has no upstream module record to take them from.
func newModuleRecord(releases []release) moduleRecord {
	current := releases[0]
	name := current.meta.Name
	_, module, _ := strings.Cut(name, "-")
	out := moduleRecord{
		URI: "/v3/modules/" + name, Slug: name, Name: module,
		Supported: current.details.supported, ModuleGroup: "base",
		Owner:          ownerOf(name, current.details),
		CurrentRelease: record(current),
		Releases:       make([]releaseLink, 0, len(releases)),
		HomepageURL:    text(current.meta.ProjectPage),
		IssuesURL:      text(current.meta.IssuesURL),
	}
	var first, last time.Time
	for _, rel := range releases {
		out.Downloads += rel.details.downloads
		out.Releases = append(out.Releases, releaseLink{
			URI: "/v3/releases/" + name + "-" + rel.meta.Version, Version: rel.meta.Version,
		})
		if t, ok := parseForgeTime(rel.details.created); ok &&
			(out.CreatedAt == nil || t.Before(first)) {
			out.CreatedAt, first = rel.details.created, t
		}
		if t, ok := parseForgeTime(rel.details.updated); ok &&
			(out.UpdatedAt == nil || t.After(last)) {
			out.UpdatedAt, last = rel.details.updated, t
		}
	}
	return out
}

// parseForgeTime reads a date written in forgeTimeLayout. A remote relays
// its upstream's dates as given, so one in another form is passed over
// when a module's dates are chosen.
func parseForgeTime(s *string) (time.Time, bool) {
	if s == nil {
		return time.Time{}, false
	}
	t, err := time.Parse(forgeTimeLayout, *s)
	return t, err == nil
}

// text returns s, or null when it is empty.
func text(s string) *string {
	if s == "" {
		return nil
	}
	return &s
}
