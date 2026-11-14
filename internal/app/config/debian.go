package config

// Debian controls how strictly Debian binary packages are read before they
// are indexed.
type Debian struct {
	// MetadataValidation is 7.x's debian.metadata.validation, on by default
	// (JFrog's "Use Debian Metadata Validation"): a package with an empty
	// control field, or no Maintainer or Description, is stored but marked
	// malformed and left out of the index. Off, such a package is indexed.
	MetadataValidation bool `yaml:"metadata_validation"`
}
