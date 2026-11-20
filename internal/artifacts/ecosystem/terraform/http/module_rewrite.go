package terraformhttp

import (
	"archive/tar"
	"archive/zip"
	"bytes"
	"compress/bzip2"
	"compress/gzip"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/http"
	"net/url"
	"os"
	"path"
	"slices"
	"strings"

	"github.com/hashicorp/hcl/v2"
	"github.com/hashicorp/hcl/v2/hclsyntax"
	"github.com/zclconf/go-cty/cty"

	"github.com/weaveplatform/artifactweave/internal/artifacts/adapters/http/pkgroute"
	"github.com/weaveplatform/artifactweave/internal/artifacts/core/remote/source"
	tf "github.com/weaveplatform/artifactweave/internal/artifacts/ecosystem/terraform"
	"github.com/weaveplatform/artifactweave/internal/kernel"
	"github.com/weaveplatform/artifactweave/internal/platform/archive"
	"github.com/weaveplatform/artifactweave/internal/platform/blob"
	"github.com/weaveplatform/artifactweave/internal/platform/httpkit/baseurl"
	"github.com/weaveplatform/artifactweave/internal/platform/xzfile"
)

// rewriteTarget is signed: replaying a URL under another proxy host cannot
// change the delivered configuration or its validators.
type rewriteTarget struct {
	Host string `json:"host,omitempty"`
	Repo string `json:"repo,omitempty"`
}

func moduleRewriteTarget(r *http.Request) rewriteTarget {
	u, _ := url.Parse(baseurl.From(r.Context()))
	return rewriteTarget{Host: u.Host, Repo: pkgroute.RequestFrom(r).Repo.Key}
}

func (t rewriteTarget) address(raw string) string {
	base, selector, _ := strings.Cut(raw, "//")
	originalHost := ""
	parts := strings.Split(base, "/")
	if len(parts) == 4 {
		// A registry hostname has no URL scheme. Git/HTTP/local source syntax
		// consequently never passes the registry module grammar below.
		if !strings.ContainsAny(parts[0], ".:") && parts[0] != "localhost" {
			return raw
		}
		originalHost = parts[0]
		parts = parts[1:]
	}
	if len(parts) != 3 || t.Host == "" || t.Repo == "" {
		return raw
	}
	m, ok := tf.ParseModule(strings.Join(parts, "/") + "/0.0.0.zip")
	if !ok {
		return raw
	}
	// Avoid adding a second repository prefix to an already routed address.
	ns := m.Namespace
	if originalHost == t.Host && strings.HasPrefix(ns, t.Repo+"__") {
		return raw
	}
	encoded := t.Repo + "__" + ns
	if _, ok := tf.ParseModule(encoded + "/" + m.Name + "/" + m.System + "/0.0.0.zip"); !ok {
		return raw
	}
	out := t.Host + "/" + encoded + "/" + m.Name + "/" + m.System
	if selector != "" {
		out += "//" + selector
	}
	return out
}

type sourceEdit struct {
	start, end int
	text       string
}

func rewriteModuleFile(name string, body []byte, target rewriteTarget) ([]byte, bool) {
	if strings.HasSuffix(name, ".tf.json") {
		return rewriteModuleJSON(body, target)
	}
	if !strings.HasSuffix(name, ".tf") {
		return body, false
	}
	file, diags := hclsyntax.ParseConfig(body, name, hcl.InitialPos)
	if diags.HasErrors() {
		return body, false
	}
	root, ok := file.Body.(*hclsyntax.Body)
	if !ok {
		return body, false
	}
	var edits []sourceEdit
	for _, block := range root.Blocks {
		if block.Type != "module" {
			continue
		}
		attr := block.Body.Attributes["source"]
		if attr == nil {
			continue
		}
		value, diags := attr.Expr.Value(nil)
		if diags.HasErrors() || !value.IsKnown() || value.IsNull() || value.Type() != cty.String {
			continue
		}
		next := target.address(value.AsString())
		if next == value.AsString() {
			continue
		}
		quoted, _ := json.Marshal(next)
		span := attr.Expr.Range()
		edits = append(edits, sourceEdit{start: span.Start.Byte, end: span.End.Byte, text: string(quoted)})
	}
	if len(edits) == 0 {
		return body, false
	}
	slices.SortFunc(edits, func(a, b sourceEdit) int { return b.start - a.start })
	out := slices.Clone(body)
	for _, edit := range edits {
		out = append(append(append([]byte{}, out[:edit.start]...), edit.text...), out[edit.end:]...)
	}
	return out, true
}

