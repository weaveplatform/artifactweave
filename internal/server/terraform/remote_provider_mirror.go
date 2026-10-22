package terraform

import (
	"context"
	"crypto/sha256"
	"encoding/base64"
	"encoding/hex"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"slices"
	"strings"

	"github.com/weaveplatform/artifactweave/internal/archive"
	tf "github.com/weaveplatform/artifactweave/internal/format/terraform"
	"github.com/weaveplatform/artifactweave/internal/server/pkgroute"
	"github.com/weaveplatform/artifactweave/internal/source"
	"github.com/weaveplatform/artifactweave/pkg/repo"
	"github.com/weaveplatform/artifactweave/pkg/types"
)

type remoteMirrorArchive struct {
	URL    string   `json:"url"`
	Hashes []string `json:"hashes,omitempty"`
}

func parseMirrorVersions(body io.Reader, q providerOp) ([]string, error) {
	var doc struct {
		Versions map[string]json.RawMessage `json:"versions"`
	}
	if providerJSON(body, &doc) != nil || doc.Versions == nil ||
		len(doc.Versions) > moduleVersionLimit {
		return nil, remoteProviderError("invalid mirror version list")
	}
	out := make([]string, 0, len(doc.Versions))
	for version, raw := range doc.Versions {
		q.version, q.os, q.arch = version, "linux", "amd64"
		var entry map[string]json.RawMessage
		if !validRemoteProvider(q) || json.Unmarshal(raw, &entry) != nil || entry == nil {
			return nil, remoteProviderError("invalid mirror version")
		}
		out = append(out, version)
	}
	slices.Sort(out)
	return out, nil
}

func parseMirrorArchives(
	body io.Reader,
	base string,
	c repo.Config,
	q providerOp,
) (map[string]remoteMirrorArchive, error) {
	var doc struct {
		Archives map[string]remoteMirrorArchive `json:"archives"`
	}
	if providerJSON(body, &doc) != nil || doc.Archives == nil || len(doc.Archives) > 256 {
		return nil, remoteProviderError("invalid mirror archives")
	}
	for platform, entry := range doc.Archives {
		q.os, q.arch, _ = strings.Cut(platform, "_")
		if !validRemoteProvider(q) || len(entry.Hashes) > 64 {
			return nil, remoteProviderError("invalid mirror platform or hashes")
		}
		supported := false
		for _, hash := range entry.Hashes {
			algorithm, value, ok := strings.Cut(hash, ":")
			if !ok || algorithm == "" || value == "" || len(hash) > 256 {
				return nil, remoteProviderError("invalid mirror hash")
			}
			switch algorithm {
			case "h1":
				b, e := base64.StdEncoding.DecodeString(value)
				if e != nil || len(b) != 32 {
					return nil, remoteProviderError("invalid mirror h1 hash")
				}
				supported = true
			case "zh":
				b, e := hex.DecodeString(value)
				if e != nil || len(b) != 32 {
					return nil, remoteProviderError("invalid mirror ZIP hash")
				}
				supported = true
			}
		}
		if len(entry.Hashes) > 0 && !supported {
			return nil, remoteProviderError("mirror has no verifiable hash algorithm")
		}
		target, err := moduleURL(base, entry.URL)
		if err != nil {
			return nil, remoteProviderError("invalid mirror archive URL")
		}
		if err = allowProviderURL(c, target); err != nil {
			return nil, err
		}
		entry.URL = target
		doc.Archives[platform] = entry
	}
	return doc.Archives, nil
}

func (h handler) upstreamMirrorMetadata(
	r *http.Request,
	q providerOp,
	list bool,
) (source.Result, error) {
	c := pkgroute.RequestFrom(r).Repo
	base := strings.TrimRight(c.TerraformRegistryURL, "/") + "/providers/"
	suffix := escapeModulePath(q.host+"/"+q.name) + "/" + q.version + ".json"
	if list {
		suffix = escapeModulePath(q.host+"/"+q.name) + "/index.json"
	}
	return h.providerMetadata(
		r,
		"provider-mirror-metadata/"+suffix,
		base+suffix,
		providerFolder(q),
		"terraform-provider-mirror-v1",
		func(body io.Reader, actual string) error {
			if list {
				_, err := parseMirrorVersions(body, q)
				return err
			}
			_, err := parseMirrorArchives(body, actual, c, q)
			return err
		},
	)
}

