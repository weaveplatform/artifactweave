package pypi

import (
	"bytes"
	"cmp"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"fmt"
	"slices"
	"time"
)

// Metadata is a version's document in the package index
// (PackageVersion.Metadata, decision 0037): one record per file, from which
// the simple index, the PEP 658 metadata files and the JSON API are built,
// so a read needs neither the files nor their properties. Everything in it
// follows from the files, their stored checksums and their properties, so
// a reindex writes the same bytes.
type Metadata struct {
	// Files are the version's files, ordered by file name, then path, the
	// order the project page lists them in (decision 0037).
	Files []File `json:"files"`
	// Texts are the core metadata files of Files by their SHA-256, each
	// kept once: a version's wheels mostly share one METADATA.
	Texts map[string]string `json:"texts,omitempty"`
}

// File is one file of a version as the index keeps it.
type File struct {
	// Path is the file's repository-relative path; Name its file name.
	Path string `json:"path"`
	Name string `json:"filename"`
	// SHA256 and MD5 are the stored content's, in hex, and Size its
	// length.
	SHA256 string `json:"sha256"`
	MD5    string `json:"md5"`
	Size   int64  `json:"size"`
	// Uploaded is when the file was last deployed, in UTC to the
	// microsecond the stores keep: PEP 700's upload-time.
	Uploaded time.Time `json:"uploaded"`
	// RequiresPython is the metadata's Requires-Python, or, for a file
	// indexed by its properties, its pypi.requires.python.
	RequiresPython string `json:"requires_python,omitempty"`
	// Yanked and YankedReason follow pypi.yanked.
	Yanked       bool   `json:"yanked,omitempty"`
	YankedReason string `json:"yanked_reason,omitempty"`
	// Metadata is the key in Texts of the file's core metadata, empty for
	// a file indexed by its properties alone.
	Metadata string `json:"metadata,omitempty"`
	// Core reports that the text under Metadata is the wheel's whole
	// METADATA, which PEP 658 serves; its key is then the SHA-256 the
	// pages advertise.
	Core bool `json:"core,omitempty"`
}

// DecodeMetadata reads a version's Metadata.
func DecodeMetadata(b []byte) (Metadata, error) {
	var m Metadata
	if err := json.Unmarshal(b, &m); err != nil {
		return m, fmt.Errorf("pypi: version metadata: %w", err)
	}
	return m, nil
}

// Text is the core metadata of f, or "" for a file without it.
func (m Metadata) Text(f File) string { return m.Texts[f.Metadata] }

// put makes f, with its core metadata text, the record at f.Path.
func (m *Metadata) put(f File, text string) {
	m.drop(f.Path)
	if f.Metadata != "" {
		if m.Texts == nil {
			m.Texts = map[string]string{}
		}
		m.Texts[f.Metadata] = text
	}
	m.Files = append(m.Files, f)
	slices.SortFunc(m.Files, func(a, b File) int {
		return cmp.Or(cmp.Compare(a.Name, b.Name), cmp.Compare(a.Path, b.Path))
	})
}

// drop removes the record at p and every text no record names any more;
// it reports whether there was one.
func (m *Metadata) drop(p string) bool {
	i := slices.IndexFunc(m.Files, func(f File) bool { return f.Path == p })
	if i < 0 {
		return false
	}
	m.Files = slices.Delete(m.Files, i, i+1)
	for k := range m.Texts {
		if !slices.ContainsFunc(m.Files, func(f File) bool { return f.Metadata == k }) {
			delete(m.Texts, k)
		}
	}
	if len(m.Texts) == 0 {
		m.Texts = nil
	}
	return true
}

// find is the record at p.
func (m Metadata) find(p string) (File, bool) {
	i := slices.IndexFunc(m.Files, func(f File) bool { return f.Path == p })
	if i < 0 {
		return File{}, false
	}
	return m.Files[i], true
}

// created is when the version's earliest file was deployed: the version's
// date in the index, which a delete of that file moves on.
func (m Metadata) created() time.Time {
	var t time.Time
	for _, f := range m.Files {
		if t.IsZero() || f.Uploaded.Before(t) {
			t = f.Uploaded
		}
	}
	return t
}

// encode is m as stored: the same Metadata always gives the same bytes,
// which is what lets a reindex leave the index unchanged. HTML is not
// escaped, so a Requires-Python of ">=3.9" reads as written.
func (m Metadata) encode() []byte {
	var b bytes.Buffer
	e := json.NewEncoder(&b)
	e.SetEscapeHTML(false)
	// Strings, numbers, times and maps of strings always marshal.
	_ = e.Encode(m)
	return bytes.TrimSuffix(b.Bytes(), []byte("\n"))
}

// textKey is the key of a core metadata text in Texts.
func textKey(text string) string {
	sum := sha256.Sum256([]byte(text))
	return hex.EncodeToString(sum[:])
}
