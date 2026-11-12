package terraformhttp

import (
	"context"
	"encoding/hex"
	"encoding/json"
	"io"
	"net/http"
	"net/url"
	"path"
	"strconv"
	"strings"
	"unicode"

	"github.com/weaveplatform/artifactweave/internal/artifacts/api/repo"
	tf "github.com/weaveplatform/artifactweave/internal/artifacts/ecosystem/terraform"
	"github.com/weaveplatform/artifactweave/internal/kernel"
	"github.com/weaveplatform/artifactweave/internal/kernel/antpath"
	"github.com/weaveplatform/artifactweave/internal/platform/httpkit/apierror"
)

const (
	moduleMetadataLimit  = 16 << 20
	moduleVersionLimit   = 100000
	moduleDiscoveryLimit = 256
)

type moduleSource struct {
	Target    string           `json:"target"`
	Subdir    string           `json:"subdir"`
	Archive   string           `json:"archive"`
	Checksums kernel.Checksums `json:"checksums"`
	// Path is the archive's cache path, where its read checks Read
	// permission and the remote's download policy and the cache keeps it.
	Path string `json:"path"`
}

func remoteModuleError() error {
	return apierror.New(
		http.StatusBadGateway,
		"invalid upstream Terraform module metadata or archive",
	)
}

func readModuleJSON(r io.Reader, v any) error {
	b, err := io.ReadAll(io.LimitReader(r, moduleMetadataLimit+1))
	if err != nil || len(b) > moduleMetadataLimit || json.Unmarshal(b, v) != nil {
		return remoteModuleError()
	}
	return nil
}

func moduleURL(base, raw string) (string, error) {
	b, err := url.Parse(base)
	if err != nil {
		return "", remoteModuleError()
	}
	u, err := url.Parse(raw)
	if err != nil || raw == "" || u.User != nil || u.Opaque != "" || u.Fragment != "" ||
		strings.ContainsAny(raw, "\\\x00\r\n") {
		return "", remoteModuleError()
	}
	u = b.ResolveReference(u)
	if u.Hostname() == "" || u.User != nil || (u.Scheme != "https" && u.Scheme != "http") ||
		(b.Scheme == "https" && u.Scheme != "https") {
		return "", remoteModuleError()
	}
	for _, seg := range strings.Split(u.Path, "/") {
		if seg == "." || seg == ".." || strings.ContainsAny(seg, "\\\x00\r\n") {
			return "", remoteModuleError()
		}
	}
	return u.String(), nil
}

func parseModuleDiscovery(r io.Reader, base string) (string, error) {
	var doc map[string]json.RawMessage
	if readModuleJSON(r, &doc) != nil || doc == nil {
		return "", remoteModuleError()
	}
	var service string
	if json.Unmarshal(doc["modules.v1"], &service) != nil || service == "" {
		return "", remoteModuleError()
	}
	target, err := moduleURL(base, service)
	if err != nil {
		return "", err
	}
	u, _ := url.Parse(target)
	if u.RawQuery != "" || !strings.HasSuffix(u.Path, "/") {
		return "", remoteModuleError()
	}
	return target, nil
}

func parseModuleVersions(r io.Reader, m tf.Module) ([]string, error) {
	var doc struct {
		Modules []struct {
			Versions []struct {
				Version string `json:"version"`
			} `json:"versions"`
		} `json:"modules"`
	}
	if readModuleJSON(r, &doc) != nil || len(doc.Modules) != 1 || doc.Modules[0].Versions == nil ||
		len(doc.Modules[0].Versions) > moduleVersionLimit {
		return nil, remoteModuleError()
	}
	out := make([]string, 0, len(doc.Modules[0].Versions))
	seen := map[string]bool{}
	for _, entry := range doc.Modules[0].Versions {
		parsed, ok := tf.ParseModule(m.Package() + "/" + entry.Version + ".zip")
		if !ok || parsed.Version != entry.Version || seen[entry.Version] {
			return nil, remoteModuleError()
		}
		seen[entry.Version] = true
		out = append(out, entry.Version)
	}
	return out, nil
}

func parseModuleLocation(r io.Reader, base string, c repo.Config) (moduleSource, error) {
	var doc map[string]string
	if readModuleJSON(r, &doc) != nil || len(doc) != 1 {
		return moduleSource{}, remoteModuleError()
	}
	return parseModuleSource(doc["source"], base, c)
}

