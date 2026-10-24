package swift

import (
	"archive/zip"
	"context"
	"errors"
	"fmt"
	"io"
	"os"
	"path"
	"regexp"
	"strings"
)

const (
	MaxArchiveBytes = 512 << 20
	maxFiles        = 100_000
	maxExpanded     = 1 << 30
	maxManifest     = 1 << 20
)

var (
	errArchive   = errors.New("invalid Swift source archive")
	manifestName = regexp.MustCompile(`^Package@swift-(\d+)(?:\.(\d+))?(?:\.(\d+))?\.swift$`)
	toolsVersion = regexp.MustCompile(
		`(?m)^//[ \t]*swift-tools-version:[ \t]*(\d+(?:\.\d+){1,2})[ \t]*$`,
	)
)

// Archive describes a completely validated source ZIP. Manifests are kept
// bounded in memory; all other members are read to EOF so CRC, size and
// truncated-stream errors are checked before a release can be published.
type Archive struct {
	Manifests map[string][]byte
	Tools     map[string]string
}

func InspectArchive(ctx context.Context, body io.Reader, size int64) (Archive, error) {
	if size < 0 || size > MaxArchiveBytes {
		return Archive{}, fmt.Errorf("%w: compressed size limit", errArchive)
	}
	tmp, err := os.CreateTemp("", "artifactweave-swift-*.zip")
	if err != nil {
		return Archive{}, fmt.Errorf("swift: temporary archive: %w", err)
	}
	defer func() {
		_ = tmp.Close()
		_ = os.Remove(tmp.Name())
	}()
	n, err := io.Copy(tmp, io.LimitReader(body, MaxArchiveBytes+1))
	if err != nil {
		return Archive{}, fmt.Errorf("swift: read source archive: %w", err)
	}
	if n <= 0 || n > MaxArchiveBytes || size > 0 && n != size {
		return Archive{}, fmt.Errorf("%w: compressed size mismatch", errArchive)
	}
	z, err := zip.NewReader(tmp, n)
	if err != nil || len(z.File) == 0 || len(z.File) > maxFiles {
		return Archive{}, fmt.Errorf("%w: ZIP directory", errArchive)
	}
	a := Archive{Manifests: map[string][]byte{}, Tools: map[string]string{}}
	// SwiftPM's archive-source and package-registry publish commands place a
	// single package directory above Package.swift. Some registries also accept
	// a flat ZIP; both layouts must resolve to exactly one package root.
	prefix := ""
	rootCount := 0
	for _, f := range z.File {
		if f.Name == "Package.swift" {
			rootCount++
			continue
		}
		if strings.HasSuffix(f.Name, "/Package.swift") && strings.Count(f.Name, "/") == 1 {
			prefix = strings.TrimSuffix(f.Name, "Package.swift")
			rootCount++
		}
	}
	if rootCount != 1 {
		return Archive{}, fmt.Errorf("%w: ambiguous package root", errArchive)
	}
	seen := map[string]bool{}
	var expanded int64
	for _, f := range z.File {
		if err := ctx.Err(); err != nil {
			return Archive{}, fmt.Errorf("swift: archive inspection: %w", err)
		}
		name := f.Name
		if name == "" || name == "." || name == ".." || strings.HasPrefix(name, "/") ||
			strings.ContainsAny(name, "\\\x00") ||
			path.Clean(name) != strings.TrimSuffix(name, "/") ||
			strings.HasPrefix(name, "../") || strings.Contains(name, "/../") || seen[name] {
			return Archive{}, fmt.Errorf("%w: unsafe or duplicate path", errArchive)
		}
		seen[name] = true
		if prefix != "" && name != prefix && !strings.HasPrefix(name, prefix) {
			return Archive{}, fmt.Errorf("%w: multiple package roots", errArchive)
		}
		mode := f.Mode()
		if !mode.IsRegular() && !mode.IsDir() || f.Flags&0x1 != 0 {
			return Archive{}, fmt.Errorf("%w: unsupported entry", errArchive)
		}
		if mode.IsDir() {
			continue
		}
		if f.UncompressedSize64 > maxExpanded ||
			expanded+int64(f.UncompressedSize64) > maxExpanded {
			return Archive{}, fmt.Errorf("%w: expanded size limit", errArchive)
		}
		rel := strings.TrimPrefix(name, prefix)
		isManifest := rel == "Package.swift" || manifestName.MatchString(rel)
		if isManifest && f.UncompressedSize64 > maxManifest {
			return Archive{}, fmt.Errorf("%w: manifest too large", errArchive)
		}
		rc, err := f.Open()
		if err != nil {
			return Archive{}, fmt.Errorf("%w: open ZIP member", errArchive)
		}
		var data []byte
		var read int64
		if isManifest {
			data, err = io.ReadAll(io.LimitReader(rc, maxManifest+1))
			read = int64(len(data))
			if len(data) > maxManifest {
				err = errArchive
			}
		} else {
			read, err = io.Copy(io.Discard, io.LimitReader(rc, maxExpanded-expanded+1))
		}
		closeErr := rc.Close()
		if err != nil || closeErr != nil || read != int64(f.UncompressedSize64) {
			return Archive{}, fmt.Errorf("%w: corrupt ZIP member", errArchive)
		}
		expanded += read
		if isManifest {
			if !strings.Contains(string(data), "import PackageDescription") {
				return Archive{}, fmt.Errorf("%w: invalid manifest", errArchive)
			}
			match := toolsVersion.FindSubmatch(data)
			if len(match) == 0 {
				return Archive{}, fmt.Errorf("%w: missing Swift tools version", errArchive)
			}
			a.Manifests[rel] = data
			a.Tools[rel] = string(match[1])
		}
	}
	if len(a.Manifests["Package.swift"]) == 0 {
		return Archive{}, fmt.Errorf("%w: missing Package.swift", errArchive)
	}
	return a, nil
}
