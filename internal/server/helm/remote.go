package helm

import (
	"bytes"
	"context"
	"crypto/sha256"
	"fmt"
	"io"
	"net/http"
	"net/url"
	"strings"

	"gopkg.in/yaml.v3"

	helmfmt "github.com/weaveplatform/artifactweave/internal/format/helm"
	"github.com/weaveplatform/artifactweave/internal/server/apierror"
	"github.com/weaveplatform/artifactweave/internal/server/baseurl"
	"github.com/weaveplatform/artifactweave/internal/server/pkgroute"
	"github.com/weaveplatform/artifactweave/internal/source"
	"github.com/weaveplatform/artifactweave/pkg/types"
)

func (h handler) upstreamIndex(r *http.Request) (remoteIndex, source.Result, error) {
	c := pkgroute.RequestFrom(r).Repo
	resource := source.Resource{
		Kind:   source.Metadata,
		Path:   helmfmt.IndexPath,
		Method: http.MethodGet,
		Accept: "application/yaml, application/x-yaml, text/yaml",
		ExpectedContentTypes: []string{
			"application/yaml",
			"application/x-yaml",
			"text/yaml",
			"text/plain",
			"application/octet-stream",
		},
		PermissionFolder: true,
		Variant:          "helm-index-v2",
		ValidateMetadata: validateRemoteIndex(c),
	}
	result, err := h.k.Read(r, resource)
	if err != nil {
		return remoteIndex{}, result, fmt.Errorf("helm index: %w", err)
	}
	if result.Stream == nil {
		return remoteIndex{}, result, remoteError()
	}
	defer result.Stream.Body.Close()
	index, err := decodeRemoteIndex(c, result.Metadata.BaseURL, result.Stream.Body)
	return index, result, err
}

func (h handler) remoteRead(w http.ResponseWriter, r *http.Request, requested string) {
	if requested == helmfmt.IndexPath {
		h.remoteIndex(w, r)
		return
	}
	owner, provenance := strings.CutSuffix(requested, ".prov")
	if !helmfmt.IsChartPath(owner) {
		h.k.Error(w, r, apierror.New(http.StatusNotFound, "Could not find resource"))
		return
	}
	if !h.k.Require(w, r, requested, types.ActionRead) ||
		(provenance && !h.k.Require(w, r, owner, types.ActionRead)) {
		return
	}
	index, metadata, err := h.upstreamIndex(r)
	if err != nil {
		h.k.Error(w, r, err)
		return
	}
	for _, versions := range index.versions {
		for _, version := range versions {
			for _, target := range version.targets {
				if target.path != owner || r.URL.RawQuery != "" {
					continue
				}
				if metadata.CheckRead != nil {
					if err = metadata.CheckRead(r.Context(), owner); err != nil {
						h.k.Error(w, r, pkgroute.SourceError(err, false))
						return
					}
				}
				resource := pkgroute.Resource(r, requested, "")
				resource.Query, resource.Accept = "", ""
				resource.Variant = "helm-chart-v2:" + version.chart.Name + ":" + version.chart.Version
				resource.Checksums.SHA256 = version.chart.Digest
				resource.Validate = validateRemoteChart(version.chart)
				upstream := *target.url
				if provenance {
					upstream.Path += ".prov"
					upstream.RawPath = ""
					resource.Checksums = types.Checksums{}
					resource.Variant = "helm-provenance-v1:" + version.chart.Digest
					resource.Validate = validateRemoteProvenance(version.chart, target.url.Path)
				}
				resource.ExternalURL = upstream.String()
				resource.CheckURL = chartURLGuard(
					pkgroute.RequestFrom(r).Repo,
					metadata.Metadata.BaseURL,
					provenance,
				)
				result, readErr := h.k.Read(r, resource)
				if readErr != nil {
					h.k.Error(w, r, readErr)
					return
				}
				if provenance && result.Item != nil {
					if err = h.cachedProvenance(
						r.Context(),
						result,
						resource.Validate,
					); err != nil {
						h.k.Error(w, r, err)
						return
					}
				}
				fenceHelmDisclosures(&result, metadata, []string{owner})
				h.k.ServeResult(w, r, result)
				return
			}
		}
	}
	h.k.Error(w, r, apierror.New(http.StatusNotFound, "Could not find resource"))
}