// The registry protocol delegates to Terraform's source grammar. This adapter
// admits HTTP archives and GitHub HTTPS sources as documented for Artifactory;
// it never launches a process or follows ambient git, SSH or cloud credentials.
func parseModuleSource(raw, base string, c repo.Config) (moduleSource, error) {
	var out moduleSource
	if raw == "" || len(raw) > 8192 || strings.ContainsFunc(raw, unicode.IsControl) ||
		strings.Contains(raw, "\\") {
		return out, remoteModuleError()
	}
	force := ""
	if i := strings.Index(raw, "::"); i >= 0 {
		force, raw = raw[:i], raw[i+2:]
	}
	if force != "" && force != "git" && force != "http" && force != "https" {
		return out, apierror.New(502, "upstream Terraform module source transport is not qualified")
	}
	if strings.HasPrefix(raw, "github.com/") {
		raw = "https://" + raw
		force = "git"
	}
	// Separate the getter's // selector before URL resolution (which cleans paths).
	end := len(raw)
	if i := strings.IndexByte(raw, '?'); i >= 0 {
		end = i
	}
	offset := 0
	if i := strings.Index(raw[:end], "://"); i >= 0 {
		offset = i + 3
	}
	if i := strings.Index(raw[offset:end], "//"); i >= 0 {
		i += offset
		out.Subdir = raw[i+2 : end]
		raw = raw[:i] + raw[end:]
		if !validModuleSubdir(out.Subdir) {
			return out, remoteModuleError()
		}
	}
	target, err := moduleURL(base, raw)
	if err != nil {
		return out, err
	}
	u, _ := url.Parse(target)
	query, err := url.ParseQuery(u.RawQuery)
	if err != nil {
		return out, remoteModuleError()
	}
	for _, key := range []string{"archive", "checksum", "ref", "depth"} {
		if len(query[key]) > 1 {
			return out, remoteModuleError()
		}
	}
	if force == "git" {
		return githubModuleSource(u, out.Subdir, query, c)
	}
	kind := query.Get("archive")
	if kind == "" {
		kind = moduleArchiveType(u.Path)
	}
	kind = canonicalModuleArchive(kind)
	if kind == "" {
		return out, apierror.New(
			502,
			"upstream Terraform module location does not select a qualified archive",
		)
	}
	out.Archive = kind
	if out.Checksums, err = moduleChecksum(query.Get("checksum")); err != nil {
		return out, err
	}
	query.Del("archive")
	query.Del("checksum")
	u.RawQuery = query.Encode()
	out.Target = u.String()
	if err = allowModuleURL(c, out.Target); err != nil {
		return out, err
	}
	// [I] No 7.x source shows where a remote caches an HTTP archive; it is
	// taken to follow the rule a provider's files do, below the remote's
	// URL (remoteFilePath).
	var ok bool
	if out.Path, ok = remoteFilePath(c.URL, out.Target); !ok {
		return out, remoteModuleError()
	}
	return out, nil
}

