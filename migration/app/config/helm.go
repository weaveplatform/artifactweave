package config

// Helm controls the classic Helm repository protocol (decision 0039).
type Helm struct {
	// RelativeURLs serves chart paths relative to the repository URL. It
	// defaults on, matching Artifactory releases after November 2024.
	RelativeURLs bool `yaml:"relative_urls"`
}
