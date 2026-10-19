package config

// NPMTagConfig holds the npm format's dist-tag option (decision 0034).
type NPMTagConfig struct {
	// TagLatestByPublish gives latest to the version deployed last rather
	// than the highest, as 7.x's global
	// artifactory.npm.tag.tagLatestByPublish does. Off by default, as
	// there.
	TagLatestByPublish bool `yaml:"tag_latest_by_publish"`
}
