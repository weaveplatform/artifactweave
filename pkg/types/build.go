package types

import (
	"strconv"
	"strings"
	"time"
	"unicode"
)

// Build is one run of a build (decision 0014). One name and number may be
// published several times, and each publish is a new run, so a run is
// identified by ProjectKey, Name, Number and Started together (research 22
// §3.3). The document is kept as published, for the read API; the modules,
// properties and files are kept as rows, for the AQL build domains and the
// checksum searches.
type Build struct {
	// ID is assigned by the store when the run is put.
	ID int64
	// ProjectKey is "" for the default project.
	ProjectKey string
	Name       string
	Number     string
	// Started is the document's start time, at millisecond precision in
	// the document; it stays fixed through promotion and replication.
	Started    time.Time
	URL        string
	Created    time.Time
	CreatedBy  string
	Modified   time.Time
	ModifiedBy string
	// Properties are the document's build-level properties, the AQL
	// build.property domain.
	Properties Properties
	Modules    []BuildModule
	// Document is the build-info JSON as published.
	Document []byte
}

// Run returns b's identity and timestamps, without its content.
func (b Build) Run() BuildRun {
	return BuildRun{
		ID: b.ID, ProjectKey: b.ProjectKey, Name: b.Name, Number: b.Number,
		Started: b.Started, Created: b.Created,
	}
}

// DocumentItem returns where b's document is also stored as an item: the
// build-info repository and the full path within it. The CLI finds the item
// with AQL $match on these encoded segments, for LATEST and build-append
// (research 22 §3.3), so the encoding must equal the CLI's byte for byte.
func (b Build) DocumentItem() (repoKey, fullPath string) {
	file := EncodeBuildInfoSegment(
		b.Number,
	) + "-" + strconv.FormatInt(
		b.Started.UnixMilli(),
		10,
	) + ".json"
	return BuildInfoRepo(b.ProjectKey), EncodeBuildInfoSegment(b.Name) + "/" + file
}

// BuildInfoRepo returns the repository holding a project's build-info
// documents: artifactory-build-info for the default project ("" or
// "default") and <project>-build-info otherwise.
func BuildInfoRepo(projectKey string) string {
	if projectKey == "" || projectKey == "default" {
		return "artifactory-build-info"
	}
	return EncodeBuildInfoSegment(projectKey) + "-build-info"
}

// buildInfoEncoding is jfrog-client-go's specialAqlCharacters
// (artifactory/services/utils/aqlquerybuilder.go). The apostrophe maps to
// %22, not %27, in the client; it is kept, because the item path must match
// what the CLI queries for.
var buildInfoEncoding = map[rune]string{
	'/':  "%2F",
	'\\': "%5C",
	'|':  "%7C",
	'*':  "%2A",
	'?':  "%3F",
	'\'': "%22",
	':':  "%3A",
	';':  "%3B",
	'%':  "%25",
}

// EncodeBuildInfoSegment encodes a build name, number or project key as the
// CLI's encodeForBuildInfoRepository does: every whitespace rune becomes a
// space, the AQL-special characters are percent-encoded, and a space next
// to an encoded slash becomes %20.
func EncodeBuildInfoSegment(s string) string {
	var b strings.Builder
	for _, r := range s {
		if unicode.IsSpace(r) {
			r = ' '
		}
		if enc, ok := buildInfoEncoding[r]; ok {
			b.WriteString(enc)
			continue
		}
		b.WriteRune(r)
	}
	slash := buildInfoEncoding['/']
	out := strings.ReplaceAll(b.String(), slash+" ", slash+"%20")
	return strings.ReplaceAll(out, " "+slash, "%20"+slash)
}

// BuildRun is one run's identity and timestamps, as listings return it.
type BuildRun struct {
	ID         int64
	ProjectKey string
	Name       string
	Number     string
	Started    time.Time
	Created    time.Time
}

// BuildName is one build name with the start of its latest run, as
// GET /api/build lists them.
type BuildName struct {
	ProjectKey  string
	Name        string
	LastStarted time.Time
}

// BuildModule is one module of a run: what it produced and consumed.
type BuildModule struct {
	// ID is the module's id in the document. A module of type "build"
	// names another build as <name>/<number>.
	ID           string
	Type         string
	Properties   Properties
	Artifacts    []BuildFile
	Dependencies []BuildFile
}

// BuildFile is an artifact a module produced or a dependency it consumed.
// Its checksums link it to items, which is how the AQL build domains join
// (decision 0014).
type BuildFile struct {
	// ID is a dependency's id; artifacts have none.
	ID   string
	Name string
	Type string
	Path string
	// Scopes are a dependency's scopes; artifacts have none.
	Scopes    []string
	Checksums Checksums
}

// BuildFileRef is a build file found by checksum, with the run and module
// it belongs to.
type BuildFileRef struct {
	Run    BuildRun
	Module string
	File   BuildFile
}

// BuildPromotion records one promotion of a run (decision 0014).
type BuildPromotion struct {
	ID      int64
	BuildID int64
	Created time.Time
	// CreatedBy is the user who promoted.
	CreatedBy string
	Status    string
	Comment   string
	// CIUser is the CI user named in the promotion request.
	CIUser     string
	SourceRepo string
	// Repo is the target repository.
	Repo string
}

// BuildRetention is the retention a build-info document asked for, kept per
// build name so later publishes and discards apply it.
type BuildRetention struct {
	ProjectKey string
	Name       string
	// Count keeps at most this many runs; zero keeps every run.
	Count int
	// MinimumBuildDate discards runs started before it; zero keeps them.
	MinimumBuildDate time.Time
	DeleteArtifacts  bool
	// KeepNumbers are build numbers never discarded.
	KeepNumbers []string
}
