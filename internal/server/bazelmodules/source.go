package bazelmodules

import (
	"context"
	"crypto/sha256"
	"encoding/base64"
	"encoding/hex"
	"encoding/json"
	"net/url"
	"path"
	"strings"

	"github.com/weaveplatform/artifactweave/internal/authz"
	"github.com/weaveplatform/artifactweave/pkg/repo"
)

// sourceDocument is the archive variant of Bazel's source.json. Source URLs
// are replaced on delivery so the Bazel client never fetches from upstream.
type sourceDocument struct {
	Type        string            `json:"type,omitempty"`
	URL         string            `json:"url"`
	MirrorURLs  []string          `json:"mirror_urls,omitempty"`
	Integrity   string            `json:"integrity"`
	StripPrefix string            `json:"strip_prefix,omitempty"`
	Patches     map[string]string `json:"patches,omitempty"`
	PatchStrip  int               `json:"patch_strip,omitempty"`
	ArchiveType string            `json:"archive_type,omitempty"`
}

func (d sourceDocument) sha256() string {
	raw, err := base64.StdEncoding.DecodeString(strings.TrimPrefix(d.Integrity, "sha256-"))
	if err != nil || len(raw) != sha256.Size {
		return ""
	}
	return hex.EncodeToString(raw)
}

func (d sourceDocument) archiveFile() string {
	t := d.ArchiveType
	if t == "" {
		u, _ := url.Parse(d.URL)
		for _, suffix := range []string{"tar.gz", "tgz", "tar", "zip"} {
			if strings.HasSuffix(u.Path, "."+suffix) {
				t = suffix
				break
			}
		}
	}
	if t == "tgz" {
		t = "tar.gz"
	}
	return "source." + t
}

func parseSource(body []byte, c repo.Config) (sourceDocument, error) {
	var d sourceDocument
	if len(body) == 0 || len(body) > metadataLimit || json.Unmarshal(body, &d) != nil ||
		(d.Type != "" && d.Type != "archive") || d.sha256() == "" ||
		len(d.MirrorURLs) > 32 || len(d.Patches) > 128 || d.PatchStrip < 0 || d.PatchStrip > 32 ||
		len(d.StripPrefix) > 1024 || strings.Contains(d.StripPrefix, "..") ||
		strings.HasPrefix(d.StripPrefix, "/") || strings.ContainsAny(d.StripPrefix, "\\\x00\r\n") {
		return sourceDocument{}, invalid()
	}
	for name, integrity := range d.Patches {
		if !safeBase(name) || !strings.HasPrefix(integrity, "sha256-") {
			return sourceDocument{}, invalid()
		}
		raw, e := base64.StdEncoding.DecodeString(strings.TrimPrefix(integrity, "sha256-"))
		if e != nil || len(raw) != sha256.Size {
			return sourceDocument{}, invalid()
		}
	}
	if d.ArchiveType != "" && d.ArchiveType != "tar.gz" && d.ArchiveType != "tgz" && d.ArchiveType != "tar" && d.ArchiveType != "zip" {
		return sourceDocument{}, invalid()
	}
	if d.archiveFile() == "source." || allowSourceURL(c, d.URL) != nil {
		return sourceDocument{}, invalid()
	}
	for _, mirror := range d.MirrorURLs {
		if allowSourceURL(c, mirror) != nil {
			return sourceDocument{}, invalid()
		}
	}
	// Mirrored upstream origins would let Bazel bypass artifactweave policy.
	d.MirrorURLs = nil
	return d, nil
}

func allowSourceURL(c repo.Config, raw string) error {
	u, err := url.Parse(raw)
	if err != nil || len(raw) > 8192 || u.Hostname() == "" || u.User != nil || u.Fragment != "" ||
		u.Opaque != "" || strings.ContainsAny(raw, "\\\x00\r\n") ||
		(u.Scheme != "https" && u.Scheme != "http") {
		return invalid()
	}
	for _, segment := range strings.Split(u.EscapedPath(), "/") {
		decoded, e := url.PathUnescape(segment)
		if e != nil || decoded == "." || decoded == ".." || strings.ContainsAny(decoded, "\\\x00\r\n") {
			return invalid()
		}
	}
	base, err := url.Parse(c.URL)
	if err == nil && base.Scheme == u.Scheme && strings.EqualFold(base.Host, u.Host) &&
		(path.Clean(u.Path) == path.Clean(base.Path) || strings.HasPrefix(path.Clean(u.Path), strings.TrimSuffix(path.Clean(base.Path), "/")+"/")) {
		return nil
	}
	if c.ExternalDependenciesEnabled != nil && *c.ExternalDependenciesEnabled {
		for _, pattern := range c.ExternalDependenciesPatterns {
			if pattern != "" && authz.CompilePattern(pattern).Match(raw) {
				return nil
			}
		}
	}
	return invalid()
}

func sourceURLGuard(c repo.Config, initial string) func(context.Context, *url.URL) error {
	return func(_ context.Context, target *url.URL) error {
		if target == nil || (initial != "" && target.Scheme == "http" && strings.HasPrefix(initial, "https://")) {
			return invalid()
		}
		return allowSourceURL(c, target.String())
	}
}
