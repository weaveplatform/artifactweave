package server

import (
	"bytes"
	"context"
	"crypto/sha256"
	"crypto/sha512"
	"encoding/hex"
	"encoding/json"
	"errors"
	"io"
	"net"
	"net/http"
	"net/url"
	"path"
	"strings"
	"time"

	"github.com/weaveplatform/artifactweave/internal/auth"
	"github.com/weaveplatform/artifactweave/internal/format/p2"
	"github.com/weaveplatform/artifactweave/internal/server/pkgroute"
	"github.com/weaveplatform/artifactweave/internal/source"
	"github.com/weaveplatform/artifactweave/pkg/repo"
	"github.com/weaveplatform/artifactweave/pkg/store"
	"github.com/weaveplatform/artifactweave/pkg/types"
)

const p2ExternalPrefix = "__p2_ext"

func p2CompositeName(name string) bool {
	if strings.Contains(name, "/") {
		return false
	}
	switch name {
	case "compositeContent.xml", "compositeContent.jar", "compositeContent.xml.xz",
		"compositeArtifacts.xml", "compositeArtifacts.jar", "compositeArtifacts.xml.xz":
		return true
	}
	return false
}

func p2Origin(u *url.URL) string {
	port := u.Port()
	if port == "" {
		if u.Scheme == "https" {
			port = "443"
		} else {
			port = "80"
		}
	}
	return strings.ToLower(u.Scheme) + "://" + net.JoinHostPort(
		strings.TrimSuffix(strings.ToLower(u.Hostname()), "."), port,
	)
}

func p2CycleKey(u *url.URL) string {
	v := *u
	v.Path = strings.TrimRight(v.Path, "/")
	v.RawPath, v.RawQuery, v.Fragment = "", "", ""
	return v.String()
}

func p2AllowedOrigin(c repo.Config, target *url.URL) bool {
	base, err := url.Parse(c.URL)
	if err != nil || base.Hostname() == "" {
		return false
	}
	if p2Origin(base) == p2Origin(target) {
		return true
	}
	for _, raw := range c.P2ChildOrigins {
		u, err := url.Parse(raw)
		if err == nil && p2Origin(u) == p2Origin(target) {
			return true
		}
	}
	return false
}

func p2ChildURL(c repo.Config, parent *url.URL, raw string) (*url.URL, error) {
	if !p2.ValidChildLocation(raw) || parent == nil {
		return nil, p2.ErrInvalid
	}
	ref, err := url.Parse(raw)
	if err != nil {
		return nil, p2.ErrInvalid
	}
	u := parent.ResolveReference(ref)
	if len(u.String()) > p2.MaxChildURL || u.User != nil || u.Fragment != "" ||
		!p2AllowedOrigin(c, u) {
		return nil, p2.ErrInvalid
	}
	return u, nil
}

func p2ChildDigest(id types.SourceIdentity, parent, rawURL string) string {
	input, _ := json.Marshal([]any{id.Repository, id.Generation, id.ID, parent, rawURL})
	sum := sha256.Sum256(input)
	return hex.EncodeToString(sum[:])
}

func p2RootBase(c repo.Config) (*url.URL, error) {
	u, err := url.Parse(c.URL)
	if err != nil || u.Hostname() == "" {
		return nil, p2.ErrInvalid
	}
	for _, name := range []string{
		"content.jar", "content.xml", "artifacts.jar", "artifacts.xml",
		"compositeContent.xml", "compositeArtifacts.xml", "p2.index",
	} {
		if strings.HasSuffix(u.Path, "/"+name) {
			u.Path = strings.TrimSuffix(u.Path, name)
			break
		}
	}
	if !strings.HasSuffix(u.Path, "/") {
		u.Path += "/"
	}
	u.RawPath = ""
	u.RawQuery = ""
	return u, nil
}

func p2ChildResourceURL(child *url.URL, name string) (*url.URL, string, error) {
	if p2.Classify(name) == p2.Invalid {
		return nil, "", p2.ErrInvalid
	}
	u := *child
	if archive, prefix, ok := strings.Cut(u.Path, "!/"); ok {
		if prefix != "" && !strings.HasSuffix(prefix, "/") {
			prefix += "/"
		}
		u.Path = archive
		u.RawPath = ""
		return &u, prefix + name, nil
	}
	if !strings.HasSuffix(u.Path, "/") {
		u.Path += "/"
	}
	u.RawPath = ""
	query := u.RawQuery
	ref := &url.URL{Path: name}
	u = *u.ResolveReference(ref)
	u.RawQuery = query
	return &u, "", nil
}

