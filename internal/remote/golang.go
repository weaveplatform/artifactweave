package remote

import (
	"context"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"net/url"
	"strings"
	"time"

	"golang.org/x/mod/modfile"
	"golang.org/x/mod/module"
	modzip "golang.org/x/mod/zip"

	goformat "github.com/weaveplatform/artifactweave/internal/format/golang"
	"github.com/weaveplatform/artifactweave/internal/source"
	"github.com/weaveplatform/artifactweave/pkg/repo"
)

// GoSource is the GOPROXY reader for the P10 virtual resolver. It deliberately
// installs no native remote HTTP route and never falls back to a VCS or sumdb.
// Source must be the application's authority-fenced source service, not a raw
// transport or cache backend. Callers retain the result's delivery/usage hooks.
type GoSource struct {
	Source interface {
		Read(context.Context, source.Request) (source.Result, error)
	}
}

func (g GoSource) Read(ctx context.Context, q source.Request) (source.Result, error) {
	if g.Source == nil || q.Repo.Class != repo.Remote || q.Repo.PackageType != repo.Go {
		return source.Result{}, source.ErrUnsupported
	}
	resource, err := GoResource(q.Resource.Path, q.Resource.Method)
	if err != nil {
		return source.Result{}, err
	}
	resource.Range, resource.IfRange = q.Resource.Range, q.Resource.IfRange
	resource.IfNoneMatch, resource.IfModifiedSince = q.Resource.IfNoneMatch, q.Resource.IfModifiedSince
	q.Resource = resource
	out, err := g.Source.Read(ctx, q)
	if err != nil {
		return out, fmt.Errorf("go upstream source: %w", err)
	}
	return out, nil
}

// GoResource separates the case-decoded storage/permission identity from the
// escaped GOPROXY target. Module ZIPs and go.mod files are immutable. Info,
// list and latest retain the existing format's metadata classification and
// remain authoritative upstream bytes, independent of downloaded cache items.
func GoResource(path, method string) (source.Resource, error) {
	if method == "" {
		method = http.MethodGet
	}
	if method != http.MethodGet && method != http.MethodHead {
		return source.Resource{}, source.ErrInvalidRequest
	}
	q, err := goformat.ParseRequest(path)
	if err != nil {
		return source.Resource{}, source.ErrInvalidRequest
	}
	escaped, err := module.EscapePath(q.Module)
	if err != nil {
		return source.Resource{}, source.ErrInvalidRequest
	}
	r := source.Resource{
		Kind:          source.Artifact,
		Method:        method,
		Variant:       "goproxy-v1",
		GoneIsMissing: true,
	}
	switch q.Kind {
	case goformat.KindList:
		r.Kind, r.Path, r.UpstreamPath = source.Versions, q.Module+"/@v/list", escaped+"/@v/list"
		r.Accept = "text/plain"
	case goformat.KindLatest:
		r.Kind, r.Path, r.UpstreamPath = source.Metadata, q.Module+"/@latest", escaped+"/@latest"
		r.Accept = "application/json"
	default:
		version, escapeErr := module.EscapeVersion(q.Version)
		if escapeErr != nil {
			return source.Resource{}, source.ErrInvalidRequest
		}
		if q.Kind == goformat.KindInfo {
			r.Kind = source.Metadata
		}
		f := q.File()
		r.Path, r.UpstreamPath = f.Path(), escaped+"/@v/"+version+"."+f.Ext
		r.Accept = map[goformat.Kind]string{
			goformat.KindInfo: "application/json", goformat.KindMod: "text/plain",
			goformat.KindZip: "application/zip",
		}[q.Kind]
	}
	r.UpstreamPath = (&url.URL{Path: r.UpstreamPath}).EscapedPath()
	r.Validate = func(ctx context.Context, body io.ReadSeeker, size int64) error {
		return validateGoResource(ctx, q, body, size)
	}
	return r, nil
}

func validateGoResource(
	ctx context.Context,
	q goformat.Request,
	body io.ReadSeeker,
	size int64,
) error {
	if err := ctx.Err(); err != nil {
		return fmt.Errorf("validate Go response: %w", err)
	}
	if q.Kind == goformat.KindZip {
		return validateGoZip(ctx, q.File(), body, size)
	}
	limit := int64(goformat.MaxInfo)
	switch q.Kind {
	case goformat.KindMod:
		limit = modzip.MaxGoMod
	case goformat.KindList:
		limit = 4 << 20
	}
	if size < 0 || size > limit {
		return ErrGoResponse
	}
	data, err := io.ReadAll(io.LimitReader(body, limit+1))
	if err != nil || int64(len(data)) != size {
		return ErrGoResponse
	}
	switch q.Kind {
	case goformat.KindList:
		return validateGoList(q.Module, data)
	case goformat.KindMod:
		f, parseErr := modfile.ParseLax("go.mod", data, nil)
		if parseErr != nil || f.Module == nil || f.Module.Mod.Path != q.Module {
			return ErrGoResponse
		}
		return nil
	case goformat.KindLatest:
		var fields map[string]json.RawMessage
		var version string
		if json.Unmarshal(data, &fields) != nil ||
			json.Unmarshal(fields["Version"], &version) != nil {
			return ErrGoResponse
		}
		selected, parseErr := goformat.ParseRequest(q.Module + "/@v/" + version + ".info")
		if parseErr != nil {
			return ErrGoResponse
		}
		q = selected
	}
	if err = goformat.CheckInfo(q.File(), data); err != nil {
		return ErrGoResponse
	}
	return nil
}

func validateGoList(modulePath string, data []byte) error {
	for _, line := range strings.Split(string(data), "\n") {
		fields := strings.Fields(line)
		if len(fields) == 0 {
			continue
		}
		if len(fields) > 2 {
			return ErrGoResponse
		}
		if _, err := goformat.ParseRequest(modulePath + "/@v/" + fields[0] + ".info"); err != nil {
			return ErrGoResponse
		}
		// Artifactory's optional timestamp extension is preserved byte-for-byte.
		if len(fields) == 2 {
			if _, err := time.Parse(time.RFC3339, fields[1]); err != nil {
				return ErrGoResponse
			}
		}
	}
	return nil
}