func githubModuleSource(
	u *url.URL,
	subdir string,
	q url.Values,
	c repo.Config,
) (moduleSource, error) {
	out := moduleSource{Subdir: subdir, Archive: "tar.gz"}
	parts := strings.Split(strings.Trim(u.Path, "/"), "/")
	if u.Scheme != "https" || !strings.EqualFold(u.Host, "github.com") || len(parts) != 2 ||
		parts[0] == "" ||
		parts[1] == "" {
		return out, apierror.New(
			502,
			"upstream Terraform SCM source is not a qualified GitHub HTTPS archive",
		)
	}
	for key := range q {
		if key != "ref" && key != "depth" {
			return out, remoteModuleError()
		}
	}
	if depth := q.Get("depth"); depth != "" {
		n, err := strconv.Atoi(depth)
		if err != nil || n < 1 {
			return out, remoteModuleError()
		}
	}
	ref := q.Get("ref")
	if ref == "" {
		ref = "HEAD"
	}
	if strings.ContainsAny(ref, "\\\x00\r\n") || strings.HasPrefix(ref, "-") {
		return out, remoteModuleError()
	}
	for _, part := range strings.Split(ref, "/") {
		if part == ".." || part == "." || part == "" {
			return out, remoteModuleError()
		}
	}
	repoName := strings.TrimSuffix(parts[1], ".git")
	if repoName == "" {
		return out, remoteModuleError()
	}
	out.Target = "https://github.com/" + url.PathEscape(
		parts[0],
	) + "/" + url.PathEscape(
		repoName,
	) + "/archive/" + url.PathEscape(
		ref,
	) + ".tar.gz"
	out.Subdir = "*"
	if subdir != "" {
		out.Subdir += "/" + subdir
	}
	if err := allowModuleURL(c, out.Target); err != nil {
		return out, err
	}
	// 7.x serves a Git source through its VCS support, which fetches the
	// ref as a tag (RTFACT-30340's trace: moduleGitDownload calls
	// VcsRemoteHandler.downloadTag), and the vcs-default layout keeps a tag
	// at [orgPath]/[module]/tags/[baseRev]/[module]-[baseRev].[ext] (VCS
	// repositories doc). [I] That a Terraform remote keeps that layout, and
	// the extension: 7.x asks GitHub for a zip, this adapter for a tar.gz.
	out.Path = parts[0] + "/" + repoName + "/tags/" + ref + "/" +
		repoName + "-" + ref + "." + out.Archive
	if _, _, _, err := kernel.SplitItemPath(out.Path); err != nil {
		return out, remoteModuleError()
	}
	return out, nil
}

func validModuleSubdir(s string) bool {
	if s == "" || strings.HasPrefix(s, "/") || strings.ContainsAny(s, "\\\x00\r\n%:#?") {
		return false
	}
	for _, seg := range strings.Split(s, "/") {
		if seg == "" || seg == "." || seg == ".." {
			return false
		}
	}
	_, err := path.Match(s, "")
	return err == nil
}

func canonicalModuleArchive(s string) string {
	switch s {
	case "zip", "tar", "tar.gz", "tar.bz2", "tar.xz", "gz", "bz2", "xz":
		return s
	case "tgz":
		return "tar.gz"
	case "tbz2", "tar.tbz2":
		return "tar.bz2"
	case "txz":
		return "tar.xz"
	}
	return ""
}

func moduleArchiveType(p string) string {
	for _, ext := range []string{"tar.gz", "tar.bz2", "tar.tbz2", "tar.xz", "zip", "tar", "tgz", "tbz2", "txz", "gz", "bz2", "xz"} {
		if strings.HasSuffix(p, "."+ext) {
			return ext
		}
	}
	return ""
}

func moduleChecksum(raw string) (kernel.Checksums, error) {
	var out kernel.Checksums
	if raw == "" {
		return out, nil
	}
	algorithm, digest, ok := strings.Cut(raw, ":")
	if !ok {
		switch len(raw) {
		case 32:
			algorithm = "md5"
		case 40:
			algorithm = "sha1"
		case 64:
			algorithm = "sha256"
		case 128:
			algorithm = "sha512"
		}
		digest = raw
	}
	value, err := hex.DecodeString(digest)
	if err != nil {
		return out, remoteModuleError()
	}
	digest = strings.ToLower(digest)
	switch {
	case algorithm == "md5" && len(value) == 16:
		out.MD5 = digest
	case algorithm == "sha1" && len(value) == 20:
		out.SHA1 = digest
	case algorithm == "sha256" && len(value) == 32:
		out.SHA256 = digest
	case algorithm == "sha512" && len(value) == 64:
		out.SHA512 = digest
	default:
		return out, remoteModuleError()
	}
	return out, nil
}

func allowModuleURL(c repo.Config, target string) error {
	u, _ := url.Parse(target)
	for _, raw := range []string{c.URL, c.TerraformRegistryURL} {
		base, err := url.Parse(raw)
		if err != nil {
			continue
		}
		if strings.EqualFold(base.Host, u.Host) && base.Scheme == u.Scheme &&
			(u.Path == strings.TrimRight(base.Path, "/") || strings.HasPrefix(u.Path, strings.TrimRight(base.Path, "/")+"/")) {
			return nil
		}
	}
	// HashiCorp's documented registry source uses the GitHub storage API. Its
	// public archive/CDN locations still pass DNS/redirect transport security.
	configured, _ := url.Parse(c.URL)
	if configured != nil &&
		(configured.Host == "api.github.com" || configured.Host == "github.com") &&
		u.Scheme == "https" &&
		(u.Host == "api.github.com" || u.Host == "github.com") {
		return nil
	}
	if c.ExternalDependenciesEnabled != nil && *c.ExternalDependenciesEnabled {
		for _, pattern := range c.ExternalDependenciesPatterns {
			if pattern != "" && antpath.CompilePattern(pattern).Match(target) {
				return nil
			}
		}
	}
	return apierror.New(
		502,
		"upstream Terraform archive URL is outside the configured origins and allow patterns",
	)
}

