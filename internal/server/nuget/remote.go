package nuget

import (
	"bytes"
	"compress/gzip"
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
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

const (
	remoteDocumentLimit = 16 << 20
	remoteGraphLimit    = 128
)

type remoteDoc map[string]json.RawMessage

type remoteSession struct {
	h         handler
	r         *http.Request
	resources []resource
	results   []source.Result
	disclosed map[string]source.Result
	calls     int
	bytes     int64
}

func (h handler) remoteV3(next http.Handler) http.Handler {
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
		s := &remoteSession{h: h, r: r, disclosed: make(map[string]source.Result)}
		p, err := pkgroute.ParsePath(pkgroute.RequestFrom(r).Rest)
		if err == nil && len(p.Segments) == 3 {
			if path, ok := nugetfmt.SymbolPath(p.Segments[0], p.Segments[1], p.Segments[2]); ok {
				s.symbol(w, path)
				return
			}
		}
		if err == nil {
			err = s.discover()
		}
		if err != nil {
			h.k.Error(w, r, err)
			return
		}
		parts := p.Segments
		switch {
		case len(parts) == 0 || len(parts) == 1 && parts[0] == "index.json":
			s.serviceIndex(w)
		case len(parts) > 1 && parts[0] == "flatcontainer":
			s.flat(r.Context(), w, parts[1:])
		case len(parts) > 1 && (parts[0] == "registration" || parts[0] == "registration-semver2"):
			s.registration(
				r.Context(),
				w,
				hive{path: parts[0], semver2: parts[0] == "registration-semver2"},
				parts[1:],
			)
		case len(parts) == 1 && parts[0] == "query":
			s.search(w)
		case len(parts) == 1 && parts[0] == "autocomplete":
			s.autocomplete(w)
		case len(parts) == 2 && parts[0] == "repository-signatures":
			s.signatures(w, strings.TrimSuffix(parts[1], ".json"))
		case len(parts) == 3 && parts[0] == "repository-certificates":
			s.certificate(w, parts[1], strings.TrimSuffix(parts[2], ".der"))
		default:
			h.k.Error(w, r, notFound(r))
		}
	})
}

func remoteFailure() error {
	return apierror.New(http.StatusBadGateway, "invalid upstream NuGet resource")
}

func stringField(
	d remoteDoc,
	k string,
) string {
	var v string
	_ = json.Unmarshal(d[k], &v)
	return v
}

func objectField(d remoteDoc, k string) remoteDoc {
	var v remoteDoc
	_ = json.Unmarshal(d[k], &v)
	return v
}

func arrayField(d remoteDoc, k string) []remoteDoc {
	var v []remoteDoc
	_ = json.Unmarshal(d[k], &v)
	return v
}
func setField(d remoteDoc, k string, v any) { d[k] = encode(v) }

func decodeRemote(r io.Reader) (remoteDoc, error) {
	raw, err := io.ReadAll(io.LimitReader(r, remoteDocumentLimit+1))
	var d remoteDoc
	if err != nil || len(raw) > remoteDocumentLimit {
		return nil, remoteFailure()
	}
	if len(raw) > 2 && raw[0] == 0x1f && raw[1] == 0x8b {
		gz, e := gzip.NewReader(bytes.NewReader(raw))
		if e != nil {
			return nil, remoteFailure()
		}
		defer gz.Close()
		raw, e = io.ReadAll(io.LimitReader(gz, remoteDocumentLimit+1))
		if e != nil || len(raw) > remoteDocumentLimit {
			return nil, remoteFailure()
		}
	}
	if json.Unmarshal(raw, &d) != nil || d == nil {
		return nil, remoteFailure()
	}
	return d, nil
}

// Each cached document is validated independently of the first reader's grants.
// Rewritten documents exist only for this request, never in the shared cache.
func (s *remoteSession) document(
	target, path, folder, kind, id string,
) (remoteDoc, source.Result, error) {
	var zero source.Result
	s.calls++
	if s.calls > remoteGraphLimit {
		return nil, zero, remoteFailure()
	}
	c := pkgroute.RequestFrom(s.r).Repo
	if err := resourceURL(c, target, false); err != nil {
		return nil, zero, err
	}
	resource := source.Resource{
		Kind:                source.Metadata,
		Path:                path,
		Method:              http.MethodGet,
		ExternalURL:         target,
		PermissionFolder:    true,
		PermissionPath:      folder,
		Accept:              jsonType,
		ExpectedContentType: jsonType,
		Variant:             "nuget-v3-" + kind + "-v1",
		Validate: func(_ context.Context, r io.ReadSeeker, size int64) error {
			if size > remoteDocumentLimit {
				return remoteFailure()
			}
			d, err := decodeRemote(r)
			if err != nil {
				return fmt.Errorf("NuGet delivery fence: %w", err)
			}
			return validateDocument(c, d, kind, id)
		},
	}
	result, err := s.h.k.Read(s.r, resource)
	if err != nil {
		return nil, zero, fmt.Errorf("NuGet source: %w", err)
	}
	if result.Stream == nil {
		return nil, zero, remoteFailure()
	}
	defer result.Stream.Body.Close()
	d, err := decodeRemote(result.Stream.Body)
	if err != nil {
		return nil, zero, err
	}
	for _, value := range d {
		s.bytes += int64(len(value))
	}
	if s.bytes > 64<<20 {
		return nil, zero, remoteFailure()
	}
	s.results = append(s.results, result)
	return d, result, nil
}

