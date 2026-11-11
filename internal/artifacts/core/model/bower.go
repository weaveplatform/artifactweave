package model

// BowerRegistration binds a registry name to a Git locator. It contains no
// credentials and belongs to one repository lifetime, independently of cache.
type BowerRegistration struct {
	RepoKey string `json:"repoKey"` //nolint:tagliatelle // P9d renamed no JSON key (decision 0044)
	Name    string `json:"name"`
	URL     string `json:"url"`
}
