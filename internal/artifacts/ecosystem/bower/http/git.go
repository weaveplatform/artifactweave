package bowerhttp

import (
	"bytes"
	"context"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/http"
	"net/url"
	"slices"
	"strconv"
	"strings"

	"github.com/weaveplatform/artifactweave/internal/access/api"
	"github.com/weaveplatform/artifactweave/internal/artifacts/adapters/http/pkgroute"
	"github.com/weaveplatform/artifactweave/internal/artifacts/api/repo"
	"github.com/weaveplatform/artifactweave/internal/artifacts/core/remote/source"
	bowerfmt "github.com/weaveplatform/artifactweave/internal/artifacts/ecosystem/bower"
	"github.com/weaveplatform/artifactweave/internal/kernel"
	"github.com/weaveplatform/artifactweave/internal/platform/httpkit/apierror"
)

const (
	maxGitCatalogBytes = 16 << 20
	maxGitRefsBytes    = 8 << 20
)

// A Bower registry maps package names to Git URLs. The configured repository
// URL is the operator-approved Git host/prefix; registry pointers cannot add
// another host or path family. Artifactory-to-Artifactory remotes use the
// separate native refs/binaries exchange.
func gitRemote(r *http.Request) bool {
	return pkgroute.RequestFrom(r).Repo.VCSGitProvider != repo.ArtifactoryVCS
}

type gitPointer struct {
	name, owner, project string
	url                  *url.URL
}

func configuredURL(raw string) (*url.URL, error) {
	u, err := url.Parse(raw)
	if err != nil || u == nil || (u.Scheme != "https" && u.Scheme != "http") ||
		u.Host == "" || u.User != nil || u.RawQuery != "" || u.Fragment != "" {
		return nil, source.ErrUnsupported
	}
	return u, nil
}

func sameOrigin(a, b *url.URL) bool {
	return a.Scheme == b.Scheme && strings.EqualFold(a.Host, b.Host)
}

func registryTarget(c repo.Config, suffix string) (*url.URL, error) {
	u, err := configuredURL(c.BowerRegistryURL)
	if err != nil {
		return nil, err
	}
	u.Path = strings.TrimSuffix(u.Path, "/") + "/packages" + suffix
	u.RawPath = ""
	return u, nil
}

func exactTarget(target *url.URL) func(context.Context, *url.URL) error {
	return func(_ context.Context, u *url.URL) error {
		if !sameOrigin(target, u) || target.EscapedPath() != u.EscapedPath() ||
			target.RawQuery != u.RawQuery || u.Fragment != "" || u.User != nil {
			return source.ErrDenied
		}
		return nil
	}
}

func gitPointerURL(c repo.Config, name, raw string) (gitPointer, error) {
	var out gitPointer
	if !bowerfmt.ValidName(name) {
		return out, remoteInvalid()
	}
	base, err := configuredURL(c.URL)
	if err != nil {
		return out, err
	}
	if strings.HasPrefix(raw, "ssh://") {
		return stashPointer(c, name, raw)
	}
	u, err := configuredURL(raw)
	if err != nil {
		return out, err
	}
	if !sameOrigin(base, u) || base.Scheme == "https" && u.Scheme != "https" {
		return out, source.ErrDenied
	}
	prefix := strings.TrimSuffix(base.Path, "/") + "/"
	if c.VCSGitProvider == repo.Stash || c.VCSGitProvider == repo.OldStash {
		prefix += "scm/"
	}
	if !strings.HasPrefix(u.Path, prefix) {
		return out, source.ErrDenied
	}
	rel := strings.TrimPrefix(strings.TrimSuffix(u.Path, ".git"), prefix)
	parts := strings.Split(rel, "/")
	if len(parts) != 2 || !bowerfmt.ValidName(parts[0]) || !bowerfmt.ValidName(parts[1]) ||
		!strings.HasSuffix(u.Path, ".git") || strings.Contains(u.EscapedPath(), "%") {
		return out, remoteInvalid()
	}
	return gitPointer{name: name, owner: parts[0], project: parts[1], url: u}, nil
}

func decodePointer(data []byte, c repo.Config, name string) (gitPointer, error) {
	var entry packageEntry
	if len(data) == 0 || len(data) > maxRefsBytes || json.Unmarshal(data, &entry) != nil ||
		entry.Name != name {
		return gitPointer{}, remoteInvalid()
	}
	return gitPointerURL(c, name, entry.URL)
}