// remoteFilePath is the path a remote repository caches the file at target
// under, as 7.x does: below base, the remote's own URL for that kind of
// file, the rest of target's path, and anywhere else
// external/<host>/<path>. JFrog's include pattern examples for a remote
// Terraform repository name a HashiCorp provider's files
// **/terraform-provider-<type>/**, its path below the default Providers
// URL https://releases.hashicorp.com, and another publisher's
// **/external/github.com/<owner>/terraform-provider-<type>/** (Terraform
// repositories doc, "Configure a Remote Terraform Repository"); RTFACT-30802
// roots both at the cache's top. The rest of each path, the host without
// the scheme 7.6x's download URLs carried (RTFACT-29809) and the query
// left out, are [I]. ok is false for a target that names no file.
func remoteFilePath(base, target string) (string, bool) {
	u, err := url.Parse(target)
	if err != nil || u.Path == "" || strings.HasSuffix(u.Path, "/") {
		return "", false
	}
	path := "external/" + strings.ToLower(u.Host) + u.Path
	if b, e := url.Parse(base); e == nil && b.Host != "" && b.Scheme == u.Scheme &&
		strings.EqualFold(b.Host, u.Host) {
		if rest, below := strings.CutPrefix(u.Path, strings.TrimRight(b.Path, "/")+"/"); below {
			path = rest
		}
	}
	if _, _, _, err = kernel.SplitItemPath(path); err != nil || strings.HasPrefix(path, "/") {
		return "", false
	}
	return path, true
}

// listingPath is where the version list checks Read on the archive of m's
// version: its name in the module's own folder, as in a local repository.
// Where 7.x checks a listing is [I], so the list filters as it did before
// the archive moved to its 7.x cache path (Path).
func (s moduleSource) listingPath(m tf.Module) string {
	return m.Package() + "/" + m.Version + "." + s.Archive
}

// Only GitHub's archive service may leave the configured GitHub origins for
// codeload, and only for the repository already selected in the source URL.
// This adds no credential forwarding; the transport retains that decision.
func moduleURLGuard(c repo.Config, initial string) func(context.Context, *url.URL) error {
	selected := githubArchiveRepository(initial)
	configured, _ := url.Parse(c.URL)
	github := configured != nil && configured.Scheme == "https" &&
		(configured.Host == "github.com" || configured.Host == "api.github.com")
	return func(_ context.Context, target *url.URL) error {
		if target == nil {
			return remoteModuleError()
		}
		if _, err := moduleURL(initial, target.String()); err != nil {
			return err
		}
		if err := allowModuleURL(c, target.String()); err == nil {
			return nil
		}
		if github && selected != "" && target.Scheme == "https" &&
			target.Host == "codeload.github.com" {
			for _, kind := range []string{"tar.gz", "legacy.tar.gz", "zip", "legacy.zip"} {
				if strings.HasPrefix(target.Path, "/"+selected+"/"+kind+"/") {
					return nil
				}
			}
		}
		return allowModuleURL(c, target.String())
	}
}

func githubArchiveRepository(raw string) string {
	u, err := url.Parse(raw)
	if err != nil || u.Scheme != "https" {
		return ""
	}
	parts := strings.Split(strings.Trim(u.Path, "/"), "/")
	if u.Host == "github.com" && len(parts) >= 4 && parts[2] == "archive" {
		return strings.Join(parts[:2], "/")
	}
	if u.Host == "api.github.com" && len(parts) >= 4 && parts[0] == "repos" &&
		(parts[3] == "tarball" || parts[3] == "zipball") {
		return strings.Join(parts[1:3], "/")
	}
	return ""
}
