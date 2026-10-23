package luarocks

import (
	"context"
	"errors"
	"fmt"
	"io"
	"net/http"
	"regexp"
	"strings"

	"github.com/weaveplatform/artifactweave/internal/archive"
	"github.com/weaveplatform/artifactweave/internal/format"
)

const maxRockspec = 1 << 20

var (
	rockLimits    = archive.Limits{Entries: 10000, Inflated: 256 << 20, File: 256 << 20}
	errUnsafeRock = errors.New("luarocks: unsafe archive")
	assignment    = regexp.MustCompile(
		`(?m)^(package|version)\s*=\s*['"]([^'"\r\n]+)['"]\s*(?:--[^\r\n]*)?$`,
	)
)

func validateStored(ctx context.Context, blobs format.Blobs, sha string, id Identity) error {
	if sha == "" {
		return format.Reject(http.StatusBadRequest, "LuaRocks content is missing")
	}
	rc, info, err := blobs.Open(ctx, sha)
	if err != nil {
		return fmt.Errorf("luarocks: open rock: %w", err)
	}
	defer func() { _ = rc.Close() }()
	if id.Arch == "rockspec" {
		if info.Size > maxRockspec {
			return format.Reject(
				http.StatusRequestEntityTooLarge,
				"LuaRocks rockspec exceeds %d bytes",
				maxRockspec,
			)
		}
		body, err := io.ReadAll(io.LimitReader(rc, maxRockspec+1))
		if err != nil {
			return fmt.Errorf("luarocks: read rockspec: %w", err)
		}
		return validateRockspec(body, id)
	}
	if archive.KindOf(archive.ReaderAt(rc), id.Name+".rock") != archive.Zip {
		return format.Reject(http.StatusBadRequest, "LuaRocks rock must be a ZIP archive")
	}
	var spec []byte
	var total int64
	err = archive.Scan(
		archive.ReaderAt(rc),
		info.Size,
		archive.Zip,
		rockLimits,
		func(e archive.Entry) error {
			name, err := archive.Clean(e.Name)
			if err != nil {
				return fmt.Errorf("luarocks: archive path: %w", err)
			}
			if e.Dir {
				return nil
			}
			if !e.Regular {
				return fmt.Errorf("%w: non-regular entry", errUnsafeRock)
			}
			if e.Size > rockLimits.File || total+e.Size > rockLimits.Inflated {
				return fmt.Errorf("%w: unpacked size limit", errUnsafeRock)
			}
			if name == id.Name+"-"+id.Version+".rockspec" {
				if e.Size > maxRockspec {
					return fmt.Errorf("%w: rockspec size limit", errUnsafeRock)
				}
				spec, err = archive.ReadEntry(e, archive.Limits{File: maxRockspec})
				if err != nil {
					return fmt.Errorf("luarocks: read embedded rockspec: %w", err)
				}
				total += int64(len(spec))
				if int64(len(spec)) != e.Size {
					return fmt.Errorf("%w: rockspec entry size", errUnsafeRock)
				}
				return nil
			}
			r, err := e.Open()
			if err != nil {
				return fmt.Errorf("luarocks: open archive entry: %w", err)
			}
			n, readErr := io.Copy(io.Discard, io.LimitReader(r, rockLimits.Inflated-total+1))
			if closer, ok := r.(io.Closer); ok {
				_ = closer.Close()
			}
			total += n
			if readErr != nil {
				return fmt.Errorf("luarocks: read archive entry: %w", readErr)
			}
			if total > rockLimits.Inflated || n > rockLimits.File || n != e.Size {
				return fmt.Errorf("%w: entry size limit or mismatch", errUnsafeRock)
			}
			return nil
		},
	)
	if err != nil {
		return format.Reject(http.StatusBadRequest, "Unsafe or invalid LuaRocks archive: %v", err)
	}
	if len(spec) == 0 {
		return format.Reject(http.StatusBadRequest, "LuaRocks rock contains no matching rockspec")
	}
	return validateRockspec(spec, id)
}

func validateRockspec(body []byte, id Identity) error {
	if len(body) > maxRockspec || strings.IndexByte(string(body), 0) >= 0 {
		return format.Reject(http.StatusBadRequest, "Invalid LuaRocks rockspec")
	}
	fields := map[string]string{}
	for _, m := range assignment.FindAllSubmatch(body, -1) {
		key := string(m[1])
		if _, exists := fields[key]; exists {
			return format.Reject(http.StatusBadRequest, "Duplicate LuaRocks rockspec %s", key)
		}
		fields[key] = string(m[2])
	}
	if fields["package"] != id.Name || fields["version"] != id.Version {
		return format.Reject(
			http.StatusBadRequest,
			"LuaRocks rockspec package/version does not match filename",
		)
	}
	return nil
}