func (h handler) gitPackage(r *http.Request, name string) (gitPointer, error) {
	c := pkgroute.RequestFrom(r).Repo
	if c.AllowAnyHostAuth != nil && *c.AllowAnyHostAuth {
		return gitPointer{}, source.ErrUnsupported
	}
	registered, err := h.k.DB().BowerRegistrations().Get(r.Context(), c.Key, name)
	if err == nil {
		return gitPointerURL(c, name, registered.URL)
	}
	if !errors.Is(err, kernel.ErrNotFound) {
		return gitPointer{}, fmt.Errorf("bower registration: %w", err)
	}
	if c.VCSGitProvider == repo.Stash || c.VCSGitProvider == repo.OldStash {
		return gitPointer{}, kernel.ErrNotFound
	}
	target, err := registryTarget(c, "/"+url.PathEscape(name))
	if err != nil {
		return gitPointer{}, err
	}
	resource := lookupPolicy(r, source.Resource{
		Kind:             source.ResourceKindMetadata,
		Path:             "catalog/" + name + "/pointer.json",
		ExternalURL:      target.String(),
		CheckURL:         exactTarget(target),
		Method:           http.MethodGet,
		PermissionFolder: true,
		PermissionPath:   name,
		Variant:          "bower-git-pointer-v1",
		ValidateMetadata: func(_ context.Context, body io.ReadSeeker, size int64, _ source.MetadataResponse) error {
			if size <= 0 || size > maxRefsBytes {
				return remoteInvalid()
			}
			data, readErr := io.ReadAll(io.LimitReader(body, maxRefsBytes+1))
			if readErr != nil || int64(len(data)) != size {
				return remoteInvalid()
			}
			_, parseErr := decodePointer(data, c, name)
			return parseErr
		},
	})
	result, err := h.k.Read(r, resource)
	if err != nil {
		return gitPointer{}, fmt.Errorf("bower git registry pointer: %w", err)
	}
	if result.Stream == nil {
		return gitPointer{}, remoteInvalid()
	}
	defer result.Stream.Body.Close()
	data, err := pkgroute.ReadServed(r.Context(), result.Stream.Body, maxRefsBytes, remoteInvalid())
	if err != nil {
		return gitPointer{}, err
	}
	if result.Revalidate != nil {
		if err := result.Revalidate(r.Context()); err != nil {
			return gitPointer{}, fmt.Errorf("bower git pointer authority: %w", err)
		}
	}
	return decodePointer(data, c, name)
}

// Git smart HTTP v1 discovery is packet-lined. We do not send the v2 request
// header; the resulting refs include the tag SHA and may include peeled tags.
func parseGitRefs(data []byte) ([]remoteRef, error) {
	if len(data) == 0 || len(data) > maxGitRefsBytes {
		return nil, remoteInvalid()
	}
	var lines [][]byte
	packets := 0
	for len(data) > 0 {
		packets++
		if packets > 65536 {
			return nil, remoteInvalid()
		}
		if len(data) < 4 {
			return nil, remoteInvalid()
		}
		number, err := strconv.ParseUint(string(data[:4]), 16, 16)
		if err != nil {
			return nil, remoteInvalid()
		}
		n := int(number)
		data = data[4:]
		if n == 0 || n == 1 || n == 2 {
			continue
		}
		if n < 4 || n-4 > len(data) {
			return nil, remoteInvalid()
		}
		lines = append(lines, data[:n-4])
		data = data[n-4:]
	}
	seen := map[string]int{}
	var refs []remoteRef
	for _, raw := range lines {
		line := strings.TrimSpace(string(bytes.SplitN(raw, []byte{0}, 2)[0]))
		if strings.HasPrefix(line, "# service=") || line == "" {
			continue
		}
		parts := strings.SplitN(line, " ", 2)
		if len(parts) != 2 || len(parts[0]) != 40 {
			return nil, remoteInvalid()
		}
		if _, err := hex.DecodeString(parts[0]); err != nil {
			return nil, remoteInvalid()
		}
		if !strings.HasPrefix(parts[1], "refs/tags/") || strings.HasSuffix(parts[1], "^{}") {
			continue
		}
		tag := strings.TrimPrefix(parts[1], "refs/tags/")
		version := strings.TrimPrefix(tag, "v")
		if !bowerfmt.ValidVersion(version) {
			continue
		}
		if index, ok := seen[version]; ok {
			// Some Git repositories publish both 1.2.3 and v1.2.3.
			// Present one stable version to the resolver and use its v tag.
			if strings.HasPrefix(tag, "v") && !strings.HasPrefix(refs[index].Tag, "v") {
				refs[index].Tag = tag
			}
			continue
		}
		seen[version] = len(refs)
		refs = append(refs, remoteRef{Version: version, Tag: tag})
		if len(refs) > 4096 {
			return nil, remoteInvalid()
		}
	}
	return refs, nil
}

