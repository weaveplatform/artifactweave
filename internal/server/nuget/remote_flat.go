package nuget

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"io"
	"net/http"
	"strings"

	"github.com/weaveplatform/artifactweave/internal/archive"
	nugetfmt "github.com/weaveplatform/artifactweave/internal/format/nuget"
	"github.com/weaveplatform/artifactweave/internal/server/pkgroute"
	"github.com/weaveplatform/artifactweave/internal/source"
	"github.com/weaveplatform/artifactweave/pkg/types"
)

func shortHash(
	s string,
) string {
	sum := sha256.Sum256([]byte(s))
	return hex.EncodeToString(sum[:])
}

func (s *remoteSession) versions(id string) ([]string, source.Result, error) {
	if !nugetfmt.ValidID(id) {
		return nil, source.Result{}, notFound(s.r)
	}
	id = nugetfmt.PackageName(id)
	base, err := s.endpoint("flatcontainer/")
	if err != nil {
		return nil, source.Result{}, err
	}
	d, result, err := s.document(
		appendResource(base, id, "index.json"),
		id+"/.nuget/versions.json",
		id,
		"versions",
		id,
	)
	if err != nil {
		return nil, result, err
	}
	var versions []string
	_ = json.Unmarshal(d["versions"], &versions)
	return versions, result, nil
}

func (s *remoteSession) flat(ctx context.Context, w http.ResponseWriter, p []string) {
	if len(p) == 2 && p[1] == "index.json" {
		id := nugetfmt.PackageName(p[0])
		vs, result, err := s.versions(id)
		if err != nil {
			s.h.k.Error(w, s.r, err)
			return
		}
		out := make([]string, 0, len(vs))
		for _, v := range vs {
			if s.allowed(id, v, result) {
				key, _ := versionKey(v)
				out = append(out, key)
			}
		}
		if len(out) == 0 {
			s.h.k.Error(w, s.r, notFound(s.r))
			return
		}
		s.json(w, map[string]any{"versions": out})
		return
	}
	if len(p) != 3 || !nugetfmt.ValidID(p[0]) {
		s.h.k.Error(w, s.r, notFound(s.r))
		return
	}
	id := nugetfmt.PackageName(p[0])
	key, ok := versionKey(p[1])
	if !ok {
		s.h.k.Error(w, s.r, notFound(s.r))
		return
	}
	filename := strings.ToLower(p[2])
	nuspec := filename == id+".nuspec"
	// Accept case and normalized version aliases without creating another cache.
	want := id + "." + strings.ToLower(p[1]) + ".nupkg"
	if !nuspec && filename != want && filename != id+"."+key+".nupkg" {
		s.h.k.Error(w, s.r, notFound(s.r))
		return
	}
	s.packageFile(ctx, w, id, key, nuspec)
}

func (s *remoteSession) packageTarget(id, key string) (string, types.Checksums, error) {
	if hv, ok := s.preferredHive(); ok {
		vs, err := s.registrations(hv, id)
		if err != nil {
			return "", types.Checksums{}, err
		}
		for _, v := range vs {
			if v.key == key {
				d := objectField(v.entry, "catalogEntry")
				checks, err := entryChecksum(d)
				target := stringField(v.entry, "packageContent")
				if target == "" {
					target = stringField(d, "packageContent")
				}
				return target, checks, err
			}
		}
		return "", types.Checksums{}, notFound(s.r)
	}
	// A minimal v3 feed may expose only PackageBaseAddress. No invented hash or
	// registration resource is required; native signature verification still runs.
	base, err := s.endpoint("flatcontainer/")
	if err != nil {
		return "", types.Checksums{}, err
	}
	return appendResource(base, id, key, id+"."+key+".nupkg"), types.Checksums{}, nil
}

func (s *remoteSession) packageFile(
	ctx context.Context,
	w http.ResponseWriter,
	id, version string,
	nuspec bool,
) {
	id = nugetfmt.PackageName(id)
	key, ok := versionKey(version)
	if !nugetfmt.ValidID(id) || !ok {
		s.h.k.Error(w, s.r, notFound(s.r))
		return
	}
	path := remotePackagePath(id, key)
	if !s.h.k.Require(w, s.r, path, types.ActionRead) {
		return
	}
	target, checks, err := s.packageTarget(id, key)
	if err != nil {
		s.h.k.Error(w, s.r, err)
		return
	}
	if err = s.fence(ctx); err != nil {
		s.h.k.Error(w, s.r, pkgroute.SourceError(err, false))
		return
	}
	resource := pkgroute.Resource(s.r, path, "")
	resource.Method = http.MethodGet
	if nuspec {
		resource.Range = ""
		resource.IfRange = ""
		resource.IfNoneMatch = ""
		resource.IfModifiedSince = ""
	}
	resource.ExternalURL = target
	resource.Checksums = checks
	resource.Query = ""
	resource.Accept = "application/octet-stream"
	// Admission sees complete bytes even when the downstream asked for HEAD or
	// a range. ZIP identity and declared integrity precede durable or transient publication.
	resource.Variant = "nuget-package-v1"
	resource.Validate = func(ctx context.Context, r io.ReadSeeker, size int64) error {
		return nugetfmt.ValidateRemote(ctx, r, size, id, key)
	}
	result, err := s.h.k.Read(s.r, resource)
	if err != nil {
		s.h.k.Error(w, s.r, err)
		return
	}
	current := result.Revalidate
	result.Revalidate = func(ctx context.Context) error {
		if err := s.fence(ctx); err != nil {
			return err
		}
		if current != nil {
			return current(ctx)
		}
		return nil
	}
	if !nuspec {
		s.h.k.ServeResult(w, s.r, result)
		return
	}
	var n *nugetfmt.Nuspec
	if result.Item != nil {
		n, err = nugetfmt.ReadStored(ctx, s.h.k.Blobs(), result.Item.Actual.SHA256)
	} else {
		defer result.Stream.Body.Close()
		seeker, ok := result.Stream.Body.(io.ReadSeeker)
		if !ok {
			err = remoteFailure()
		} else {
			n, err = nugetfmt.ReadPackage(archive.ReaderAt(seeker), result.Stream.Size)
		}
	}
	if err != nil {
		s.h.k.Error(w, s.r, remoteFailure())
		return
	}
	s.results = append(s.results, source.Result{Revalidate: current, Used: result.Used})
	s.bytesResponse(w, n.Raw, "application/xml")
}