func (h handler) mirrorArchives(
	r *http.Request,
	q providerOp,
) (map[string]remoteMirrorArchive, source.Result, string, error) {
	result, err := h.upstreamMirrorMetadata(r, q, false)
	if err != nil {
		return nil, result, "", err
	}
	defer closeModuleResult(result)
	if result.Stream == nil {
		return nil, result, "", remoteProviderError("missing mirror bytes")
	}
	archives, err := parseMirrorArchives(
		result.Stream.Body,
		result.Metadata.BaseURL,
		pkgroute.RequestFrom(r).Repo,
		q,
	)
	digest := moduleDigest(result)
	result.Stream = nil
	return archives, result, digest, err
}

func (h handler) providerMirrorBundle(r *http.Request, q providerOp) (providerBundle, error) {
	archives, result, digest, err := h.mirrorArchives(r, q)
	b := providerBundle{
		q:       q,
		digest:  digest,
		results: []source.Result{result},
		paths:   []string{providerArchivePath(q)},
	}
	if err != nil {
		return b, err
	}
	entry, ok := archives[q.os+"_"+q.arch]
	if !ok {
		return b, missing()
	}
	resource := pkgroute.Resource(r, b.paths[0], "")
	resource.Method = http.MethodGet
	resource.Query = ""
	resource.Accept = "application/octet-stream"
	resource.ExternalURL = entry.URL
	descriptor, _ := json.Marshal(entry)
	resource.Variant = "terraform-mirror-archive-v2:" + string(descriptor)
	resource.CheckURL = providerURLGuard(pkgroute.RequestFrom(r).Repo, entry.URL)
	resource.Validate = func(ctx context.Context, body io.ReadSeeker, size int64) error {
		if e := (moduleSource{Archive: "zip"}).validateArchive(ctx, body, size); e != nil {
			return e
		}
		return verifyMirrorArchive(ctx, body, size, entry.Hashes)
	}
	artifact, err := h.k.Read(r, resource)
	if err != nil {
		return b, providerReadError(err, "mirror archive could not be retrieved or validated; check its strongest advertised hash, ZIP contents and allowed destination")
	}
	b.results = append(b.results, artifact)
	b.metadata.Shasum = moduleDigest(artifact)
	if len(entry.Hashes) == 0 {
		var reader io.ReadSeeker
		var size int64
		if artifact.Item != nil {
			opened, info, e := h.k.Blobs().Open(r.Context(), artifact.Item.Actual.SHA256)
			if e != nil {
				return b, fmt.Errorf("terraform mirror archive: %w", e)
			}
			defer opened.Close()
			reader, size = opened, info.Size
		} else {
			reader, ok = artifact.Stream.Body.(io.ReadSeeker)
			if !ok {
				return b, remoteProviderError("unreadable mirror archive")
			}
			size = artifact.Stream.Size
		}
		hash, valid, e := tf.H1(
			&archiveReaderAt{ctx: r.Context(), r: archive.ReaderAt(reader)},
			size,
		)
		if e != nil || !valid {
			return b, remoteProviderError("invalid mirror archive content hash")
		}
		b.h1 = hash
		if _, e = reader.Seek(0, io.SeekStart); e != nil {
			return b, fmt.Errorf("terraform mirror rewind: %w", e)
		}
	}
	return b, nil
}