type p2ReadResult struct {
	body []byte
	base *url.URL
	id   types.SourceIdentity
	used func(context.Context) error
}

func (s artifactoryService) p2Read(r *http.Request, c repo.Config, cached bool,
	pathName string, external *url.URL, archiveEntry string, p auth.Principal, acc access,
	expected ...types.Checksums,
) (p2ReadResult, error) {
	if s.cfg.RemoteSources == nil {
		return p2ReadResult{}, source.ErrUnavailable
	}
	rootArchive := false
	if external == nil && p2CompositeName(pathName) {
		if archive, ok := p2ArchiveURL(c.URL); ok {
			var err error
			external, err = url.Parse(archive)
			if err != nil {
				return p2ReadResult{}, source.ErrInvalidRequest
			}
			archiveEntry = pathName
			rootArchive = true
		}
	}
	q := source.Request{
		Repo: c, Subject: p.Subject(), Access: acc.d, Project: c.ProjectKey,
		CacheOnly: cached,
		Resource:  source.Resource{Kind: source.Artifact, Path: pathName, Method: http.MethodGet},
	}
	var want types.Checksums
	if len(expected) > 0 {
		want = expected[0]
	}
	var err error
	if archiveEntry == "" {
		q, err = p2SourceRequest(q)
		if err != nil {
			return p2ReadResult{}, err
		}
	} else {
		q.Resource.Kind = source.Artifact
		q.Resource.Variant = "p2-external-archive-v1"
		q.Resource.Validate = func(_ context.Context, src io.ReadSeeker, size int64) error {
			if size <= 0 || size > p2.MaxArtifact {
				return source.ErrInvalidResult
			}
			body, err := io.ReadAll(io.LimitReader(src, p2.MaxArtifact+1))
			if err != nil || int64(len(body)) != size {
				return source.ErrInvalidResult
			}
			entry, err := p2.ExtractArchive(body, archiveEntry)
			if err != nil {
				return err
			}
			return p2CheckBytes(entry, want)
		}
	}
	if archiveEntry == "" {
		q.Resource.Checksums = want
	}
	if external != nil {
		expected := p2Origin(external)
		q.Resource.ExternalURL = external.String()
		root, err := p2RootBase(c)
		if err != nil {
			return p2ReadResult{}, source.ErrInvalidRequest
		}
		q.Resource.NoCrossOriginAuth = !rootArchive && expected != p2Origin(root)
		q.Resource.CheckURL = func(_ context.Context, next *url.URL) error {
			if next.User != nil || next.Fragment != "" || p2Origin(next) != expected ||
				!p2AllowedOrigin(c, next) {
				return source.ErrDenied
			}
			return nil
		}
		q.Resource.Variant += "-external-v1"
	}
	result, err := s.cfg.RemoteSources.Read(r.Context(), q)
	if err != nil {
		return p2ReadResult{}, err
	}
	if result.Revalidate != nil {
		if err := result.Revalidate(r.Context()); err != nil {
			if result.Stream != nil {
				_ = result.Stream.Body.Close()
			}
			return p2ReadResult{}, err
		}
	}
	limit := int64(p2.MaxMetadata)
	if q.Resource.Kind == source.Artifact {
		limit = p2.MaxArtifact
	}
	var reader io.ReadCloser
	if result.Stream != nil {
		reader = result.Stream.Body
	} else if result.Item != nil {
		reader, _, err = s.cfg.Blobs.Open(r.Context(), result.Item.Actual.SHA256)
	} else {
		err = source.ErrInvalidResult
	}
	if err != nil {
		return p2ReadResult{}, err
	}
	defer func() { _ = reader.Close() }()
	body, err := io.ReadAll(io.LimitReader(reader, limit+1))
	if err != nil || int64(len(body)) > limit {
		return p2ReadResult{}, source.ErrInvalidResult
	}
	if archiveEntry != "" {
		body, err = p2.ExtractArchive(body, archiveEntry)
		if err != nil {
			return p2ReadResult{}, source.ErrInvalidResult
		}
	}
	baseRaw := result.Metadata.BaseURL
	if archiveEntry != "" && external != nil {
		virtual := *external
		virtual.Path += "!/" + archiveEntry
		virtual.RawPath = ""
		baseRaw = virtual.String()
	}
	if baseRaw == "" && external != nil {
		baseRaw = external.String()
	}
	base, _ := url.Parse(baseRaw)
	return p2ReadResult{body: body, base: base, id: result.Identity, used: result.Used}, nil
}

