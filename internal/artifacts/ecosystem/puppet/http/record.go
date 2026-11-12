package puppethttp

import (
	"bytes"
	"encoding/json"
	"errors"
	"slices"
	"strconv"
	"strings"
	"time"

	"github.com/weaveplatform/artifactweave/internal/artifacts/core/model"
)

// The release and module records every Puppet answer gives. Their fields
// are those JFrog's REST API reference gives Get Puppet Releases, Get
// Puppet Release Info, Get Puppet Modules and Get Puppet Module Info
// (jfrog.com/help, archived 2023 to 2025), with the ones Artifactory's own
// answers add: a release's slug, file_uri and file_sha256 (RTFACT-26143),
// its module's slug, owner and deprecated_at (RTFACT-19130 quotes a 6.8.6
// remote's release record), and a module's slug, endorsement, module_group
// and owner slug and gravatar_id (JFrog's sample answer for Get Puppet
// Modules). The fields are in the Forge's order, which RTFACT-19130's answer
// keeps but for the module's owner, which it writes before deprecated_at;
// where no answer shows the order, the reference's is taken [I].

// releaseRecord is a release as the release lists, v3/releases/<slug> and
// a module's current_release answer it.
type releaseRecord struct {
	URI        string          `json:"uri"`
	Slug       string          `json:"slug"`
	Module     moduleRef       `json:"module"`
	Version    string          `json:"version"`
	Metadata   json.RawMessage `json:"metadata"`
	Tags       []string        `json:"tags"`
	Supported  bool            `json:"supported"`
	FileURI    string          `json:"file_uri"`
	FileSize   int64           `json:"file_size"`
	FileMD5    *string         `json:"file_md5"`
	FileSHA256 string          `json:"file_sha256"`
	Downloads  int64           `json:"downloads"`
	Readme     *string         `json:"readme"`
	Changelog  *string         `json:"changelog"`
	License    *string         `json:"license"`
	CreatedAt  *string         `json:"created_at"`
	UpdatedAt  *string         `json:"updated_at"`
	DeletedAt  *string         `json:"deleted_at"`
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
// gives. Each is null, zero, false or empty unless the release's source
// sets it.
type details struct {
	md5                        *string
	downloads                  int64
	supported                  bool
	gravatarID, deprecatedAt   *string
	readme, changelog, license *string
	created, updated, deleted  *string
	tags                       []string
}

// moduleDetails are a module record's fields beyond its name, owner name
// and releases: a hosted or virtual module's taken from its releases
// (derivedModule), a remote's from its upstream's module record (#1326).
type moduleDetails struct {
	downloads        int64
	created, updated *string
	supported        bool
	endorsement      *string
	group            string
	gravatarID       *string
	homepage, issues *string
}

// metadataOrder is the order a release record writes its metadata in, each
// field null when metadata.json has none. RTFACT-19130 quotes a 6.8.6
// remote answering puppetlabs-sqlserver 2.5.0, whose metadata.json writes
// author, summary, license and source after version, has no description and
// puts tags after requirements, in this order instead, with "description":
// null: Artifactory writes a release's metadata through its own model
// rather than as the file has it, which 7.x is taken to keep [I]. The quote
// stops after dependencies, so the file's other fields follow in the
// file's order [I].
var metadataOrder = []string{
	"name", "version", "source", "author", "license", "summary", "description",
	"project_page", "issues_url", "tags", "dependencies",
}

// errMetadata is what releaseMetadata returns for a document that is not
// one JSON object.
var errMetadata = errors.New("puppet: release metadata is not a JSON object")

// releaseMetadata returns document, a release's metadata.json, as its
// record answers it (metadataOrder), and the tags it gives: its tags when
// they are a list of strings, and none otherwise [I]. The Forge derives a
// release's tags from metadata.json (its v3 API description), so a hosted
// release answers those. A key that repeats keeps its first place and its
// last value, as encoding/json reads it, though the manifest's own decoding
// refuses repeated keys first.
func releaseMetadata(document []byte) (json.RawMessage, []string, error) {
	dec := json.NewDecoder(bytes.NewReader(document))
	if token, err := dec.Token(); err != nil || token != json.Delim('{') {
		return nil, nil, errMetadata
	}
	// document is valid JSON, since encoding/json decoded it into the index
	// entry or the upstream's page, so an object's keys and values read
	// back.
	var keys []string
	values := map[string]json.RawMessage{}
	for dec.More() {
		token, _ := dec.Token()
		key, _ := token.(string)
		var value json.RawMessage
		_ = dec.Decode(&value)
		if _, seen := values[key]; !seen {
			keys = append(keys, key)
		}
		values[key] = value
	}
	var out bytes.Buffer
	out.WriteByte('{')
	field := func(key string, value json.RawMessage) {
		if out.Len() > 1 {
			out.WriteByte(',')
		}
		out.Write(jsonString(key))
		out.WriteByte(':')
		out.Write(plainJSON(value))
	}
	for _, key := range metadataOrder {
		value, ok := values[key]
		if !ok {
			value = json.RawMessage("null")
		}
		field(key, value)
	}
	for _, key := range keys {
		if !slices.Contains(metadataOrder, key) {
			field(key, values[key])
		}
	}
	out.WriteByte('}')
	return out.Bytes(), stringList(values["tags"]), nil
}

// stringList returns raw as a list of strings, or an empty list when it is
// anything else, so that a record's tags are never null.
func stringList(raw json.RawMessage) []string {
	var out []string
	if json.Unmarshal(raw, &out) != nil || out == nil {
		return []string{}
	}
	return out
}

// plainJSON returns value, which encoding/json decoded and so is valid
// JSON, compacted and with every string written as writeJSON writes it.
// The package index keeps a hosted release's metadata.json through
// encoding/json, which writes "&", "<" and ">" as \u0026, \u003c and
// \u003e, and an upstream may escape any character; the record answers the
// text the file gave, as it did before it answered the whole file. Keys
// keep their order and numbers their text.
func plainJSON(value json.RawMessage) []byte {
	dec := json.NewDecoder(bytes.NewReader(value))
	dec.UseNumber()
	// open holds, for each object or array the value has open, whether it
	// is an object and how many keys and values it has had, which decide
	// whether a comma or a colon comes next.
	type level struct {
		object bool
		n      int
	}
	var open []level
	var out bytes.Buffer
	for {
		token, err := dec.Token()
		if err != nil {
			// io.EOF: value was valid, so its tokens have all been read.
			return out.Bytes()
		}
		if d, ok := token.(json.Delim); ok && (d == '}' || d == ']') {
			open = open[:len(open)-1]
			out.WriteString(d.String())
			continue
		}
		if len(open) > 0 {
			top := &open[len(open)-1]
			switch {
			case top.object && top.n%2 == 1:
				out.WriteByte(':')
			case top.n > 0:
				out.WriteByte(',')
			}
			top.n++
		}
		switch t := token.(type) {
		case json.Delim:
			out.WriteString(t.String())
			open = append(open, level{object: t == '{'})
		case string:
			out.Write(jsonString(t))
		case json.Number:
			out.WriteString(t.String())
		case bool:
			out.WriteString(strconv.FormatBool(t))
		default:
			out.WriteString("null")
		}
	}
}

// jsonString returns s as a JSON string, writing "&", "<" and ">" as they
// are, as writeJSON does.
func jsonString(s string) []byte {
	var b bytes.Buffer
	enc := json.NewEncoder(&b)
	enc.SetEscapeHTML(false)
	_ = enc.Encode(s)
	return bytes.TrimSuffix(b.Bytes(), []byte("\n"))
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

// ownerOf returns the owner of module name, with gravatarID: null for a
// hosted module, as in JFrog's sample answer.
func ownerOf(name string, gravatarID *string) owner {
	user, _, _ := strings.Cut(name, "-")
	return owner{URI: "/v3/users/" + user, Slug: user, Username: user, GravatarID: gravatarID}
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
			Owner: ownerOf(meta.Name, d.gravatarID), DeprecatedAt: d.deprecatedAt,
		},
		Version:    meta.Version,
		Metadata:   rel.metadata,
		Tags:       d.tags,
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

// derivedModule returns the details of a module whose releases are
// releases, newest first, taken from those releases [I]: downloads is
// theirs summed, created_at the earliest of theirs and updated_at the
// latest, and supported, the owner's gravatar_id and the links are the
// current release's, homepage_url and issues_url being its metadata.json's
// project_page and issues_url, as the Forge answers them. endorsement and
// module_group are a hosted module's in JFrog's sample answer, null and
// "base". A virtual repository's module takes them from its merged
// releases too, whatever its members are [I].
func derivedModule(releases []release) moduleDetails {
	current := releases[0]
	out := moduleDetails{
		supported: current.details.supported, group: "base",
		gravatarID: current.details.gravatarID,
		homepage:   text(current.meta.ProjectPage), issues: text(current.meta.IssuesURL),
	}
	var first, last time.Time
	for _, rel := range releases {
		out.downloads += rel.details.downloads
		if t, ok := parseForgeTime(rel.details.created); ok &&
			(out.created == nil || t.Before(first)) {
			out.created, first = rel.details.created, t
		}
		if t, ok := parseForgeTime(rel.details.updated); ok &&
			(out.updated == nil || t.After(last)) {
			out.updated, last = rel.details.updated, t
		}
	}
	return out
}

// newModuleRecord returns the module record of releases, newest first,
// with d's fields: its current_release and releases are those the caller
// may read, whatever the module's source says of the rest.
func newModuleRecord(releases []release, d moduleDetails) moduleRecord {
	current := releases[0]
	name := current.meta.Name
	_, module, _ := strings.Cut(name, "-")
	out := moduleRecord{
		URI: "/v3/modules/" + name, Slug: name, Name: module,
		Downloads: d.downloads, CreatedAt: d.created, UpdatedAt: d.updated,
		Supported: d.supported, Endorsement: d.endorsement, ModuleGroup: d.group,
		Owner:          ownerOf(name, d.gravatarID),
		CurrentRelease: record(current),
		Releases:       make([]releaseLink, 0, len(releases)),
		HomepageURL:    d.homepage,
		IssuesURL:      d.issues,
	}
	for _, rel := range releases {
		out.Releases = append(out.Releases, releaseLink{
			URI: "/v3/releases/" + name + "-" + rel.meta.Version, Version: rel.meta.Version,
		})
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
