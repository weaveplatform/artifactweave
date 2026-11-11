package buildinfo

var (
	version   = "devel"
	commit    = ""
	buildDate = ""
)

// Version returns the release version or "devel".
func Version() string { return version }

// Commit returns the source commit, if known.
func Commit() string { return commit }

// BuildDate returns the RFC 3339 date stamped at link time, or empty: the
// build time for make build, the commit date for a release build, and
// nothing for the image build.
func BuildDate() string { return buildDate }
