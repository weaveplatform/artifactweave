package model

// NPMExternal binds a rendered dist-tag to its fetched URL across restarts.
// SHA256 records admitted bytes for offline cache-path collision checks; it is
// never permission or authority to read those bytes.
type NPMExternal struct {
	Virtual string
	Name    string
	Spec    string
	URL     string
	File    string
	SHA256  string
}
