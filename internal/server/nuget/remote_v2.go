package nuget

import (
	"context"
	"encoding/xml"
	"fmt"
	"io"
	"net/http"
	"net/url"
	"strings"

	nugetfmt "github.com/weaveplatform/artifactweave/internal/format/nuget"
	"github.com/weaveplatform/artifactweave/internal/server/apierror"
	"github.com/weaveplatform/artifactweave/internal/server/pkgroute"
	"github.com/weaveplatform/artifactweave/internal/source"
	"github.com/weaveplatform/artifactweave/pkg/repo"
	"github.com/weaveplatform/artifactweave/pkg/types"
)

func (h handler) remoteV2(next http.Handler) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if pkgroute.RequestFrom(r).Repo.Class != repo.Remote {
			next.ServeHTTP(w, r)
			return
		}
		if r.Method != http.MethodGet && r.Method != http.MethodHead {
			h.k.Error(
				w,
				r,
				apierror.New(http.StatusMethodNotAllowed, "remote NuGet repository is read-only"),
			)
			return
		}
		parts, err := segments(r, "")
		if err != nil {
			h.k.Error(w, r, err)
			return
		}
		s := &remoteSession{h: h, r: r, disclosed: make(map[string]source.Result)}
		s.legacy(r.Context(), w, parts)
	})
}

func nugetContext(c repo.Config, configured *string, parts ...string) (string, error) {
	context := ""
	if configured != nil {
		context = *configured
	}
	segments := strings.Split(strings.Trim(context, "/"), "/")
	if context == "" {
		segments = nil
	}
	for _, p := range segments {
		if p == "." || p == ".." || strings.ContainsAny(p, "?#\\:") {
			return "", remoteFailure()
		}
	}
	target := appendResource(c.URL, append(segments, parts...)...)
	check := c
	check.V3FeedURL = c.URL
	if resourceURL(check, target, false) != nil {
		return "", remoteFailure()
	}
	return target, nil
}

func (s *remoteSession) legacy(ctx context.Context, w http.ResponseWriter, parts []string) {
	c := pkgroute.RequestFrom(s.r).Repo
	if len(parts) == 0 || len(parts) == 1 && parts[0] == "$metadata" {
		kind := "service"
		if len(parts) > 0 {
			kind = "metadata"
		}
		target, err := nugetContext(c, c.FeedContextPath, parts...)
		if err == nil {
			_, _, _, err = s.xmlDocument(target, "", kind)
		}
		if err != nil {
			s.h.k.Error(w, s.r, err)
			return
		}
		w.Header().Set("DataServiceVersion", "2.0")
		if kind == "metadata" {
			s.bytesResponse(w, v2MetadataBody(), "application/xml")
		} else {
			s.xml(w, v2Service(s.r), "application/xml")
		}
		return
	}
	if len(parts) == 3 && parts[0] == "Download" {
		s.legacyDownload(ctx, w, parts[1], parts[2])
		return
	}
	if len(parts) == 1 {
		if match := v2Key.FindStringSubmatch(parts[0]); match != nil {
			s.legacyEntry(
				w,
				strings.ReplaceAll(match[1], "''", "'"),
				strings.ReplaceAll(match[2], "''", "'"),
			)
			return
		}
	}
	count := len(parts) == 2 && parts[1] == "$count"
	if len(parts) != 1 && !count {
		s.h.k.Error(w, s.r, notFound(s.r))
		return
	}
	switch parts[0] {
	case "Packages", "Packages()", "Search()", "FindPackagesById()":
		s.legacyFeed(w, parts[0], count)
	default:
		s.h.k.Error(w, s.r, notFound(s.r))
	}
}

