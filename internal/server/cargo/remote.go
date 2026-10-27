package cargo

import (
	"bytes"
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"net/url"
	"strings"

	"github.com/weaveplatform/artifactweave/internal/format/cargo"
	"github.com/weaveplatform/artifactweave/internal/source"
	"github.com/weaveplatform/artifactweave/pkg/types"
)

type upstreamConfig struct {
	Download string `json:"dl"`
}

func parseConfig(raw []byte) (upstreamConfig, error) {
	var c upstreamConfig
	if len(raw) == 0 || len(raw) > cargo.MaxIndex || json.Unmarshal(raw, &c) != nil {
		return c, source.ErrInvalidResult
	}
	u, err := url.Parse(c.Download)
	if err != nil || (u.Scheme != "http" && u.Scheme != "https") || u.Host == "" || u.User != nil ||
		u.Fragment != "" {
		return c, source.ErrInvalidResult
	}
	return c, nil
}

func parseIndex(raw []byte, name string) ([]cargo.Record, error) {
	if len(raw) == 0 || len(raw) > cargo.MaxIndex || raw[len(raw)-1] != '\n' {
		return nil, source.ErrInvalidResult
	}
	lines := bytes.Split(bytes.TrimSuffix(raw, []byte{'\n'}), []byte{'\n'})
	if len(lines) > 10000 {
		return nil, source.ErrInvalidResult
	}
	out := make([]cargo.Record, 0, len(lines))
	seen := map[string]bool{}
	for _, line := range lines {
		var rec cargo.Record
		if len(line) == 0 || len(line) > 1<<20 || json.Unmarshal(line, &rec) != nil ||
			!rec.IsValid() ||
			!strings.EqualFold(rec.Name, name) ||
			seen[rec.Version] {
			return nil, source.ErrInvalidResult
		}
		seen[rec.Version] = true
		out = append(out, rec)
	}
	return out, nil
}

func (h handler) remoteDocument(r *http.Request, path, name string) ([]byte, source.Result, error) {
	upstream := strings.TrimPrefix(path, "index/")
	res := source.Resource{
		Kind:             source.ResourceKindMetadata,
		Path:             path,
		UpstreamPath:     upstream,
		Method:           http.MethodGet,
		PermissionFolder: true,
		PermissionPath:   "crates/" + name,
		Variant:          "cargo-sparse-v1",
	}
	if name == "" {
		res.PermissionPath = ""
	}
	res.ValidateMetadata = func(_ context.Context, body io.ReadSeeker, size int64, _ source.MetadataResponse) error {
		if size <= 0 || size > cargo.MaxIndex {
			return source.ErrInvalidResult
		}
		raw, err := io.ReadAll(io.LimitReader(body, cargo.MaxIndex+1))
		if err != nil || int64(len(raw)) != size {
			return source.ErrInvalidResult
		}
		if name == "" {
			_, err = parseConfig(raw)
		} else {
			_, err = parseIndex(raw, name)
		}
		return err
	}
	result, err := h.k.Read(r, res)
	if err != nil {
		return nil, result, fmt.Errorf("cargo: read sparse metadata: %w", err)
	}
	var raw []byte
	switch {
	case result.Stream != nil:
		defer result.Stream.Body.Close()
		raw, err = io.ReadAll(io.LimitReader(result.Stream.Body, cargo.MaxIndex+1))
	case result.Item != nil:
		var rc io.ReadSeekCloser
		rc, _, err = h.k.Blobs().Open(r.Context(), result.Item.Actual.SHA256)
		if err == nil {
			raw, err = io.ReadAll(io.LimitReader(rc, cargo.MaxIndex+1))
			_ = rc.Close()
		}
	default:
		return nil, result, source.ErrInvalidResult
	}
	if err != nil || len(raw) > cargo.MaxIndex {
		return nil, result, source.ErrInvalidResult
	}
	if name == "" {
		_, err = parseConfig(raw)
	} else {
		_, err = parseIndex(raw, name)
	}
	if err != nil {
		return nil, result, err
	}
	result.Stream = nil
	result.Item = nil
	return raw, result, nil
}

type bytesBody struct{ *bytes.Reader }

func (*bytesBody) Close() error { return nil }