func rewriteModuleJSON(body []byte, target rewriteTarget) ([]byte, bool) {
	return rewriteJSONObjects(body, func(root map[string]json.RawMessage) bool {
		modules, changed := rewriteJSONObjects(root["module"], func(modules map[string]json.RawMessage) bool {
			changed := false
			for name, raw := range modules {
				block, edited := rewriteJSONObjects(raw, func(block map[string]json.RawMessage) bool {
					var value string
					if json.Unmarshal(block["source"], &value) != nil {
						return false
					}
					next := target.address(value)
					if value == next {
						return false
					}
					block["source"], _ = json.Marshal(next)
					return true
				})
				if edited {
					modules[name] = block
					changed = true
				}
			}
			return changed
		})
		if changed {
			root["module"] = modules
		}
		return changed
	})
}

// HCL JSON permits objects and arrays of objects at each block level. Raw
// values preserve non-module numbers and expressions without reinterpretation.
func rewriteJSONObjects(body []byte, edit func(map[string]json.RawMessage) bool) ([]byte, bool) {
	var object map[string]json.RawMessage
	if json.Unmarshal(body, &object) == nil && object != nil {
		if !edit(object) {
			return body, false
		}
		out, _ := json.Marshal(object)
		return out, true
	}
	var array []json.RawMessage
	if json.Unmarshal(body, &array) != nil {
		return body, false
	}
	changed := false
	for i, raw := range array {
		out, edited := rewriteJSONObjects(raw, edit)
		if edited {
			array[i] = out
			changed = true
		}
	}
	if !changed {
		return body, false
	}
	out, _ := json.Marshal(array)
	return out, true
}

type moduleRewrite struct {
	result          source.Result
	digest, archive string
}

// tempModule owns only a derived representation, never a source-cache entry.
type tempModule struct{ *os.File }

func (t *tempModule) Close() error {
	name := t.Name()
	err := t.File.Close()
	_ = os.Remove(name)
	if err != nil {
		return fmt.Errorf("terraform derived archive close: %w", err)
	}
	return nil
}

func completeModuleResource(r *source.Resource) {
	r.Method = http.MethodGet
	r.Range = ""
	r.IfRange = ""
	r.IfNoneMatch = ""
	r.IfModifiedSince = ""
}

func (h handler) rewriteModule(r *http.Request, result source.Result, location moduleSource, target rewriteTarget) (moduleRewrite, error) {
	out := moduleRewrite{archive: location.Archive}
	var reader io.ReadSeeker
	var size int64
	switch {
	case result.Item != nil:
		opened, info, err := h.k.Blobs().Open(r.Context(), result.Item.Actual.SHA256)
		if err != nil {
			return out, fmt.Errorf("terraform rewrite archive: %w", err)
		}
		defer opened.Close()
		reader = opened
		size = info.Size
	case result.Stream != nil:
		var ok bool
		reader, ok = result.Stream.Body.(io.ReadSeeker)
		if !ok {
			return out, remoteModuleError()
		}
		size = result.Stream.Size
	default:
		return out, remoteModuleError()
	}
	defer func() { _, _ = reader.Seek(0, io.SeekStart) }()
	needed, err := moduleArchiveNeedsRewrite(r.Context(), archive.ReaderAt(reader), size, location, target)
	if err != nil {
		return out, err
	}
	if !needed {
		return out, nil
	}
	file, err := os.CreateTemp("", "terraform-module-*.archive")
	if err != nil {
		return out, fmt.Errorf("terraform rewrite temporary: %w", err)
	}
	temp := &tempModule{file}
	keep := false
	defer func() {
		if !keep {
			_ = temp.Close()
		}
	}()
	kind, changed, err := rewriteModuleArchive(r.Context(), archive.ReaderAt(reader), size, location, target, file)
	if err != nil {
		return out, err
	}
	if !changed {
		return out, nil
	}
	if _, err := file.Seek(0, io.SeekStart); err != nil {
		return out, fmt.Errorf("terraform rewrite rewind: %w", err)
	}
	hashes := blob.NewHasher()
	size, err = io.Copy(hashes, file)
	if err != nil {
		return out, fmt.Errorf("terraform rewrite checksum: %w", err)
	}
	sums := hashes.Sum()
	if _, err := file.Seek(0, io.SeekStart); err != nil {
		return out, fmt.Errorf("terraform rewrite rewind: %w", err)
	}
	out.digest = sums.SHA256
	out.archive = kind
	derived := result
	derived.Item = nil
	derived.Stream = &source.Stream{
		Body:   temp,
		Status: 200,
		Representation: source.Representation{
			Size:      size,
			Path:      "module." + kind,
			Checksums: kernel.Checksums{MD5: sums.MD5, SHA1: sums.SHA1, SHA256: sums.SHA256},
			ETag:      `"` + sums.SHA1 + `"`,
		},
	}
	out.result = derived
	keep = true
	return out, nil
}

const moduleConfigLimit = 16 << 20

