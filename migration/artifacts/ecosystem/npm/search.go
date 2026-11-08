package npm

import "encoding/json"

// Keywords is a package.json keywords field as indexing reads it into
// npm.keywords: one string split at commas, and entries that are no
// non-empty string dropped. npm search matches against these, so a keyword
// found by the property search is found by npm search too.
func Keywords(raw json.RawMessage) []string { return keywords(raw) }
