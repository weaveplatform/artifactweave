package source

import (
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"net/http"
	"net/url"
	"strings"
	"unicode"
)

// CocoaPodsGitSource is the closed source contract for a pod's Git checkout.
// The remote backend reads Git objects without executing Git, checking out a
// filesystem, or handing upstream destinations to the downstream client.
type CocoaPodsGitSource struct {
	URL        string `json:"git"`
	Tag        string `json:"tag,omitempty"`
	Branch     string `json:"branch,omitempty"`
	Commit     string `json:"commit,omitempty"`
	Submodules bool   `json:"submodules,omitempty"`
}

func (g CocoaPodsGitSource) Validate() error {
	u, err := url.Parse(g.URL)
	if err != nil || u == nil || u.Host == "" || u.User != nil ||
		(u.Scheme != "https" && u.Scheme != "http") || u.RawQuery != "" ||
		u.Fragment != "" || len(g.URL) > 2048 || u.Path == "" || u.Path == "/" ||
		strings.Contains(u.EscapedPath(), "%") || strings.ContainsAny(u.Path, "\\\x00\r\n") {
		return ErrInvalidRequest
	}
	for _, part := range strings.Split(strings.Trim(u.Path, "/"), "/") {
		if part == "" || part == "." || part == ".." {
			return ErrInvalidRequest
		}
	}
	for _, ref := range []string{g.Tag, g.Branch} {
		if ref != "" && !validCocoaPodsGitRef(ref) {
			return ErrInvalidRequest
		}
	}
	if g.Commit != "" {
		if len(g.Commit) != 40 {
			return ErrInvalidRequest
		}
		if _, err := hex.DecodeString(g.Commit); err != nil || g.Commit == strings.Repeat("0", 40) {
			return ErrInvalidRequest
		}
	}
	return nil
}

func validCocoaPodsGitRef(ref string) bool {
	if len(ref) > 256 || ref == "@" || strings.HasPrefix(ref, "-") ||
		strings.Contains(ref, "..") || strings.Contains(ref, "@{") ||
		strings.ContainsAny(ref, " ~^:?*[\\") {
		return false
	}
	for _, r := range ref {
		if unicode.IsControl(r) {
			return false
		}
	}
	for _, part := range strings.Split(ref, "/") {
		if part == "" || strings.HasPrefix(part, ".") || strings.HasSuffix(part, ".") ||
			strings.HasSuffix(part, ".lock") {
			return false
		}
	}
	return true
}

// Variant binds the cached archive to every checkout option, including the
// requested revision and whether Git submodules are materialized.
func (g CocoaPodsGitSource) Variant() string {
	data, _ := json.Marshal(g)
	hash := sha256.Sum256(data)
	return "cocoapods-git-v1:" + hex.EncodeToString(hash[:])
}

func validateCocoaPodsGitContract(r Resource) error {
	if r.CocoaPodsGit == nil {
		return nil
	}
	g := *r.CocoaPodsGit
	if g.Validate() != nil || r.Kind != ResourceKindArtifact ||
		(r.Method != http.MethodGet && r.Method != http.MethodHead) ||
		r.ExternalURL != g.URL || r.Variant != g.Variant() ||
		!strings.HasSuffix(r.Path, "/source.tar.gz") || !r.Mutable ||
		r.Validate == nil || r.CheckURL == nil || !r.NoCrossOriginAuth ||
		r.UpstreamPath != "" || r.Query != "" || r.PermissionRoot || r.PermissionFolder ||
		len(r.PostBody) != 0 || r.Transient || r.RequireSmart || r.SmartAQL != "" ||
		r.ModuleDownloadLocation || r.NuGetEmptyFeed || r.ChecksumOnly ||
		len(r.GitLFSActionHeaders) != 0 {
		return ErrInvalidRequest
	}
	return nil
}
