package cocoapods

import (
	"archive/tar"
	"archive/zip"
	"bytes"
	"compress/gzip"
	"errors"
	"fmt"
	"io"
	"strings"
)

// ValidateSourceArchive checks an HTTP-sourced pod's complete archive before
// remote cache publication. Its podspec comes from the CDN, not this archive.
func ValidateSourceArchive(body io.ReadSeeker, size int64, ext string) error {
	if size <= 0 || size > MaxArchiveBytes {
		return invalid("invalid CocoaPods source archive size")
	}
	data, err := io.ReadAll(io.LimitReader(body, MaxArchiveBytes+1))
	if err != nil || int64(len(data)) != size {
		return invalid("truncated CocoaPods source archive")
	}
	if ext == ".zip" {
		return validateZIP(data)
	}
	if ext != ".tgz" && ext != ".tar.gz" {
		return invalid("unsupported CocoaPods source archive extension")
	}
	return validateTGZ(data)
}

func safeArchivePath(name string) bool {
	if name == "" || strings.HasPrefix(name, "/") || strings.ContainsAny(name, "\\\x00") {
		return false
	}
	for _, part := range strings.Split(strings.TrimSuffix(name, "/"), "/") {
		if part == "" || part == "." || part == ".." {
			return false
		}
	}
	return true
}

func validateZIP(data []byte) error {
	r, err := zip.NewReader(bytes.NewReader(data), int64(len(data)))
	if err != nil || len(r.File) == 0 || len(r.File) > maxArchiveFiles {
		return invalid("invalid CocoaPods source zip")
	}
	seen := map[string]bool{}
	var expanded int64
	for _, file := range r.File {
		name := strings.TrimSuffix(file.Name, "/")
		if !safeArchivePath(file.Name) || seen[name] ||
			!file.Mode().IsRegular() && !file.FileInfo().IsDir() {
			return invalid("unsafe CocoaPods source zip member")
		}
		seen[name] = true
		if file.FileInfo().IsDir() {
			continue
		}
		if file.UncompressedSize64 > maxExpandedBytes ||
			expanded+int64(file.UncompressedSize64) > maxExpandedBytes {
			return invalid("oversized CocoaPods source zip")
		}
		expanded += int64(file.UncompressedSize64)
		rc, err := file.Open()
		if err != nil {
			return fmt.Errorf("open cocoapods zip member: %w", err)
		}
		_, copyErr := io.Copy(io.Discard, io.LimitReader(rc, maxExpandedBytes+1))
		closeErr := rc.Close()
		if copyErr != nil || closeErr != nil {
			return invalid("corrupt CocoaPods source zip member")
		}
	}
	return nil
}

func validateTGZ(data []byte) error {
	gz, err := gzip.NewReader(bytes.NewReader(data))
	if err != nil {
		return fmt.Errorf("cocoapods source gzip: %w", err)
	}
	defer gz.Close()
	r := tar.NewReader(gz)
	seen := map[string]bool{}
	var expanded int64
	for count := 0; ; count++ {
		h, err := r.Next()
		if errors.Is(err, io.EOF) {
			break
		}
		if err != nil || h == nil || count >= maxArchiveFiles || h.Size < 0 ||
			!safeArchivePath(h.Name) || seen[strings.TrimSuffix(h.Name, "/")] ||
			(h.Typeflag != tar.TypeReg && h.Typeflag != 0 && h.Typeflag != tar.TypeDir) {
			return invalid("unsafe CocoaPods source tar member")
		}
		seen[strings.TrimSuffix(h.Name, "/")] = true
		expanded += h.Size
		if expanded > maxExpandedBytes {
			return invalid("oversized CocoaPods source tar")
		}
		if h.Typeflag != tar.TypeDir {
			if _, err := io.CopyN(io.Discard, r, h.Size); err != nil {
				return invalid("truncated CocoaPods source tar member")
			}
		}
	}
	if len(seen) == 0 {
		return invalid("empty CocoaPods source archive")
	}
	var extra [1]byte
	if n, err := gz.Read(extra[:]); n != 0 || !errors.Is(err, io.EOF) {
		return invalid("extra CocoaPods source data or invalid gzip trailer")
	}
	return nil
}
