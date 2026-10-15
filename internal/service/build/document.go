package build

import (
	"bytes"
	"encoding/json"
	"fmt"
	"maps"
	"slices"
	"strings"
	"time"

	"github.com/weaveplatform/artifactweave/pkg/types"
)

// StartedLayout is the layout of started in build-info documents and in
// the started query parameter: yyyy-MM-dd'T'HH:mm:ss.SSSZ, as the JFrog CLI
// writes it (research 22 §3.2).
const StartedLayout = "2006-01-02T15:04:05.000-0700"

// FormatStarted renders t in StartedLayout, in UTC.
func FormatStarted(t time.Time) string { return t.UTC().Format(StartedLayout) }

// ParseStarted reads a started timestamp in StartedLayout, or in RFC 3339
// as other clients write it.
func ParseStarted(s string) (time.Time, error) {
	// An unencoded + in a query string arrives as a space; the offset is
	// the only place the layout can hold one.
	s = strings.Replace(strings.TrimSpace(s), " ", "+", 1)
	for _, layout := range []string{StartedLayout, time.RFC3339Nano} {
		if t, err := time.Parse(layout, s); err == nil {
			return t.UTC(), nil
		}
	}
	return time.Time{}, fmt.Errorf(
		"%w: started %q is not in the layout yyyy-MM-dd'T'HH:mm:ss.SSSZ", ErrInvalid, s)
}

// document is what the store indexes of a build-info document. The
// document itself is kept as published, so fields this leaves out (agent,
// vcs, issues and the rest) survive.
type document struct {
	Name       string                     `json:"name"`
	Number     string                     `json:"number"`
	Started    string                     `json:"started"`
	URL        string                     `json:"url"`
	Properties map[string]json.RawMessage `json:"properties"`
	Modules    []module                   `json:"modules"`
}

type module struct {
	ID           string                     `json:"id"`
	Type         string                     `json:"type"`
	Properties   map[string]json.RawMessage `json:"properties"`
	Artifacts    []file                     `json:"artifacts"`
	Dependencies []file                     `json:"dependencies"`
}

type file struct {
	ID     string   `json:"id"`
	Name   string   `json:"name"`
	Type   string   `json:"type"`
	Path   string   `json:"path"`
	Scopes []string `json:"scopes"`
	SHA1   string   `json:"sha1"`
	SHA256 string   `json:"sha256"`
	MD5    string   `json:"md5"`
}

// Parse reads a build-info document into the run it publishes for a
// project. The run keeps raw as its document.
func Parse(projectKey string, raw []byte) (types.Build, error) {
	var d document
	if err := json.Unmarshal(raw, &d); err != nil {
		return types.Build{}, fmt.Errorf("%w: the build-info document is not valid JSON: %w",
			ErrInvalid, err)
	}
	switch {
	case strings.TrimSpace(d.Name) == "":
		return types.Build{}, fmt.Errorf("%w: the build-info document has no name", ErrInvalid)
	case strings.TrimSpace(d.Number) == "":
		return types.Build{}, fmt.Errorf("%w: the build-info document has no number", ErrInvalid)
	case d.Started == "":
		return types.Build{}, fmt.Errorf("%w: the build-info document has no started", ErrInvalid)
	}
	started, err := ParseStarted(d.Started)
	if err != nil {
		return types.Build{}, err
	}
	b := types.Build{
		ProjectKey: projectKey, Name: d.Name, Number: d.Number, Started: started, URL: d.URL,
		Properties: properties(d.Properties), Document: raw,
	}
	for _, m := range d.Modules {
		bm := types.BuildModule{ID: m.ID, Type: m.Type, Properties: properties(m.Properties)}
		for _, f := range m.Artifacts {
			bm.Artifacts = append(bm.Artifacts, f.build())
		}
		for _, f := range m.Dependencies {
			bm.Dependencies = append(bm.Dependencies, f.build())
		}
		b.Modules = append(b.Modules, bm)
	}
	return b, nil
}

func (f file) build() types.BuildFile {
	return types.BuildFile{
		ID: f.ID, Name: f.Name, Type: f.Type, Path: f.Path, Scopes: f.Scopes,
		Checksums: types.Checksums{SHA1: f.SHA1, SHA256: f.SHA256, MD5: f.MD5},
	}
}

// properties converts a document's properties to the store's: a string is
// its value, null is no value, and anything else is kept as its JSON text,
// so a number keeps the digits the client sent.
func properties(in map[string]json.RawMessage) types.Properties {
	out := types.Properties{}
	for k, raw := range in {
		var s string
		switch {
		case bytes.Equal(raw, []byte("null")):
		case json.Unmarshal(raw, &s) == nil:
			out.Add(k, s)
		default:
			out.Add(k, string(raw))
		}
	}
	return out
}

// setFields returns the JSON object raw with the top-level fields in set
// replaced, or added at its end, leaving every other field's bytes and the
// order of the fields as they were.
func setFields(raw []byte, set map[string]json.RawMessage) ([]byte, error) {
	dec := json.NewDecoder(bytes.NewReader(raw))
	if tok, err := dec.Token(); err != nil || tok != json.Delim('{') {
		return nil, fmt.Errorf(
			"%w: the stored build-info document is not a JSON object",
			ErrInvalid,
		)
	}
	var b bytes.Buffer
	b.WriteByte('{')
	done := map[string]bool{}
	write := func(k string, v json.RawMessage) {
		if b.Len() > 1 {
			b.WriteByte(',')
		}
		key, _ := json.Marshal(k)
		b.Write(key)
		b.WriteByte(':')
		b.Write(v)
	}
	for dec.More() {
		tok, err := dec.Token()
		if err != nil {
			return nil, fmt.Errorf("%w: the stored build-info document: %w", ErrInvalid, err)
		}
		k, _ := tok.(string)
		var v json.RawMessage
		if err := dec.Decode(&v); err != nil {
			return nil, fmt.Errorf("%w: the stored build-info document: %w", ErrInvalid, err)
		}
		if nv, ok := set[k]; ok {
			v, done[k] = nv, true
		}
		write(k, v)
	}
	for _, k := range slices.Sorted(maps.Keys(set)) {
		if !done[k] {
			write(k, set[k])
		}
	}
	b.WriteByte('}')
	return b.Bytes(), nil
}