func rewriteConfig(r io.Reader, name string, target rewriteTarget) ([]byte, bool, error) {
	body, err := io.ReadAll(io.LimitReader(r, moduleConfigLimit+1))
	if err != nil {
		return nil, false, remoteModuleError()
	}
	if len(body) > moduleConfigLimit {
		return nil, false, remoteModuleError()
	}
	out, changed := rewriteModuleFile(name, body, target)
	return out, changed, nil
}

func isModuleConfig(name string) bool {
	return strings.HasSuffix(name, ".tf") || strings.HasSuffix(name, ".tf.json")
}

func rewriteModuleArchive(ctx context.Context, ra io.ReaderAt, size int64, location moduleSource, target rewriteTarget, w io.Writer) (string, bool, error) {
	// Reuse complete validation even for a caller handing in pre-existing cached
	// bytes. No transform can turn an invalid link/selection graph into admission.
	if err := location.validateArchive(ctx, io.NewSectionReader(ra, 0, size), size); err != nil {
		return "", false, err
	}
	if location.Archive == "zip" {
		changed, err := rewriteModuleZip(ctx, ra, size, target, w)
		return "zip", changed, err
	}
	stream, closeReader, err := moduleArchiveReader(ctx, ra, size, location.Archive)
	if err != nil {
		return "", false, err
	}
	defer closeReader()
	gz := gzip.NewWriter(w)
	if strings.HasPrefix(location.Archive, "tar") {
		changed, err := rewriteModuleTar(stream, target, gz)
		if closeErr := gz.Close(); err == nil {
			err = closeErr
		}
		if err != nil {
			return "", false, remoteModuleError()
		}
		return "tar.gz", changed || location.Archive == "tar", nil
	}
	u, _ := url.Parse(location.Target)
	name := strings.TrimSuffix(path.Base(u.Path), "."+location.Archive)
	// A module installer asks go-getter for a directory. Its single-file
	// decompressors refuse that mode, even when the URL carries archive=gz,
	// bz2 or xz. Wrap the one decoded file in a portable package; the source
	// cache and its checksum continue to describe the original compressed file.
	var body io.Reader
	var expanded int64
	if isModuleConfig(name) {
		content, _, err := rewriteConfig(stream, name, target)
		if err != nil {
			_ = gz.Close()
			return "", false, err
		}
		body, expanded = bytes.NewReader(content), int64(len(content))
	} else {
		// TAR needs the size before its entry. Count through the existing
		// expansion/cancellation budget, then reopen without retaining the file
		// in memory or changing the allowed non-config file size.
		expanded, err = io.Copy(io.Discard, stream)
		if err != nil {
			_ = gz.Close()
			return "", false, remoteModuleError()
		}
		closeReader()
		var closeAgain func()
		body, closeAgain, err = moduleArchiveReader(ctx, ra, size, location.Archive)
		if err != nil {
			_ = gz.Close()
			return "", false, err
		}
		defer closeAgain()
	}
	tw := tar.NewWriter(gz)
	if err = tw.WriteHeader(&tar.Header{Name: name, Typeflag: tar.TypeReg, Mode: 0o600, Size: expanded}); err != nil {
		_ = tw.Close()
		_ = gz.Close()
		return "", false, remoteModuleError()
	}
	if _, err = io.Copy(tw, body); err != nil { //nolint:gosec // validated, context-aware bounded source
		_ = tw.Close()
		_ = gz.Close()
		return "", false, remoteModuleError()
	}
	if err = tw.Close(); err != nil {
		_ = gz.Close()
		return "", false, remoteModuleError()
	}
	if err = gz.Close(); err != nil {
		return "", false, remoteModuleError()
	}
	return "tar.gz", true, nil
}

func rewriteModuleZip(ctx context.Context, ra io.ReaderAt, size int64, target rewriteTarget, w io.Writer) (bool, error) {
	zr, err := zip.NewReader(ra, size)
	if err != nil {
		return false, remoteModuleError()
	}
	zw := zip.NewWriter(w)
	changed := false
	for _, entry := range zr.File {
		if err := ctx.Err(); err != nil {
			_ = zw.Close()
			return false, fmt.Errorf("terraform rewrite canceled: %w", err)
		}
		if entry.Mode().IsRegular() && isModuleConfig(entry.Name) {
			body, err := entry.Open()
			if err != nil {
				_ = zw.Close()
				return false, remoteModuleError()
			}
			content, edited, err := rewriteConfig(body, entry.Name, target)
			_ = body.Close()
			if err != nil {
				_ = zw.Close()
				return false, err
			}
			if edited {
				header := entry.FileHeader
				header.CRC32 = 0
				header.CompressedSize64 = 0
				header.UncompressedSize64 = uint64(len(content))
				dst, err := zw.CreateHeader(&header)
				if err == nil {
					_, err = dst.Write(content)
				}
				if err != nil {
					_ = zw.Close()
					return false, remoteModuleError()
				}
				changed = true
				continue
			}
		}
		if err := zw.Copy(entry); err != nil {
			_ = zw.Close()
			return false, remoteModuleError()
		}
	}
	if err := zw.Close(); err != nil {
		return false, remoteModuleError()
	}
	return changed, nil
}