func p2CheckBytes(body []byte, want types.Checksums) error {
	if want.SHA256 != "" {
		sum := sha256.Sum256(body)
		if hex.EncodeToString(sum[:]) != want.SHA256 {
			return source.ErrInvalidResult
		}
	}
	if want.SHA512 != "" {
		sum := sha512.Sum512(body)
		if hex.EncodeToString(sum[:]) != want.SHA512 {
			return source.ErrInvalidResult
		}
	}
	return nil
}

func (s artifactoryService) p2ArtifactChecksums(r *http.Request, c repo.Config,
	cached bool, synthetic string, site *url.URL, resource string, p auth.Principal, acc access,
) (types.Checksums, error) {
	parent := strings.TrimSuffix(synthetic, "/"+resource)
	for _, candidate := range []string{"artifacts.xml.xz", "artifacts.jar", "artifacts.xml"} {
		target, entry, err := p2ChildResourceURL(site, candidate)
		if err != nil {
			return types.Checksums{}, source.ErrInvalidRequest
		}
		metadata, err := s.p2Read(r, c, cached, parent+"/"+candidate, target, entry, p, acc)
		if errors.Is(err, store.ErrNotFound) {
			continue
		}
		if err != nil {
			return types.Checksums{}, err
		}
		want, err := p2.ArtifactChecksums(candidate, metadata.body, resource)
		if err != nil {
			return types.Checksums{}, source.ErrInvalidResult
		}
		return want, nil
	}
	return types.Checksums{}, store.ErrNotFound
}

func p2CompositeCandidates(name string) []string {
	if strings.HasPrefix(path.Base(name), "artifacts") ||
		strings.HasPrefix(path.Base(name), "compositeArtifacts") ||
		strings.HasPrefix(name, "plugins/") || strings.Contains(name, "/plugins/") ||
		strings.HasPrefix(name, "features/") || strings.Contains(name, "/features/") ||
		strings.HasPrefix(name, "binary/") || strings.Contains(name, "/binary/") {
		return []string{
			"compositeArtifacts.xml",
			"compositeArtifacts.jar",
			"compositeArtifacts.xml.xz",
		}
	}
	if name == "p2.index" {
		return []string{
			"compositeContent.xml", "compositeContent.jar", "compositeContent.xml.xz",
			"compositeArtifacts.xml", "compositeArtifacts.jar", "compositeArtifacts.xml.xz",
		}
	}
	return []string{"compositeContent.xml", "compositeContent.jar", "compositeContent.xml.xz"}
}

func p2ExternalParts(name string) ([]string, string, error) {
	parts := strings.Split(name, "/")
	if len(parts) < 3 || parts[0] != p2ExternalPrefix {
		return nil, "", source.ErrInvalidRequest
	}
	i := 1
	for i < len(parts) && len(parts[i]) == 64 {
		if _, err := hex.DecodeString(
			parts[i],
		); err != nil ||
			strings.ToLower(parts[i]) != parts[i] {
			return nil, "", source.ErrInvalidRequest
		}
		i++
	}
	if i < 2 || i-1 > p2.MaxDepth || i == len(parts) {
		return nil, "", source.ErrInvalidRequest
	}
	resource := strings.Join(parts[i:], "/")
	if p2.Classify(resource) == p2.Invalid {
		return nil, "", source.ErrInvalidRequest
	}
	return parts[1:i], resource, nil
}