func (h handler) gitRefs(r *http.Request, name string) ([]remoteRef, source.Result, error) {
	pointer, err := h.gitPackage(r, name)
	if err != nil {
		return nil, source.Result{}, err
	}
	return h.gitRefsForPointer(r, name, pointer, nil)
}

func (h handler) gitRefsForPointer(
	r *http.Request,
	name string,
	pointer gitPointer,
	checkURL func(context.Context, *url.URL) error,
) ([]remoteRef, source.Result, error) {
	target := *pointer.url
	target.Path += "/info/refs"
	target.RawQuery = "service=git-upload-pack"
	guard := exactTarget(&target)
	if checkURL != nil {
		// Check before Read as well: cached metadata must obey the current
		// virtual URL allowlist, although it needs no transport request.
		if err := checkURL(r.Context(), &target); err != nil {
			return nil, source.Result{}, err
		}
		exact := guard
		guard = func(ctx context.Context, u *url.URL) error {
			if err := exact(ctx, u); err != nil {
				return err
			}
			return checkURL(ctx, u)
		}
	}
	resource := lookupPolicy(r, source.Resource{
		Kind:             source.ResourceKindMetadata,
		Path:             "catalog/" + name + "/git-refs",
		ExternalURL:      target.String(),
		CheckURL:         guard,
		Method:           http.MethodGet,
		PermissionFolder: true,
		PermissionPath:   name,
		Variant:          "bower-git-smart-refs-v1",
		ValidateMetadata: func(_ context.Context, body io.ReadSeeker, size int64, _ source.MetadataResponse) error {
			if size <= 0 || size > maxGitRefsBytes {
				return remoteInvalid()
			}
			data, readErr := io.ReadAll(io.LimitReader(body, maxGitRefsBytes+1))
			if readErr != nil || int64(len(data)) != size {
				return remoteInvalid()
			}
			_, parseErr := parseGitRefs(data)
			return parseErr
		},
	})
	result, err := h.k.Read(r, resource)
	if err != nil {
		return nil, source.Result{}, fmt.Errorf("bower git refs: %w", err)
	}
	if result.Stream == nil {
		return nil, source.Result{}, remoteInvalid()
	}
	defer result.Stream.Body.Close()
	data, err := pkgroute.ReadServed(
		r.Context(),
		result.Stream.Body,
		maxGitRefsBytes,
		remoteInvalid(),
	)
	if err != nil {
		return nil, source.Result{}, err
	}
	refs, err := parseGitRefs(data)
	return refs, result, err
}

