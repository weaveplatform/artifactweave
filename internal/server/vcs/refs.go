// Package vcs implements the remote-only Artifactory VCS archive protocol.
package vcs

import (
	"encoding/hex"
	"errors"
	"strconv"
	"strings"
)

const (
	MaxRefsBytes = 8 << 20
	MaxRefs      = 65536
)

var ErrInvalidRefs = errors.New("vcs: invalid vcs references")

// Ref is a Git branch or tag and the object ID it resolves to. Annotated tags
// use the peeled object ID when one is advertised.
type Ref struct {
	Name     string
	CommitID string
	Branch   bool
}

// ParseRefs accepts the packet-lined Git upload-pack discovery response. This
// is the data Git providers expose at info/refs?service=git-upload-pack without
// a Git-Protocol: version=2 request header.
func ParseRefs(data []byte) ([]Ref, error) {
	if len(data) == 0 || len(data) > MaxRefsBytes {
		return nil, ErrInvalidRefs
	}
	refs := make([]Ref, 0)
	indexes := make(map[string]int)
	peeled := make(map[string]string)
	for len(data) != 0 {
		if len(data) < 4 {
			return nil, ErrInvalidRefs
		}
		size, err := strconv.ParseUint(string(data[:4]), 16, 16)
		if err != nil || size != 0 && (size < 4 || int(size) > len(data)) {
			return nil, ErrInvalidRefs
		}
		if size == 0 {
			data = data[4:]
			continue
		}
		line := string(data[4:size])
		data = data[size:]
		if strings.HasPrefix(line, "# service=") {
			continue
		}
		line = strings.TrimSuffix(strings.SplitN(line, "\x00", 2)[0], "\n")
		oid, name, ok := strings.Cut(line, " ")
		if !ok || !validOID(oid) {
			return nil, ErrInvalidRefs
		}
		branch, short, ok := refName(name)
		if !ok {
			if name == "HEAD" {
				continue
			}
			return nil, ErrInvalidRefs
		}
		if strings.HasSuffix(short, "^{}") {
			base := strings.TrimSuffix(name, "^{}")
			if _, exists := peeled[base]; exists {
				return nil, ErrInvalidRefs
			}
			peeled[base] = oid
			continue
		}
		if len(refs) >= MaxRefs || indexes[name] != 0 {
			return nil, ErrInvalidRefs
		}
		indexes[name] = len(refs) + 1
		refs = append(refs, Ref{Name: short, CommitID: oid, Branch: branch})
	}
	for name, oid := range peeled {
		index := indexes[name]
		if index == 0 {
			return nil, ErrInvalidRefs
		}
		refs[index-1].CommitID = oid
	}
	return refs, nil
}

func validOID(s string) bool {
	if len(s) != 40 && len(s) != 64 {
		return false
	}
	_, err := hex.DecodeString(s)
	return err == nil && s == strings.ToLower(s)
}

// ValidCommitID accepts the Git SHA-1 and SHA-256 object ID forms.
func ValidCommitID(s string) bool { return validOID(s) }

func refName(s string) (branch bool, short string, ok bool) {
	switch {
	case strings.HasPrefix(s, "refs/heads/"):
		branch, short = true, strings.TrimPrefix(s, "refs/heads/")
	case strings.HasPrefix(s, "refs/tags/"):
		short = strings.TrimPrefix(s, "refs/tags/")
	default:
		return false, "", false
	}
	bare := strings.TrimSuffix(short, "^{}")
	if bare == "" || len(bare) > 256 || strings.HasPrefix(bare, "/") ||
		strings.HasSuffix(bare, "/") || strings.Contains(bare, "//") ||
		strings.Contains(bare, "..") || strings.Contains(bare, "@{") ||
		strings.ContainsAny(bare, "\\~^:?*[") {
		return false, "", false
	}
	for _, r := range bare {
		if r <= ' ' || r == 0x7f {
			return false, "", false
		}
	}
	for part := range strings.SplitSeq(bare, "/") {
		if part == "." || strings.HasPrefix(part, ".") || strings.HasSuffix(part, ".lock") ||
			strings.HasSuffix(part, ".") {
			return false, "", false
		}
	}
	return branch, short, true
}