func (s artifactoryService) p2ResolveExternal(r *http.Request, c repo.Config, cached bool,
	name string, p auth.Principal, acc access,
) (*url.URL, error) {
	tokens, resource, err := p2ExternalParts(name)
	if err != nil {
		return nil, err
	}
	base, err := p2RootBase(c)
	if err != nil {
		return nil, source.ErrInvalidRequest
	}
	parentDir := ""
	visited := map[string]bool{p2CycleKey(base): true}
	for depth, token := range tokens {
		var child *url.URL
		var lastErr error = source.ErrInvalidRequest
		for _, filename := range p2CompositeCandidates(resource) {
			parentName := filename
			if parentDir != "" {
				parentName = parentDir + "/" + filename
			}
			var target *url.URL
			archiveEntry := ""
			if parentDir != "" {
				target, archiveEntry, err = p2ChildResourceURL(base, filename)
				if err != nil {
					return nil, source.ErrInvalidRequest
				}
			}
			parent, err := s.p2Read(r, c, cached, parentName, target, archiveEntry, p, acc)
			if errors.Is(err, source.ErrDenied) {
				return nil, err
			}
			if err != nil {
				lastErr = err
				continue
			}
			metadataBase := parent.base
			if metadataBase == nil || metadataBase.Hostname() == "" {
				metadataBase = base.ResolveReference(&url.URL{Path: filename})
			}
			_, err = p2.EditComposite(filename, parent.body, func(raw string) (string, error) {
				u, err := p2ChildURL(c, metadataBase, raw)
				if err != nil {
					return "", err
				}
				if p2ChildDigest(parent.id, parentDir, u.String()) == token {
					child = u
				}
				return p2ExternalPrefix + "/" + p2ChildDigest(parent.id, parentDir, u.String()), nil
			})
			if err != nil {
				return nil, source.ErrInvalidResult
			}
			if child != nil {
				break
			}
		}
		if child == nil {
			return nil, lastErr
		}
		key := p2CycleKey(child)
		if visited[key] || depth+1 > p2.MaxDepth {
			return nil, source.ErrInvalidResult
		}
		visited[key] = true
		base = child
		if parentDir == "" {
			parentDir = p2ExternalPrefix + "/" + token
		} else {
			parentDir += "/" + token
		}
	}
	return base, nil
}

func (s artifactoryService) serveP2CompositeChild(w http.ResponseWriter, r *http.Request,
	c repo.Config, cached bool, req itemRequest, p auth.Principal, acc access,
) {
	if !acc.file(c, req.path, types.ActionRead) {
		writeErr(w, r, denied(p))
		return
	}
	var target *url.URL
	var err error
	name := req.path
	if strings.HasPrefix(name, p2ExternalPrefix+"/") {
		target, err = s.p2ResolveExternal(r, c, cached, name, p, acc)
		if err != nil {
			writeErr(w, r, pkgroute.SourceError(err, p.Anonymous))
			return
		}
	}
	filename := name
	if target != nil {
		_, filename, err = p2ExternalParts(name)
		if err != nil {
			writeErr(w, r, itemNotFound())
			return
		}
	}
	var external *url.URL
	archiveEntry := ""
	if target != nil {
		var err error
		external, archiveEntry, err = p2ChildResourceURL(target, filename)
		if err != nil {
			writeErr(w, r, itemNotFound())
			return
		}
	}
	var expected types.Checksums
	if target != nil && p2.Classify(filename) == p2.Artifact {
		expected, err = s.p2ArtifactChecksums(r, c, cached, name, target, filename, p, acc)
		if err != nil {
			writeErr(w, r, pkgroute.SourceError(err, p.Anonymous))
			return
		}
	}
	result, err := s.p2Read(r, c, cached, name, external, archiveEntry, p, acc, expected)
	if err != nil {
		writeErr(w, r, pkgroute.SourceError(err, p.Anonymous))
		return
	}
	body := result.body
	if p2CompositeName(filename) {
		metadataBase := result.base
		if metadataBase == nil || metadataBase.Hostname() == "" {
			if external != nil {
				metadataBase = external
			} else {
				metadataBase, _ = p2RootBase(c)
				metadataBase = metadataBase.ResolveReference(&url.URL{Path: filename})
			}
		}
		parentDir := strings.TrimSuffix(name, "/"+filename)
		if parentDir == name {
			parentDir = ""
		}
		body, err = p2.EditComposite(filename, body, func(raw string) (string, error) {
			child, err := p2ChildURL(c, metadataBase, raw)
			if err != nil {
				return "", err
			}
			depth := 0
			if parentDir != "" {
				depth = strings.Count(parentDir, "/")
			}
			if depth >= p2.MaxDepth {
				return "", p2.ErrInvalid
			}
			digest := p2ChildDigest(result.id, parentDir, child.String())
			if parentDir != "" {
				return digest, nil
			}
			return p2ExternalPrefix + "/" + digest, nil
		})
		if err != nil {
			writeErr(w, r, pkgroute.SourceError(source.ErrInvalidResult, p.Anonymous))
			return
		}
	}
	w.Header().Set("Cache-Control", "private, no-store")
	if p2CompositeName(filename) {
		w.Header().Set("Content-Type", "application/octet-stream")
	}
	sw := &statusWriter{ResponseWriter: w}
	http.ServeContent(sw, r, filename, time.Time{}, bytes.NewReader(body))
	if sw.complete(r) && result.used != nil {
		_ = result.used(r.Context())
	}
}