func gitArchiveURL(
	c repo.Config,
	p gitPointer,
	tag string,
) (*url.URL, func(context.Context, *url.URL) error, error) {
	if !validGitTarget(tag) {
		return nil, nil, remoteInvalid()
	}
	refKind := "tags"
	if !bowerfmt.ValidVersion(strings.TrimPrefix(tag, "v")) {
		refKind = "heads"
	}
	if c.VCSGitDownloadURL == "" &&
		(c.VCSGitProvider == repo.Stash || c.VCSGitProvider == repo.OldStash) {
		target, err := configuredURL(c.URL)
		if err != nil {
			return nil, nil, err
		}
		target.Path = strings.TrimSuffix(
			target.Path,
			"/",
		) + "/rest/api/1.0/projects/" + p.owner + "/repos/" + p.project + "/archive"
		if c.VCSGitProvider == repo.OldStash {
			target.Path = strings.TrimSuffix(
				target.Path,
				"/rest/api/1.0/projects/"+p.owner+"/repos/"+p.project+"/archive",
			) + "/plugins/servlet/archive/projects/" + p.owner + "/repos/" + p.project
		}
		target.RawQuery = url.Values{
			"at":     {"refs/" + refKind + "/" + tag},
			"format": {"tar.gz"},
			"prefix": {p.project + "-" + tag + "/"},
		}.Encode()
		if c.VCSGitProvider == repo.OldStash {
			target.RawQuery = url.Values{
				"at":     {"refs/" + refKind + "/" + tag},
				"format": {"tar.gz"},
			}.Encode()
		}
		return target, exactTarget(target), nil
	}
	var raw string
	if c.VCSGitDownloadURL != "" {
		raw = strings.NewReplacer("{0}", p.owner, "{1}", p.project, "{2}", tag, "{3}", "tar.gz").
			Replace(c.VCSGitDownloadURL)
		if strings.ContainsAny(raw, "{}") {
			return nil, nil, source.ErrUnsupported
		}
	} else {
		switch c.VCSGitProvider {
		case "", repo.GitHub, repo.GitHubEnterprise:
			raw = strings.TrimSuffix(
				p.url.String(),
				".git",
			) + "/archive/refs/" + refKind + "/" + url.PathEscape(
				tag,
			) + ".tar.gz"
		case repo.Bitbucket:
			raw = strings.TrimSuffix(
				p.url.String(),
				".git",
			) + "/get/" + url.PathEscape(
				tag,
			) + ".tar.gz"
		default:
			return nil, nil, source.ErrUnsupported
		}
	}
	target, err := configuredURL(raw)
	if err != nil {
		return nil, nil, err
	}
	if !sameOrigin(target, p.url) && c.VCSGitDownloadURL == "" {
		return nil, nil, source.ErrDenied
	}
	guard := exactTarget(target)
	return target, func(ctx context.Context, u *url.URL) error {
		if guard(ctx, u) == nil {
			return nil
		}
		if p.url.Scheme == "https" && p.url.Host == "github.com" &&
			u.Scheme == "https" && u.Host == "codeload.github.com" &&
			u.Path == "/"+p.owner+"/"+p.project+"/tar.gz/refs/"+refKind+"/"+tag &&
			u.RawQuery == "" && u.User == nil {
			return nil
		}
		return source.ErrDenied
	}, nil
}

func (h handler) gitBinary(w http.ResponseWriter, r *http.Request, name, version string) {
	pointer, err := h.gitPackage(r, name)
	if err != nil {
		h.k.Error(w, r, pkgroute.SourceError(err, false))
		return
	}
	refs, _, err := h.gitRefsForPointer(r, name, pointer, nil)
	if err != nil {
		h.k.Error(w, r, pkgroute.SourceError(err, false))
		return
	}
	tag := ""
	for _, ref := range refs {
		if ref.Version == version {
			tag = ref.Tag
			break
		}
	}
	if tag == "" {
		h.k.Error(w, r, apierror.New(http.StatusNotFound, "Bower version not found"))
		return
	}
	target, guard, err := gitArchiveURL(pkgroute.RequestFrom(r).Repo, pointer, tag)
	if err != nil {
		h.k.Error(w, r, pkgroute.SourceError(err, false))
		return
	}
	resource := source.Resource{
		Kind:        source.ResourceKindArtifact,
		Path:        remoteArchivePath(name, version),
		ExternalURL: target.String(),
		CheckURL:    guard,
		Method:      r.Method,
		Variant:     "bower-git-archive-v1",
		Validate:    validateArchive(name, version, true),
	}
	if c := pkgroute.RequestFrom(
		r,
	).Repo; c.VCSGitProvider == repo.Stash ||
		c.VCSGitProvider == repo.OldStash {
		resource.Variant = "bower-stash-archive-v1"
		resource.Validate = func(_ context.Context, body io.ReadSeeker, size int64) error {
			if size <= 0 || size > maxArchiveBytes {
				return remoteInvalid()
			}
			data, err := io.ReadAll(io.LimitReader(body, maxArchiveBytes+1))
			if err != nil || int64(len(data)) != size {
				return remoteInvalid()
			}
			files, err := dependencyFiles(data, name)
			if err != nil {
				return err
			}
			m, err := bowerfmt.DecodeGitManifest(files["bower.json"])
			if err != nil || m.Version != "" && m.Version != version {
				return remoteInvalid()
			}
			return nil
		}
	}
	result, err := h.k.Read(r, resource)
	if err != nil {
		h.k.Error(w, r, pkgroute.SourceError(err, false))
		return
	}
	h.serveSourceResult(w, r, result)
}

