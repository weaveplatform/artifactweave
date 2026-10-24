package composer

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/http"
	"net/url"
	"regexp"
	"strings"

	"github.com/weaveplatform/artifactweave/internal/format/composer"
	"github.com/weaveplatform/artifactweave/internal/server/apierror"
	"github.com/weaveplatform/artifactweave/internal/server/pkgroute"
	"github.com/weaveplatform/artifactweave/internal/source"
	"github.com/weaveplatform/artifactweave/pkg/store"
	"github.com/weaveplatform/artifactweave/pkg/types"
)

const (
	remoteMetadataLimit = 16 << 20
	remoteVersionLimit  = 4096
)

var sha1RE = regexp.MustCompile(`^[0-9a-f]{40}$`)

func remoteInvalid() error {
	return apierror.New(http.StatusBadGateway, "invalid upstream Composer metadata")
}

func registryURL(cURL, registry string) string {
	if registry != "" {
		return strings.TrimRight(registry, "/")
	}
	return strings.TrimRight(cURL, "/")
}

func readMetadata(result source.Result) ([]byte, error) {
	if result.Stream == nil {
		return nil, remoteInvalid()
	}
	defer result.Stream.Body.Close()
	data, err := io.ReadAll(io.LimitReader(result.Stream.Body, remoteMetadataLimit+1))
	if err != nil || len(data) > remoteMetadataLimit {
		return nil, remoteInvalid()
	}
	return data, nil
}

func parseRoot(data []byte, base string) (string, error) {
	var doc map[string]json.RawMessage
	if json.Unmarshal(data, &doc) != nil || doc == nil {
		return "", remoteInvalid()
	}
	var tmpl string
	if json.Unmarshal(doc["metadata-url"], &tmpl) != nil || strings.Count(tmpl, "%package%") != 1 ||
		len(tmpl) > 2048 {
		return "", remoteInvalid()
	}
	u, err := url.Parse(strings.Replace(tmpl, "%package%", "composer/placeholder", 1))
	if err != nil {
		return "", remoteInvalid()
	}
	b, err := url.Parse(base)
	if err != nil || b.Scheme == "" || b.Host == "" {
		return "", remoteInvalid()
	}
	resolved := b.ResolveReference(u)
	if resolved.Scheme != b.Scheme || resolved.Host != b.Host || resolved.User != nil ||
		resolved.RawQuery != "" ||
		resolved.Fragment != "" {
		return "", remoteInvalid()
	}
	return strings.Replace(resolved.String(), "composer/placeholder", "%package%", 1), nil
}

func parseP2Doc(data []byte, name, base string) ([]release, error) {
	var doc struct {
		Packages map[string][]json.RawMessage `json:"packages"`
	}
	if json.Unmarshal(data, &doc) != nil || len(doc.Packages) != 1 ||
		len(doc.Packages[name]) > remoteVersionLimit {
		return nil, remoteInvalid()
	}
	entries, ok := doc.Packages[name]
	if !ok {
		return nil, remoteInvalid()
	}
	out := make([]release, 0, len(entries))
	seen := map[string]bool{}
	for _, entry := range entries {
		var fields map[string]json.RawMessage
		if json.Unmarshal(entry, &fields) != nil || fields == nil {
			return nil, remoteInvalid()
		}
		var packageName, version string
		if json.Unmarshal(fields["name"], &packageName) != nil || packageName != name ||
			json.Unmarshal(
				fields["version"],
				&version,
			) != nil || !composer.ValidVersion(version) || seen[version] {
			return nil, remoteInvalid()
		}
		seen[version] = true
		var dist struct {
			Type   string `json:"type"`
			URL    string `json:"url"`
			Shasum string `json:"shasum"`
		}
		if json.Unmarshal(fields["dist"], &dist) != nil || dist.Type != "zip" || dist.URL == "" ||
			(dist.Shasum != "" && !sha1RE.MatchString(dist.Shasum)) {
			return nil, remoteInvalid()
		}
		selected, err := resolveDist(base, dist.URL)
		if err != nil {
			return nil, err
		}
		var public map[string]any
		if json.Unmarshal(entry, &public) != nil {
			return nil, remoteInvalid()
		}
		delete(public, "source")
		out = append(
			out,
			release{
				Name:      name,
				Version:   version,
				Document:  public,
				RemoteURL: selected,
				SHA1:      dist.Shasum,
			},
		)
	}
	return out, nil
}

func resolveDist(base, href string) (string, error) {
	b, err := url.Parse(base)
	if err != nil {
		return "", remoteInvalid()
	}
	u, err := url.Parse(href)
	if err != nil {
		return "", remoteInvalid()
	}
	u = b.ResolveReference(u)
	if (u.Scheme != "https" && u.Scheme != "http") || u.Host == "" || u.User != nil ||
		u.Fragment != "" ||
		u.RawQuery != "" {
		return "", remoteInvalid()
	}
	return u.String(), nil
}