func (s *remoteSession) xmlDocument(
	target, id, kind string,
) (*remoteXML, source.Result, string, error) {
	var zero source.Result
	s.calls++
	if s.calls > remoteGraphLimit {
		return nil, zero, "", remoteFailure()
	}
	c := pkgroute.RequestFrom(s.r).Repo
	check := c
	check.V3FeedURL = c.URL
	if resourceURL(check, target, false) != nil {
		return nil, zero, "", remoteFailure()
	}
	resource := source.Resource{
		Kind:             source.ResourceKindMetadata,
		Path:             strings.TrimPrefix(id+"/.nuget/v2/"+shortHash(target)+".xml", "/"),
		ExternalURL:      target,
		Method:           http.MethodGet,
		PermissionFolder: true,
		PermissionPath:   id,
		Accept:           "application/atom+xml, application/xml, text/xml",
		ExpectedContentTypes: []string{
			"application/atom+xml",
			"application/xml",
			"text/xml",
			"application/atomsvc+xml",
			// XML without a declaration sniffs as plain text. Full XML/schema
			// admission remains mandatory before this alternative can publish.
			"text/plain",
		},
		Variant:        "nuget-v2-" + kind + "-v1",
		NuGetEmptyFeed: kind == "feed",
		ValidateMetadata: func(_ context.Context, r io.ReadSeeker, size int64, response source.MetadataResponse) error {
			if size > remoteDocumentLimit {
				return remoteFailure()
			}
			doc, err := readRemoteXML(r)
			if err != nil {
				return err
			}
			return validateRemoteXML(c, doc, response.BaseURL, kind, id)
		},
	}
	result, err := s.h.k.Read(s.r, resource)
	if err != nil {
		return nil, zero, "", fmt.Errorf("NuGet v2 source: %w", err)
	}
	if result.Stream == nil {
		return nil, zero, "", remoteFailure()
	}
	defer result.Stream.Body.Close()
	root, err := readRemoteXML(result.Stream.Body)
	if err != nil {
		return nil, zero, "", err
	}
	s.bytes += result.Stream.Size
	if s.bytes > 64<<20 {
		return nil, zero, "", remoteFailure()
	}
	s.results = append(s.results, result)
	return root, result, result.Metadata.BaseURL, nil
}

func validateRemoteXML(c repo.Config, n *remoteXML, base, kind, id string) error {
	switch kind {
	case "feed":
		_, err := parseRemoteAtom(c, n, base, id)
		return err
	case "service":
		if n.name.Local != "service" || n.name.Space != "http://www.w3.org/2007/app" {
			return remoteFailure()
		}
	case "metadata":
		if n.name.Local != "Edmx" ||
			n.name.Space != "http://schemas.microsoft.com/ado/2007/06/edmx" {
			return remoteFailure()
		}
	default:
		return remoteFailure()
	}
	return validateXMLLinks(c, n, base)
}

func validateXMLLinks(c repo.Config, n *remoteXML, base string) error {
	base, err := n.base(c, base)
	if err != nil {
		return err
	}
	for _, a := range n.attrs {
		if a.Name.Local == "href" || a.Name.Local == "src" {
			if _, err = remoteXMLURL(c, base, a.Value); err != nil {
				return err
			}
		}
	}
	for _, child := range n.children {
		if err = validateXMLLinks(c, child, base); err != nil {
			return err
		}
	}
	return nil
}

func (s *remoteSession) xml(w http.ResponseWriter, value any, mime string) {
	body, _ := xml.Marshal(value)
	w.Header().Set("DataServiceVersion", "2.0")
	s.bytesResponse(w, append([]byte(xml.Header), body...), mime)
}

func (s *remoteSession) legacyTarget(endpoint string, q url.Values) (string, error) {
	c := pkgroute.RequestFrom(s.r).Repo
	target, err := nugetContext(c, c.FeedContextPath, endpoint)
	if err != nil {
		return "", err
	}
	u, _ := url.Parse(target)
	values := u.Query()
	for name, vs := range q {
		if len(vs) > 0 {
			values.Set(name, vs[0])
		}
	}
	u.RawQuery = values.Encode()
	return u.String(), nil
}

func (s *remoteSession) legacyDownload(
	ctx context.Context,
	w http.ResponseWriter,
	id, version string,
) {
	id = nugetfmt.PackageName(id)
	key, valid := versionKey(version)
	if !nugetfmt.ValidID(id) || !valid {
		s.h.k.Error(w, s.r, notFound(s.r))
		return
	}
	if !s.h.k.Require(w, s.r, remotePackagePath(id, key), types.ActionRead) {
		return
	}
	entries, err := s.legacyEntries(id)
	if err != nil {
		s.h.k.Error(w, s.r, err)
		return
	}
	for _, entry := range entries {
		if entry.entry.meta.Version != key {
			continue
		}
		c := pkgroute.RequestFrom(s.r).Repo
		target, e := nugetContext(c, c.DownloadContextPath, entry.entry.meta.ID, key)
		if e != nil {
			s.h.k.Error(w, s.r, e)
			return
		}
		s.deliverPackage(ctx, w, id, key, target, entry.entry.checks, false)
		return
	}
	s.h.k.Error(w, s.r, notFound(s.r))
}