func (h handler) gitPackageNames(r *http.Request, query string) ([]string, error) {
	c := pkgroute.RequestFrom(r).Repo
	if c.AllowAnyHostAuth != nil && *c.AllowAnyHostAuth {
		return nil, source.ErrUnsupported
	}
	registered, err := h.registrationNames(r, query)
	if err != nil {
		return nil, err
	}
	if c.VCSGitProvider == repo.Stash || c.VCSGitProvider == repo.OldStash {
		return registered, nil
	}
	suffix := ""
	path := "catalog/packages.json"
	if query != "" {
		suffix = "/search/" + url.PathEscape(query)
		path = "catalog/search/" + query + ".json"
	}
	target, err := registryTarget(c, suffix)
	if err != nil {
		return nil, fmt.Errorf("bower git registry catalog: %w", err)
	}
	resource := source.Resource{
		Kind:             source.ResourceKindMetadata,
		Path:             path,
		ExternalURL:      target.String(),
		CheckURL:         exactTarget(target),
		Method:           http.MethodGet,
		PermissionFolder: true,
		PermissionPath:   "",
		Variant:          "bower-git-catalog-v1",
		ValidateMetadata: func(_ context.Context, body io.ReadSeeker, size int64, _ source.MetadataResponse) error {
			if size < 0 || size > maxGitCatalogBytes {
				return remoteInvalid()
			}
			data, readErr := io.ReadAll(io.LimitReader(body, maxGitCatalogBytes+1))
			if readErr != nil || int64(len(data)) != size {
				return remoteInvalid()
			}
			_, parseErr := parseGitCatalog(c, data)
			return parseErr
		},
	}
	result, err := h.k.Read(r, resource)
	if errors.Is(err, kernel.ErrNotFound) {
		return registered, nil
	}
	if err != nil {
		return nil, fmt.Errorf("bower git catalog fetch: %w", err)
	}
	if result.Stream == nil {
		return nil, remoteInvalid()
	}
	defer result.Stream.Body.Close()
	data, err := pkgroute.ReadServed(
		r.Context(),
		result.Stream.Body,
		maxGitCatalogBytes,
		remoteInvalid(),
	)
	if err != nil {
		return nil, err
	}
	names, err := parseGitCatalog(c, data)
	if err != nil {
		return nil, err
	}
	visible := names[:0]
	for _, name := range names {
		if !h.k.CanFolder(r, name, api.ActionRead) {
			continue
		}
		if result.CheckFolder != nil {
			// A package whose folder the patterns reject stays listed, and
			// only its archive is refused (R3): CheckFolder checks the
			// policy after Read permission, so its refusal keeps the name.
			if err := result.CheckFolder(r.Context(), name); errors.Is(err, source.ErrDenied) {
				continue
			} else if err != nil && !pkgroute.IsRefusal(err) {
				return nil, fmt.Errorf("bower git catalog authority: %w", err)
			}
		}
		visible = append(visible, name)
	}
	if result.Revalidate != nil {
		if err := result.Revalidate(r.Context()); err != nil {
			return nil, fmt.Errorf("bower git catalog revalidate: %w", err)
		}
	}
	visible = append(visible, registered...)
	slices.Sort(visible)
	return slices.Compact(visible), nil
}

func parseGitCatalog(c repo.Config, data []byte) ([]string, error) {
	var entries []packageEntry
	if len(data) > maxGitCatalogBytes || json.Unmarshal(data, &entries) != nil ||
		len(entries) > 100000 {
		return nil, remoteInvalid()
	}
	seen := map[string]bool{}
	var out []string
	for _, e := range entries {
		if !bowerfmt.ValidName(e.Name) || seen[e.Name] {
			return nil, remoteInvalid()
		}
		seen[e.Name] = true
		if _, err := gitPointerURL(c, e.Name, e.URL); err == nil {
			out = append(out, e.Name)
		}
	}
	slices.Sort(out)
	return out, nil
}
