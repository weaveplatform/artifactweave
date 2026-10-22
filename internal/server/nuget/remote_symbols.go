package nuget

import (
	"context"
	"io"
	"net/http"
	"strings"

	nugetfmt "github.com/weaveplatform/artifactweave/internal/format/nuget"
	"github.com/weaveplatform/artifactweave/internal/server/pkgroute"
	"github.com/weaveplatform/artifactweave/pkg/types"
)

func (s *remoteSession) symbol(w http.ResponseWriter, path string) {
	if !s.h.k.Require(w, s.r, path, types.ActionRead) {
		return
	}
	c := pkgroute.RequestFrom(s.r).Repo
	if c.SymbolServerURL == "" {
		s.h.k.Error(w, s.r, notFound(s.r))
		return
	}
	parts := strings.Split(strings.TrimPrefix(path, ".symbols/"), "/")
	target := appendResource(c.SymbolServerURL, parts...)
	// The explicitly configured symbol origin is trusted network configuration;
	// source transport still fences DNS/redirects and credential forwarding.
	check := c
	check.URL, check.V3FeedURL = c.SymbolServerURL, ""
	if err := resourceURL(check, target, false); err != nil {
		s.h.k.Error(w, s.r, err)
		return
	}
	resource := pkgroute.Resource(s.r, path, "")
	resource.Method = http.MethodGet
	resource.ExternalURL = target
	resource.Query = ""
	resource.Accept = "application/octet-stream"
	resource.Variant = "nuget-symbol-v1"
	resource.Validate = func(ctx context.Context, r io.ReadSeeker, size int64) error {
		if size > 64<<20 {
			return remoteFailure()
		}
		raw, err := io.ReadAll(io.LimitReader(r, 64<<20+1))
		if err != nil || len(raw) > 64<<20 || ctx.Err() != nil {
			return remoteFailure()
		}
		key, ok := nugetfmt.PDBKey(raw)
		if !ok || !strings.EqualFold(key, parts[1]) {
			return remoteFailure()
		}
		return nil
	}
	result, err := s.h.k.Read(s.r, resource)
	if err != nil {
		s.h.k.Error(w, s.r, err)
		return
	}
	if result.Stream != nil {
		result.Stream.MediaType = "application/octet-stream"
	}
	s.h.k.ServeResult(w, s.r, result)
}