func rewriteModuleTar(r io.Reader, target rewriteTarget, w io.Writer) (bool, error) {
	tr := tar.NewReader(r)
	tw := tar.NewWriter(w)
	changed := false
	for {
		header, err := tr.Next()
		if errors.Is(err, io.EOF) {
			break
		}
		if err != nil {
			_ = tw.Close()
			return false, remoteModuleError()
		}
		var body []byte
		if header.Typeflag == tar.TypeReg && isModuleConfig(header.Name) {
			var edited bool
			body, edited, err = rewriteConfig(tr, header.Name, target)
			if err != nil {
				_ = tw.Close()
				return false, err
			}
			if edited {
				changed = true
				header.Size = int64(len(body))
				delete(header.PAXRecords, "size")
			}
		}
		if err = tw.WriteHeader(header); err != nil {
			_ = tw.Close()
			return false, remoteModuleError()
		}
		if body != nil {
			_, err = tw.Write(body)
		} else {
			_, err = io.Copy(tw, tr) //nolint:gosec // bounded, context-aware reader; complete archive validated before rewriting
		}
		if err != nil {
			_ = tw.Close()
			return false, remoteModuleError()
		}
	}
	if err := tw.Close(); err != nil {
		return false, remoteModuleError()
	}
	return changed, nil
}

// Discover representation changes without creating a derived archive. Supported
// packages with only relative/Git sources retain their original representation.
func moduleArchiveNeedsRewrite(ctx context.Context, ra io.ReaderAt, size int64, location moduleSource, target rewriteTarget) (bool, error) {
	if err := location.validateArchive(ctx, io.NewSectionReader(ra, 0, size), size); err != nil {
		return false, err
	}
	// Plain TAR is parsed as vanity XML, while single-file gzip/bzip2/XZ
	// decompression refuses the installer's directory mode. These formats need
	// guarded tar.gz delivery even without edits; the cache keeps original bytes.
	if location.Archive == "tar" || location.Archive == "gz" || location.Archive == "bz2" || location.Archive == "xz" {
		return true, nil
	}
	if location.Archive == "zip" {
		zr, err := zip.NewReader(ra, size)
		if err != nil {
			return false, remoteModuleError()
		}
		for _, entry := range zr.File {
			if !entry.Mode().IsRegular() || !isModuleConfig(entry.Name) {
				continue
			}
			body, err := entry.Open()
			if err != nil {
				return false, remoteModuleError()
			}
			_, changed, err := rewriteConfig(archiveReader{ctx: ctx, r: body}, entry.Name, target)
			_ = body.Close()
			if err != nil || changed {
				return changed, err
			}
		}
		return false, nil
	}
	stream, closeReader, err := moduleArchiveReader(ctx, ra, size, location.Archive)
	if err != nil {
		return false, err
	}
	defer closeReader()
	if strings.HasPrefix(location.Archive, "tar") {
		tr := tar.NewReader(stream)
		for {
			header, err := tr.Next()
			if errors.Is(err, io.EOF) {
				return false, nil
			}
			if err != nil {
				return false, remoteModuleError()
			}
			if header.Typeflag != tar.TypeReg || !isModuleConfig(header.Name) {
				continue
			}
			_, changed, err := rewriteConfig(tr, header.Name, target)
			if err != nil || changed {
				return changed, err
			}
		}
	}
	return false, nil
}

func moduleArchiveReader(ctx context.Context, ra io.ReaderAt, size int64, kind string) (io.Reader, func(), error) {
	var stream io.Reader = io.NewSectionReader(ra, 0, size)
	closeReader := func() {}
	switch {
	case strings.HasSuffix(kind, "gz"):
		gz, err := gzip.NewReader(stream)
		if err != nil {
			return nil, closeReader, remoteModuleError()
		}
		stream = gz
		closeReader = func() { _ = gz.Close() }
	case strings.HasSuffix(kind, "bz2"):
		stream = bzip2.NewReader(stream)
	case strings.HasSuffix(kind, "xz"):
		xr, err := xzfile.NewReader(ctx, stream, remoteArchiveInflated+1)
		if err != nil {
			return nil, closeReader, remoteModuleError()
		}
		stream = xr
		closeReader = func() { _ = xr.Close() }
	}
	budget := remoteArchiveInflated
	return archiveReader{ctx: ctx, r: stream, remaining: &budget}, closeReader, nil
}
