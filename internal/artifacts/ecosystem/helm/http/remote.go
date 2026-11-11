package helmhttp

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

	"github.com/weaveplatform/artifactweave/internal/access/api"
	"github.com/weaveplatform/artifactweave/internal/artifacts/adapters/http/pkgroute"
	"github.com/weaveplatform/artifactweave/internal/artifacts/core/remote/source"
	helmfmt "github.com/weaveplatform/artifactweave/internal/artifacts/ecosystem/helm"
	"github.com/weaveplatform/artifactweave/internal/kernel"
	"github.com/weaveplatform/artifactweave/internal/platform/httpkit/apierror"
	"github.com/weaveplatform/artifactweave/internal/platform/httpkit/baseurl"
)

// upstreamIndex reads the remote's index.yaml for the request whose policy
// path is download: index.yaml itself when the index is answered, which 7.x
// caches there, so a blackout or a pattern on index.yaml refuses it; or the
// chart or provenance file a download serves, which looks its URL up in the
// index on its way, so only the blackout refuses the lookup and its answer
// names the requested file.
func (h handler) upstreamIndex(
	r *http.Request,
	download string,
) (remoteIndex, source.Result, error) {
	c := pkgroute.RequestFrom(r).Repo
	resource := source.Resource{
		Kind:   source.ResourceKindMetadata,
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
		PolicyPath:       download,
		BlackoutOnly:     download != helmfmt.IndexPath,
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
	if !h.k.Require(w, r, requested, api.ActionRead) ||
		(provenance && !h.k.Require(w, r, owner, api.ActionRead)) {
		return
	}
	index, metadata, err := h.upstreamIndex(r, requested)
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
				// The chart's Read permission only: the read below applies the
				// policy at the requested path, so a provenance file is
				// checked at its own path, not its chart's, as 6.20's
				// RepoBase.accepts checks every path but a checksum's.
				if metadata.CheckPermission != nil {
					if err = metadata.CheckPermission(r.Context(), owner); err != nil {
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
					resource.Checksums = kernel.Checksums{}
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
	index, result, err := h.upstreamIndex(r, helmfmt.IndexPath)
	if err != nil {
		h.k.Error(w, r, err)
		return
	}
	c := pkgroute.RequestFrom(r).Repo
	prefix := baseurl.From(r.Context()) + "/artifactory/api/helm/" + url.PathEscape(c.Key) + "/"
	if h.settings.IsRelative() {
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
				if !h.k.Can(r, target.path, api.ActionRead) {
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

// fenceHelmDisclosures checks again, before delivery, that the caller may
// read each chart the answer discloses. It checks Read permission alone: the
// index lists a chart whose file the remote's patterns reject, as 7.x's does
// (RTFACT-29572), and a chart's own read result applies the policy to it.
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
		if metadata.CheckPermission != nil {
			for _, path := range paths {
				if err := metadata.CheckPermission(ctx, path); err != nil {
					return fmt.Errorf("helm chart authority: %w", err)
				}
			}
		}
		return nil
	}
}
