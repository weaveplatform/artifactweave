package ledger

import (
	"bytes"
	"encoding/json"
	"slices"
	"strconv"
	"strings"
)

// Format returns the canonical bytes of l: the header keys in struct order,
// then the packages and files sorted by from and the placeholders sorted by
// to, each entry on one line as compact JSON in struct field order, indented
// four spaces per level, with a trailing newline. One entry per line keeps
// the conflicts of parallel pull requests on their own lines. HTML
// characters are not escaped, so notes read as written. Format does not
// change l.
func Format(l *Ledger) []byte {
	var b bytes.Buffer
	b.WriteString("{\n")
	b.WriteString(`    "schema": ` + strconv.Itoa(l.Schema) + ",\n")
	for _, kv := range [][2]string{
		{"module", l.Module},
		{"staging_root", l.StagingRoot},
		{"final_root", l.FinalRoot},
	} {
		b.WriteString(`    "` + kv[0] + `": `)
		b.Write(compact(kv[1]))
		b.WriteString(",\n")
	}
	packages := sortedBy(l.Packages, func(p Package) string { return p.From })
	files := sortedBy(l.Files, func(f File) string { return f.From })
	placeholders := sortedBy(l.Placeholders, func(p Placeholder) string { return p.To })
	writeEntries(&b, "packages", packages, ",")
	writeEntries(&b, "files", files, ",")
	writeEntries(&b, "placeholders", placeholders, "")
	b.WriteString("}\n")
	return b.Bytes()
}

func sortedBy[E any](entries []E, key func(E) string) []E {
	sorted := slices.Clone(entries)
	slices.SortStableFunc(sorted, func(a, b E) int { return strings.Compare(key(a), key(b)) })
	return sorted
}

func writeEntries[E any](b *bytes.Buffer, key string, entries []E, after string) {
	b.WriteString(`    "` + key + `": [`)
	for i, e := range entries {
		if i > 0 {
			b.WriteString(",")
		}
		b.WriteString("\n        ")
		b.Write(compact(e))
	}
	if len(entries) > 0 {
		b.WriteString("\n    ")
	}
	b.WriteString("]" + after + "\n")
}

// compact encodes v on one line without HTML escaping.
func compact(v any) []byte {
	var b bytes.Buffer
	enc := json.NewEncoder(&b)
	enc.SetEscapeHTML(false)
	// The ledger holds only strings, string slices and string maps, which
	// always encode, and a bytes.Buffer write cannot fail, so there is no
	// error to handle.
	_ = enc.Encode(v)
	return bytes.TrimSuffix(b.Bytes(), []byte("\n"))
}
