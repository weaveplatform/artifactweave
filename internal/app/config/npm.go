package config

// NPMTag holds the npm format's dist-tag option (decision 0034).
type NPMTag struct {
	// TagLatestByPublish gives latest to the version deployed last rather
	// than the highest, as 7.x's global
	// artifactory.npm.tag.tagLatestByPublish does. Off by default, as
	// there.
	TagLatestByPublish bool `yaml:"tag_latest_by_publish"`
	// MergeDistTagsByVersion is 7.75.3's optional custom dist-tag merge strategy.
	// The default keeps the first member's tag, independently of publish date.
	MergeDistTagsByVersion bool `yaml:"merge_dist_tags_by_version"`
}

// NPMPath holds the npm format's upload path option (decision 0034).
type NPMPath struct {
	// EnforcePath refuses the upload of a package tarball whose file name
	// is not the one its package.json gives, in every npm local
	// repository, as 7.x's global artifactory.npm.enforce.path does. Off
	// by default, as there.
	EnforcePath bool `yaml:"enforce_path"`
}