func verifyMirrorArchive(
	ctx context.Context,
	body io.ReadSeeker,
	size int64,
	hashes []string,
) error {
	if len(hashes) == 0 {
		return nil
	}
	h1 := []string{}
	zh := []string{}
	for _, hash := range hashes {
		if strings.HasPrefix(hash, "h1:") {
			h1 = append(h1, hash)
		} else if strings.HasPrefix(hash, "zh:") {
			zh = append(zh, strings.ToLower(hash))
		}
	}
	if len(h1) > 0 {
		hash, ok, err := tf.H1(&archiveReaderAt{ctx: ctx, r: archive.ReaderAt(body)}, size)
		if err != nil || !ok || !slices.Contains(h1, hash) {
			return remoteProviderError("mirror archive does not match its strongest content hash")
		}
		return nil
	}
	if _, err := body.Seek(0, io.SeekStart); err != nil {
		return remoteProviderError("unreadable mirror archive")
	}
	digest := sha256.New()
	left := size
	if _, err := io.Copy(digest, archiveReader{ctx: ctx, r: body, remaining: &left}); err != nil {
		return remoteProviderError("unreadable mirror archive")
	}
	if !slices.Contains(zh, "zh:"+hex.EncodeToString(digest.Sum(nil))) {
		return remoteProviderError("mirror archive does not match its ZIP hash")
	}
	return nil
}

func (h handler) remoteProviderMirrorUpstream(
	w http.ResponseWriter,
	r *http.Request,
	q providerOp,
) {
	index, err := h.upstreamMirrorMetadata(r, q, true)
	if err != nil {
		h.k.Error(w, r, err)
		return
	}
	if index.Stream == nil {
		h.k.Error(w, r, remoteProviderError("missing mirror index"))
		return
	}
	versions, err := parseMirrorVersions(index.Stream.Body, q)
	closeModuleResult(index)
	index.Stream = nil
	if err != nil {
		h.k.Error(w, r, err)
		return
	}
	if !q.list && !slices.Contains(versions, q.version) {
		h.k.Error(w, r, missing())
		return
	}
	if !q.list {
		h.remoteMirrorArchives(w, r, q, index)
		return
	}
	if len(versions) > moduleDiscoveryLimit {
		h.k.Error(w, r, remoteProviderError("mirror version discovery exceeds its bound"))
		return
	}
	out := map[string]any{}
	results := []source.Result{index}
	paths := []string{}
	for _, version := range versions {
		q.version = version
		archives, result, _, e := h.mirrorArchives(r, q)
		if e != nil {
			h.k.Error(w, r, e)
			return
		}
		for platform := range archives {
			q.os, q.arch, _ = strings.Cut(platform, "_")
			path := providerArchivePath(q)
			if h.k.Can(r, path, types.ActionRead) {
				out[version] = struct{}{}
				paths = append(paths, path)
				results = append(results, result)
			}
		}
	}
	if len(out) == 0 {
		h.k.Error(w, r, missing())
		return
	}
	h.serveProviderJSON(w, r, map[string]any{"versions": out}, results, paths)
}

func (h handler) remoteMirrorArchives(
	w http.ResponseWriter,
	r *http.Request,
	q providerOp,
	index source.Result,
) {
	archives, metadata, _, err := h.mirrorArchives(r, q)
	if err != nil {
		h.k.Error(w, r, err)
		return
	}
	out := map[string]remoteMirrorArchive{}
	results := []source.Result{index, metadata}
	paths := []string{}
	for platform, entry := range archives {
		q.os, q.arch, _ = strings.Cut(platform, "_")
		if !h.k.Can(r, providerArchivePath(q), types.ActionRead) {
			continue
		}
		bundle, e := h.providerMirrorBundle(r, q)
		defer bundle.close()
		if e != nil {
			h.k.Error(w, r, e)
			return
		}
		entry.URL, e = h.remoteProviderURL(r, bundle, 0)
		if e != nil {
			h.k.Error(w, r, e)
			return
		}
		if len(entry.Hashes) == 0 {
			entry.Hashes = []string{bundle.h1}
		}
		out[platform] = entry
		results = append(results, bundle.results...)
		paths = append(paths, bundle.paths...)
	}
	if len(out) == 0 {
		h.k.Error(w, r, missing())
		return
	}
	h.serveProviderJSON(w, r, map[string]any{"archives": out}, results, paths)
}
