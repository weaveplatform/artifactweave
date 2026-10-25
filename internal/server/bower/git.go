package bower

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

	bowerfmt "github.com/weaveplatform/artifactweave/internal/format/bower"
	"github.com/weaveplatform/artifactweave/internal/server/apierror"
	"github.com/weaveplatform/artifactweave/internal/server/pkgroute"
	"github.com/weaveplatform/artifactweave/internal/source"
	"github.com/weaveplatform/artifactweave/pkg/repo"
	"github.com/weaveplatform/artifactweave/pkg/store"
	"github.com/weaveplatform/artifactweave/pkg/types"
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
	u, err := configuredURL(raw)
	if err != nil {
		return out, err // SSH/git transports need an explicit, audited transport.
	}
	if !sameOrigin(base, u) || base.Scheme == "https" && u.Scheme != "https" {
		return out, source.ErrDenied
	}
	prefix := strings.TrimSuffix(base.Path, "/") + "/"
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
	target, err := registryTarget(c, "/"+url.PathEscape(name))
	if err != nil {
		return gitPointer{}, err
	}
	resource := source.Resource{
		Kind:             source.Metadata,
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
	}
	result, err := h.k.Read(r, resource)
	if err != nil {
		return gitPointer{}, fmt.Errorf("bower Git registry pointer: %w", err)
	}
	if result.Stream == nil {
		return gitPointer{}, remoteInvalid()
	}
	defer result.Stream.Body.Close()
	data, err := io.ReadAll(io.LimitReader(result.Stream.Body, maxRefsBytes+1))
	if err != nil {
		return gitPointer{}, remoteInvalid()
	}
	if result.Revalidate != nil {
		if err := result.Revalidate(r.Context()); err != nil {
			return gitPointer{}, fmt.Errorf("bower Git pointer authority: %w", err)
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
	return h.gitRefsForPointer(r, name, pointer)
}

func (h handler) gitRefsForPointer(
	r *http.Request,
	name string,
	pointer gitPointer,
) ([]remoteRef, source.Result, error) {
	target := *pointer.url
	target.Path += "/info/refs"
	target.RawQuery = "service=git-upload-pack"
	resource := source.Resource{
		Kind:             source.Metadata,
		Path:             "catalog/" + name + "/git-refs",
		ExternalURL:      target.String(),
		CheckURL:         exactTarget(&target),
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
	}
	result, err := h.k.Read(r, resource)
	if err != nil {
		return nil, source.Result{}, fmt.Errorf("bower Git refs: %w", err)
	}
	if result.Stream == nil {
		return nil, source.Result{}, remoteInvalid()
	}
	defer result.Stream.Body.Close()
	data, err := io.ReadAll(io.LimitReader(result.Stream.Body, maxGitRefsBytes+1))
	if err != nil {
		return nil, source.Result{}, remoteInvalid()
	}
	refs, err := parseGitRefs(data)
	return refs, result, err
}

func gitArchiveURL(
	c repo.Config,
	p gitPointer,
	tag string,
) (*url.URL, func(context.Context, *url.URL) error, error) {
	if !bowerfmt.ValidVersion(strings.TrimPrefix(tag, "v")) {
		return nil, nil, remoteInvalid()
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
			) + "/archive/refs/tags/" + url.PathEscape(
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
			u.Path == "/"+p.owner+"/"+p.project+"/tar.gz/refs/tags/"+tag &&
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
	refs, _, err := h.gitRefsForPointer(r, name, pointer)
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
		Kind:        source.Artifact,
		Path:        name + "/" + version + "/bower-archive.tar.gz",
		ExternalURL: target.String(),
		CheckURL:    guard,
		Method:      r.Method,
		Variant:     "bower-git-archive-v1",
		Validate:    validateArchive(name, version, true),
	}
	result, err := h.k.Read(r, resource)
	if err != nil {
		h.k.Error(w, r, pkgroute.SourceError(err, false))
		return
	}
	h.k.ServeResult(w, r, result)
}

func (h handler) gitPackageNames(r *http.Request, query string) ([]string, error) {
	c := pkgroute.RequestFrom(r).Repo
	if c.AllowAnyHostAuth != nil && *c.AllowAnyHostAuth {
		return nil, source.ErrUnsupported
	}
	suffix := ""
	path := "catalog/packages.json"
	if query != "" {
		suffix = "/search/" + url.PathEscape(query)
		path = "catalog/search/" + query + ".json"
	}
	target, err := registryTarget(c, suffix)
	if err != nil {
		return nil, fmt.Errorf("bower Git registry catalog: %w", err)
	}
	resource := source.Resource{
		Kind:             source.Metadata,
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
	if errors.Is(err, store.ErrNotFound) {
		return nil, nil
	}
	if err != nil {
		return nil, fmt.Errorf("bower Git catalog fetch: %w", err)
	}
	if result.Stream == nil {
		return nil, remoteInvalid()
	}
	defer result.Stream.Body.Close()
	data, err := io.ReadAll(io.LimitReader(result.Stream.Body, maxGitCatalogBytes+1))
	if err != nil {
		return nil, remoteInvalid()
	}
	names, err := parseGitCatalog(c, data)
	if err != nil {
		return nil, err
	}
	visible := names[:0]
	for _, name := range names {
		if !h.k.CanFolder(r, name, types.ActionRead) {
			continue
		}
		if result.CheckFolder != nil {
			if err := result.CheckFolder(r.Context(), name); errors.Is(err, source.ErrDenied) {
				continue
			} else if err != nil {
				return nil, fmt.Errorf("bower Git catalog authority: %w", err)
			}
		}
		visible = append(visible, name)
	}
	if result.Revalidate != nil {
		if err := result.Revalidate(r.Context()); err != nil {
			return nil, fmt.Errorf("bower Git catalog revalidate: %w", err)
		}
	}
	return visible, nil
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