// Cache-only artifacts are selected by their local path, without re-running
// admission. Bind signed provenance to the current index before any delivery,
// including HEAD/conditional/signed-URL responses and offline reads.
func (h handler) cachedProvenance(
	ctx context.Context,
	result source.Result,
	validate func(context.Context, io.ReadSeeker, int64) error,
) error {
	body, info, err := h.k.Blobs().Open(ctx, result.Item.Actual.SHA256)
	if err != nil {
		return remoteError()
	}
	defer body.Close()
	return validate(ctx, body, info.Size)
}

func (h handler) remoteIndex(w http.ResponseWriter, r *http.Request) {
	index, result, err := h.upstreamIndex(r)
	if err != nil {
		h.k.Error(w, r, err)
		return
	}
	c := pkgroute.RequestFrom(r).Repo
	prefix := baseurl.From(r.Context()) + "/artifactory/api/helm/" + url.PathEscape(c.Key) + "/"
	if h.settings.Relative() {
		prefix = ""
	}
	var paths []string
	var entries []*yaml.Node
	for i := 0; i+1 < len(index.entries.Content); i += 2 {
		name := index.entries.Content[i]
		versions := &yaml.Node{Kind: yaml.SequenceNode, Tag: "!!seq"}
		for _, version := range index.versions[name.Value] {
			var locations []string
			for _, target := range version.targets {
				if !h.k.Can(r, target.path, types.ActionRead) {
					continue
				}
				// Signed upstream query parameters remain adapter-only authority.
				rewritten := (&url.URL{Path: target.path}).String()
				locations = append(locations, prefix+rewritten)
				paths = append(paths, target.path)
			}
			if len(locations) == 0 {
				continue
			}
			urls := mappingValue(version.node, "urls")
			if urls == nil || urls.Encode(locations) != nil {
				h.k.Error(w, r, remoteError())
				return
			}
			versions.Content = append(versions.Content, version.node)
		}
		if len(versions.Content) > 0 {
			entries = append(entries, name, versions)
		}
	}
	index.entries.Content = entries
	var encoded bytes.Buffer
	encoder := yaml.NewEncoder(&encoded)
	encoder.SetIndent(2)
	if err = encoder.Encode(index.node); err != nil {
		h.k.Error(w, r, remoteError())
		return
	}
	if err = encoder.Close(); err != nil {
		h.k.Error(w, r, remoteError())
		return
	}
	data := encoded.Bytes()
	fenceHelmDisclosures(&result, result, paths)
	result.Stream = &source.Stream{
		Status: http.StatusOK, Body: &remoteIndexBody{Reader: bytes.NewReader(data)},
		Representation: source.Representation{
			MediaType: "application/x-yaml",
			Size:      int64(len(data)),
			ETag:      fmt.Sprintf(`"%x"`, sha256.Sum256(data)),
		},
	}
	h.k.ServeResult(w, r, result)
}

type remoteIndexBody struct{ *bytes.Reader }

func (*remoteIndexBody) Close() error { return nil }

func fenceHelmDisclosures(result *source.Result, metadata source.Result, paths []string) {
	prior := result.Revalidate
	result.Revalidate = func(ctx context.Context) error {
		if prior != nil {
			if err := prior(ctx); err != nil {
				return err
			}
		}
		if metadata.Revalidate != nil {
			if err := metadata.Revalidate(ctx); err != nil {
				return fmt.Errorf("helm metadata authority: %w", err)
			}
		}
		if metadata.CheckRead != nil {
			for _, path := range paths {
				if err := metadata.CheckRead(ctx, path); err != nil {
					return fmt.Errorf("helm chart authority: %w", err)
				}
			}
		}
		return nil
	}
}