func (h handler) remoteRoot(r *http.Request) (source.Result, string, error) {
	q := pkgroute.RequestFrom(r)
	registry := registryURL(q.Repo.URL, q.Repo.ComposerRegistryURL)
	if registry == "" {
		return source.Result{}, "", source.ErrUnavailable
	}
	resource := source.Resource{
		Kind:             source.Metadata,
		Path:             "packages.json",
		ExternalURL:      registry + "/packages.json",
		Method:           http.MethodGet,
		PermissionFolder: true,
		Variant:          "composer-root-v1",
		ValidateMetadata: func(_ context.Context, body io.ReadSeeker, _ int64, meta source.MetadataResponse) error {
			data, err := io.ReadAll(io.LimitReader(body, remoteMetadataLimit+1))
			if err != nil || len(data) > remoteMetadataLimit {
				return remoteInvalid()
			}
			_, err = parseRoot(data, meta.BaseURL)
			return err
		},
	}
	result, err := h.k.Read(r, resource)
	if err != nil {
		return source.Result{}, "", fmt.Errorf("read composer root metadata: %w", err)
	}
	data, err := readMetadata(result)
	if err != nil {
		return source.Result{}, "", err
	}
	template, err := parseRoot(data, result.Metadata.BaseURL)
	if err != nil {
		return source.Result{}, "", err
	}
	if result.Revalidate != nil {
		if err = result.Revalidate(r.Context()); err != nil {
			return source.Result{}, "", fmt.Errorf("revalidate composer root metadata: %w", err)
		}
	}
	return result, template, nil
}

func (h handler) remoteP2(r *http.Request, name string) ([]release, error) {
	_, template, err := h.remoteRoot(r)
	if err != nil {
		return nil, err
	}
	var all []release
	for _, dev := range []bool{false, true} {
		suffix := ""
		if dev {
			suffix = "~dev"
		}
		path := "p2/" + name + suffix + ".json"
		target := strings.Replace(template, "%package%", name+suffix, 1)
		resource := source.Resource{
			Kind:             source.Metadata,
			Path:             path,
			ExternalURL:      target,
			Method:           http.MethodGet,
			PermissionFolder: true,
			PermissionPath:   name,
			Variant:          "composer-p2-v1",
			ValidateMetadata: func(_ context.Context, body io.ReadSeeker, _ int64, meta source.MetadataResponse) error {
				data, e := io.ReadAll(io.LimitReader(body, remoteMetadataLimit+1))
				if e != nil || len(data) > remoteMetadataLimit {
					return remoteInvalid()
				}
				_, e = parseP2Doc(data, name, meta.BaseURL)
				return e
			},
		}
		result, e := h.k.Read(r, resource)
		var api *apierror.Error
		if errors.Is(e, store.ErrNotFound) ||
			errors.As(e, &api) && api.Status == http.StatusNotFound {
			continue
		}
		if e != nil {
			return nil, fmt.Errorf("read composer package metadata %s: %w", name, e)
		}
		data, e := readMetadata(result)
		if e != nil {
			return nil, e
		}
		releases, e := parseP2Doc(data, name, result.Metadata.BaseURL)
		if e != nil {
			return nil, e
		}
		for i := range releases {
			rel := &releases[i]
			rel.Path = name + "/" + rel.Version + ".zip"
			if !h.k.Can(r, rel.Path, types.ActionRead) {
				continue
			}
			if result.CheckRead != nil {
				if e := result.CheckRead(r.Context(), rel.Path); e != nil {
					if errors.Is(e, source.ErrDenied) {
						continue
					}
					return nil, fmt.Errorf(
						"check composer distribution permission %s: %w",
						rel.Path,
						e,
					)
				}
			}
			rel.Document["dist"] = distDocument(r, name, rel.Version, rel.SHA1)
			rel.revalidate = result.Revalidate
			all = append(all, *rel)
		}
	}
	return all, nil
}

func (h handler) remoteDist(w http.ResponseWriter, r *http.Request, rel release) {
	if rel.RemoteURL == "" {
		h.k.Error(w, r, remoteInvalid())
		return
	}
	resource := source.Resource{
		Kind: source.Artifact, Path: rel.Path, ExternalURL: rel.RemoteURL, Method: http.MethodGet,
		Variant: "composer-zip-v1", Checksums: types.Checksums{SHA1: rel.SHA1},
		Validate: func(ctx context.Context, body io.ReadSeeker, size int64) error {
			pkg, err := composer.ParseArchive(ctx, body, size, rel.Version)
			if err != nil || pkg.Name != rel.Name {
				return source.ErrInvalidResult
			}
			return nil
		},
	}
	result, err := h.k.Read(r, resource)
	if err != nil {
		h.k.Error(w, r, err)
		return
	}
	h.k.ServeResult(w, r, result)
}