func (h handler) serveRemoteBytes(
	w http.ResponseWriter,
	r *http.Request,
	data []byte,
	result source.Result,
	media string,
) {
	digest := sha256.Sum256(data)
	result.Stream = &source.Stream{
		Status: http.StatusOK,
		Body:   &bytesBody{bytes.NewReader(data)},
		Representation: source.Representation{
			Size: int64(
				len(data),
			),
			MediaType: media,
			ETag:      fmt.Sprintf(`"%x"`, digest),
			Checksums: types.Checksums{SHA256: hex.EncodeToString(digest[:])},
		},
	}
	h.k.ServeResult(w, r, result)
}

func downloadURL(base, name, version string) (string, error) {
	if !cargo.ValidName(name) || !cargo.ValidVersion(version) {
		return "", source.ErrInvalidRequest
	}
	u, err := url.Parse(base)
	if err != nil {
		return "", source.ErrInvalidResult
	}
	if !strings.Contains(base, "{crate}") && !strings.Contains(base, "{version}") &&
		!strings.Contains(base, "{prefix}") &&
		!strings.Contains(base, "{lowerprefix}") {
		u.Path = strings.TrimRight(u.Path, "/") + "/" + name + "/" + version + "/download"
		return u.String(), nil
	}
	prefix := strings.TrimSuffix(cargo.IndexPath(name), "/"+strings.ToLower(name))
	r := strings.NewReplacer(
		"{crate}",
		url.PathEscape(name),
		"{version}",
		url.PathEscape(version),
		"{prefix}",
		prefix,
		"{lowerprefix}",
		strings.ToLower(prefix),
	)
	return r.Replace(base), nil
}

func (h handler) remoteCrate(w http.ResponseWriter, r *http.Request, name, version, path string) {
	if !h.k.Require(w, r, path, types.ActionRead) {
		return
	}
	configRaw, configResult, err := h.remoteDocument(r, "index/config.json", "")
	if err != nil {
		h.replyError(w, r, err)
		return
	}
	cfg, err := parseConfig(configRaw)
	if err != nil {
		h.replyError(w, r, err)
		return
	}
	indexRaw, indexResult, err := h.remoteDocument(
		r,
		"index/"+cargo.IndexPath(name),
		strings.ToLower(name),
	)
	if err != nil {
		h.replyError(w, r, err)
		return
	}
	records, err := parseIndex(indexRaw, strings.ToLower(name))
	if err != nil {
		h.replyError(w, r, err)
		return
	}
	var selected *cargo.Record
	for i := range records {
		if records[i].Version == version && strings.EqualFold(records[i].Name, name) {
			selected = &records[i]
			break
		}
	}
	if selected == nil {
		h.replyError(w, r, failure(http.StatusNotFound, "Cargo version not found"))
		return
	}
	url, err := downloadURL(cfg.Download, name, version)
	if err != nil {
		h.replyError(w, r, err)
		return
	}
	res := source.Resource{
		Kind:        source.ResourceKindArtifact,
		Path:        path,
		Method:      http.MethodGet,
		ExternalURL: url,
		Variant:     "cargo-crate-v1:" + selected.Checksum,
		Checksums:   types.Checksums{SHA256: selected.Checksum},
	}
	res.Validate = func(ctx context.Context, body io.ReadSeeker, size int64) error {
		if size <= 0 || size > cargo.MaxCrate {
			return source.ErrInvalidResult
		}
		if _, _, err := cargo.Inspect(ctx, body, size, path); err != nil {
			return source.ErrInvalidResult
		}
		if _, err := body.Seek(0, io.SeekStart); err != nil {
			return source.ErrInvalidResult
		}
		hash := sha256.New()
		if _, err := io.Copy(
			hash,
			body,
		); err != nil ||
			hex.EncodeToString(hash.Sum(nil)) != selected.Checksum {
			return source.ErrInvalidResult
		}
		return nil
	}
	result, err := h.k.Read(r, res)
	if err != nil {
		h.replyError(w, r, err)
		return
	}
	prior := result.Revalidate
	result.Revalidate = func(ctx context.Context) error {
		for _, fn := range []func(context.Context) error{prior, configResult.Revalidate, indexResult.Revalidate} {
			if fn != nil {
				if err := fn(ctx); err != nil {
					return err
				}
			}
		}
		if indexResult.CheckRead != nil {
			return indexResult.CheckRead(ctx, path)
		}
		return nil
	}
	used := result.Used
	result.Used = func(ctx context.Context) error {
		for _, fn := range []func(context.Context) error{used, configResult.Used, indexResult.Used} {
			if fn != nil {
				if err := fn(ctx); err != nil {
					return err
				}
			}
		}
		return nil
	}
	h.k.ServeResult(w, r, result)
}
