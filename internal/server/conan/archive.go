package conan

import (
	"archive/tar"
	"compress/gzip"
	"crypto/md5" //nolint:gosec // Conan manifest wire format uses MD5.
	"encoding/hex"
	"fmt"
	"io"
	"path"
	"strings"

	conanfmt "github.com/weaveplatform/artifactweave/internal/format/conan"
	"github.com/weaveplatform/artifactweave/internal/server/apierror"
)

// archiveDigests verifies the files Conan put inside a package/export archive.
// The manifest hashes the unpacked files, not the .tgz transport archive.
func archiveDigests(rc io.Reader) (map[string]string, error) {
	gz, err := gzip.NewReader(rc)
	if err != nil {
		return nil, apierror.New(422, "Invalid Conan archive")
	}
	defer func() { _ = gz.Close() }()
	tr := tar.NewReader(gz)
	out := map[string]string{}
	var total int64
	for i := 0; ; i++ {
		if i >= 10000 {
			return nil, apierror.New(422, "Conan archive has too many entries")
		}
		h, err := tr.Next()
		if err == io.EOF {
			break
		}
		if err != nil {
			return nil, apierror.New(422, "Invalid Conan archive")
		}
		name := strings.TrimPrefix(h.Name, "./")
		if h.Typeflag == tar.TypeDir {
			name = strings.TrimSuffix(name, "/")
		}
		if name == "" && h.Typeflag == tar.TypeDir {
			continue
		}
		if path.Clean(name) != name || !conanfmt.SafeFile(name) {
			return nil, apierror.New(422, "Unsafe Conan archive path")
		}
		if h.Typeflag == tar.TypeDir {
			continue
		}
		if h.Typeflag == tar.TypeSymlink {
			//nolint:gosec // The normalized target is checked for traversal before admission.
			target := path.Clean(path.Join(path.Dir(name), h.Linkname))
			if path.IsAbs(h.Linkname) || target == ".." || strings.HasPrefix(target, "../") {
				return nil, apierror.New(422, "Unsafe Conan archive symlink")
			}
			if _, exists := out[name]; exists {
				return nil, apierror.New(422, "Duplicate Conan archive entry")
			}
			//nolint:gosec // Conan manifest wire format uses MD5.
			sum := md5.Sum([]byte(h.Linkname))
			out[name] = hex.EncodeToString(sum[:])
			continue
		}
		if h.Typeflag != tar.TypeReg {
			return nil, apierror.New(422, "Unsupported Conan archive entry")
		}
		if h.Size < 0 || h.Size > maxUpload || total+h.Size > maxUpload {
			return nil, apierror.New(422, "Conan archive exceeds limit")
		}
		if _, exists := out[name]; exists {
			return nil, apierror.New(422, "Duplicate Conan archive entry")
		}
		hash := md5.New() //nolint:gosec // Conan manifest wire format uses MD5.
		if _, err := io.CopyN(hash, tr, h.Size); err != nil {
			return nil, fmt.Errorf("read conan archive: %w", err)
		}
		out[name] = hex.EncodeToString(hash.Sum(nil))
		total += h.Size
	}
	return out, nil
}