func (s *remoteSession) discover() error {
	c := pkgroute.RequestFrom(s.r).Repo
	if c.V3FeedURL == "" {
		return remoteFailure()
	}
	doc, _, err := s.document(c.V3FeedURL, ".nuget/index.json", "", "index", "")
	if err != nil {
		return err
	}
	_ = json.Unmarshal(doc["resources"], &s.resources)
	return nil
}

func resourceRoute(t string) string {
	switch t {
	case "PackageBaseAddress/3.0.0":
		return "flatcontainer/"
	case "RegistrationsBaseUrl",
		"RegistrationsBaseUrl/3.0.0-beta",
		"RegistrationsBaseUrl/3.0.0-rc",
		"RegistrationsBaseUrl/3.4.0":
		return "registration/"
	case "RegistrationsBaseUrl/3.6.0":
		return "registration-semver2/"
	case "SearchQueryService",
		"SearchQueryService/3.0.0-beta",
		"SearchQueryService/3.0.0-rc",
		"SearchQueryService/3.5.0":
		return "query"
	case "SearchAutocompleteService",
		"SearchAutocompleteService/3.0.0-beta",
		"SearchAutocompleteService/3.0.0-rc",
		"SearchAutocompleteService/3.5.0":
		return "autocomplete"
	case "RepositorySignatures/4.7.0", "RepositorySignatures/4.9.0", "RepositorySignatures/5.0.0":
		return "repository-signatures/" + strings.TrimPrefix(t, "RepositorySignatures/") + ".json"
	}
	return ""
}

func (s *remoteSession) endpoint(route string) (string, error) {
	var selected resource
	for _, r := range s.resources {
		if resourceRoute(r.Type) == route && (selected.ID == "" || r.Type > selected.Type) {
			selected = r
		}
	}
	if selected.ID == "" {
		return "", apierror.New(http.StatusNotFound, "upstream NuGet resource is unavailable")
	}
	return selected.ID, nil
}

func (s *remoteSession) serviceIndex(w http.ResponseWriter) {
	out := make([]resource, 0)
	for _, r := range s.resources {
		if path := resourceRoute(r.Type); path != "" {
			out = append(out, resource{ID: v3Base(s.r) + "/" + path, Type: r.Type})
		}
	}
	s.json(w, serviceIndex{Version: "3.0.0", Resources: out})
}

func (s *remoteSession) fence(ctx context.Context) error {
	for _, r := range s.results {
		if r.Revalidate != nil {
			if err := r.Revalidate(ctx); err != nil {
				return fmt.Errorf("NuGet delivery fence: %w", err)
			}
		}
	}
	for path, r := range s.disclosed {
		if r.CheckRead != nil {
			if err := r.CheckRead(ctx, path); err != nil {
				return fmt.Errorf("NuGet delivery fence: %w", err)
			}
		}
	}
	return nil
}

func (s *remoteSession) allowed(id, version string, result source.Result) bool {
	path := remotePackagePath(id, version)
	if !s.h.k.Can(s.r, path, types.ActionRead) {
		return false
	}
	s.disclosed[path] = result
	return true
}

func (s *remoteSession) json(w http.ResponseWriter, d any) {
	s.bytesResponse(w, append(encode(d), '\n'), jsonType)
}

func (s *remoteSession) bytesResponse(w http.ResponseWriter, b []byte, media string) {
	sum := sha256.Sum256(b)
	result := source.Result{Revalidate: s.fence, Stream: &source.Stream{
		Body:   &remoteBody{bytes.NewReader(b)},
		Status: http.StatusOK,
		Representation: source.Representation{
			Size:      int64(len(b)),
			MediaType: media,
			ETag:      `"` + hex.EncodeToString(sum[:]) + `"`,
		},
	}}
	result.Used = func(ctx context.Context) error {
		for _, r := range s.results {
			if r.Used != nil {
				if err := r.Used(ctx); err != nil {
					return fmt.Errorf("NuGet cache usage: %w", err)
				}
			}
		}
		return nil
	}
	s.h.k.ServeResult(w, s.r, result)
}

type remoteBody struct{ *bytes.Reader }

func (*remoteBody) Close() error { return nil }

func remotePackagePath(id, version string) string {
	id = nugetfmt.PackageName(id)
	key, _ := versionKey(version)
	return id + "/" + key + "/" + id + "." + key + ".nupkg"
}

func appendResource(base string, segments ...string) string {
	// Protocol bases are absolute URLs; retain their query (including signed CDN
	// query parameters) while adding escaped identity segments to the path.
	u, _ := url.Parse(base)
	for _, p := range segments {
		u.Path = strings.TrimRight(u.Path, "/") + "/" + p
	}
	u.RawPath = ""
	return u.String()
}
