package vcs

import (
	"errors"
	"net/url"
	"strings"
	"unicode"

	"github.com/weaveplatform/artifactweave/pkg/repo"
)

var ErrInvalidPath = errors.New("vcs: invalid vcs path")

// ValidProject accepts the provider's owner path and repository name. GitLab
// owners can contain subgroups; the number of segments is bounded because this
// path also becomes a source URL and an authorization/cache key.
func ValidProject(owner, name string) bool {
	if len(owner) == 0 || len(owner) > 512 || len(name) == 0 || len(name) > 128 {
		return false
	}
	parts := strings.Split(owner, "/")
	if len(parts) > 8 || !validProjectSegment(name) {
		return false
	}
	for _, part := range parts {
		if !validProjectSegment(part) {
			return false
		}
	}
	return true
}

func validProjectSegment(s string) bool {
	if len(s) == 0 || len(s) > 128 || s == "." || s == ".." ||
		strings.HasPrefix(s, ".") || strings.HasSuffix(s, ".") {
		return false
	}
	for _, r := range s {
		if !unicode.IsLetter(r) && !unicode.IsDigit(r) && r != '-' && r != '_' && r != '.' {
			return false
		}
	}
	return true
}

// ValidRef is a conservative Git branch/tag name. It rejects syntax that can
// alter a URL's authority or path meaning when passed to a provider.
func ValidRef(name string) bool {
	_, _, ok := refName("refs/heads/" + name)
	return ok && !strings.HasSuffix(name, "^{}")
}

func ValidFilePath(path string) bool {
	if len(path) == 0 || len(path) > 1024 || strings.HasPrefix(path, "/") ||
		strings.HasSuffix(path, "/") || strings.Contains(path, "\\") ||
		strings.Contains(path, "!") || strings.Contains(path, "%") {
		return false
	}
	for _, part := range strings.Split(path, "/") {
		if part == "" || part == "." || part == ".." {
			return false
		}
		for _, r := range part {
			if r < 0x20 || r == 0x7f {
				return false
			}
		}
	}
	return true
}

func ValidExtension(ext string) bool { return ext == "tar.gz" || ext == "zip" }

// GitRefsURL points at Git smart-HTTP discovery on the configured provider.
// It does not ask for protocol v2, so a v1 ref advertisement is expected.
func GitRefsURL(c repo.Config, owner, name string) (*url.URL, error) {
	if !ValidProject(owner, name) {
		return nil, ErrInvalidPath
	}
	u, err := providerBase(c.URL)
	if err != nil {
		return nil, err
	}
	u.Path = strings.TrimSuffix(u.Path, "/") + "/" + owner + "/" + name +
		".git/info/refs"
	u.RawPath = ""
	u.RawQuery = "service=git-upload-pack"
	return u, nil
}

func providerBase(raw string) (*url.URL, error) {
	u, err := url.Parse(raw)
	if err != nil || u == nil || (u.Scheme != "http" && u.Scheme != "https") ||
		u.Host == "" || u.User != nil || u.RawQuery != "" || u.Fragment != "" ||
		strings.Contains(u.EscapedPath(), "%") {
		return nil, ErrInvalidPath
	}
	return u, nil
}

// ArchiveURL builds an archive address for a provider's HTTP source. The
// returned URL remains subject to the configured remote destination policy.
func ArchiveURL(c repo.Config, owner, name, ref, ext string) (*url.URL, error) {
	if !ValidProject(owner, name) || !ValidRef(ref) || !ValidExtension(ext) {
		return nil, ErrInvalidPath
	}
	u, err := providerBase(c.URL)
	if err != nil {
		return nil, err
	}
	root := strings.TrimSuffix(u.Path, "/") + "/" + owner + "/" + name
	switch c.VCSGitProvider {
	case repo.GitHub, repo.GitHubEnterprise, "":
		u.Path = root + "/archive/" + ref + "." + ext
	case repo.Bitbucket:
		u.Path = root + "/get/" + ref + "." + ext
	case repo.GitLab:
		u.Path = root + "/-/archive/" + ref + "/" + name + "-" + ref + "." + ext
	case repo.CustomVCS:
		return customArchiveURL(c, u, owner, name, ref, ext)
	default:
		return nil, ErrInvalidPath
	}
	u.RawPath = ""
	return u, nil
}

func customArchiveURL(
	c repo.Config,
	base *url.URL,
	owner, name, ref, ext string,
) (*url.URL, error) {
	template := c.VCSGitDownloadURL
	if template == "" || strings.ContainsAny(template, "?#%\\") {
		return nil, ErrInvalidPath
	}
	for from, to := range map[string]string{"{0}": owner, "{1}": name, "{2}": ref, "{3}": ext} {
		template = strings.ReplaceAll(template, from, to)
	}
	if strings.ContainsAny(template, "{}") || strings.HasPrefix(template, "/") ||
		strings.Contains(template, "//") || strings.Contains(template, "..") {
		return nil, ErrInvalidPath
	}
	u := *base
	u.Path = strings.TrimSuffix(base.Path, "/") + "/" + template
	u.RawPath = ""
	return &u, nil
}
