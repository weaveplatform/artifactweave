package bowerhttp

import (
	"archive/tar"
	"archive/zip"
	"bytes"
	"compress/gzip"
	"errors"
	"io"
	"slices"
	"strings"

	bowerfmt "github.com/weaveplatform/artifactweave/internal/artifacts/ecosystem/bower"
)

func validGitTarget(tag string) bool {
	if tag == "" || len(tag) > 200 || strings.Contains(tag, "..") ||
		strings.HasSuffix(tag, ".lock") {
		return false
	}
	for _, c := range tag {
		if (c < 'a' || c > 'z') && (c < 'A' || c > 'Z') && (c < '0' || c > '9') &&
			!strings.ContainsRune("._-+", c) {
			return false
		}
	}
	return true
}

// dependencyFiles admits bounded regular files only. Archives may have one
// provider root or be rootless; links, ambiguous paths, duplicate files and
// malformed manifests are rejected before anything enters the remote cache.
func dependencyFiles(data []byte, name string) (map[string][]byte, error) {
	if len(data) == 0 || len(data) > maxArchiveBytes || !bowerfmt.ValidName(name) {
		return nil, remoteInvalid()
	}
	files := map[string][]byte{}
	var total int64
	add := func(path string, size int64, body io.Reader) error {
		if len(files) >= maxArchiveFiles || size < 0 || size > maxArchiveBytes-total {
			return remoteInvalid()
		}
		if _, _, _, ok := bowerfmt.ParsePath(name + "/0.0.0/" + path); !ok {
			return remoteInvalid()
		}
		if _, exists := files[path]; exists {
			return remoteInvalid()
		}
		content, err := io.ReadAll(io.LimitReader(body, size+1))
		if err != nil || int64(len(content)) != size {
			return remoteInvalid()
		}
		files[path] = content
		total += size
		return nil
	}
	var err error
	if bytes.HasPrefix(data, []byte("PK\x03\x04")) {
		err = readDependencyZIP(data, add)
	} else {
		err = readDependencyTAR(data, name, add)
	}
	if err != nil {
		return nil, err
	}
	prefix := ""
	if _, ok := files["bower.json"]; !ok {
		for path := range files {
			if strings.Count(path, "/") == 1 && strings.HasSuffix(path, "/bower.json") {
				if prefix != "" {
					return nil, remoteInvalid()
				}
				prefix = strings.TrimSuffix(path, "bower.json")
			}
		}
		if prefix == "" {
			return nil, remoteInvalid()
		}
	}
	normalized := make(map[string][]byte, len(files))
	for path, content := range files {
		if !strings.HasPrefix(path, prefix) {
			return nil, remoteInvalid()
		}
		normalized[strings.TrimPrefix(path, prefix)] = content
	}
	m, err := bowerfmt.DecodeGitManifest(normalized["bower.json"])
	if err != nil || m.Name != name {
		return nil, remoteInvalid()
	}
	return normalized, nil
}

func dependencyArchive(files map[string][]byte, name string) ([]byte, error) {
	var out bytes.Buffer
	gz := gzip.NewWriter(&out)
	tw := tar.NewWriter(gz)
	names := make([]string, 0, len(files))
	for path := range files {
		names = append(names, path)
	}
	slices.Sort(names)
	for _, path := range names {
		data := files[path]
		if err := tw.WriteHeader(
			&tar.Header{
				Name:     name + "/" + path,
				Mode:     0o644,
				Size:     int64(len(data)),
				Typeflag: tar.TypeReg,
			},
		); err != nil {
			return nil, remoteInvalid()
		}
		if _, err := tw.Write(data); err != nil {
			return nil, remoteInvalid()
		}
		if out.Len() > maxArchiveBytes {
			return nil, remoteInvalid()
		}
	}
	if errors.Join(tw.Close(), gz.Close()) != nil || out.Len() > maxArchiveBytes {
		return nil, remoteInvalid()
	}
	return out.Bytes(), nil
}

func readDependencyZIP(data []byte, add func(string, int64, io.Reader) error) error {
	zr, err := zip.NewReader(bytes.NewReader(data), int64(len(data)))
	if err != nil || len(zr.File) > maxArchiveFiles {
		return remoteInvalid()
	}
	for _, f := range zr.File {
		if f.FileInfo().IsDir() {
			continue
		}
		if !f.Mode().IsRegular() || f.UncompressedSize64 > maxArchiveBytes {
			return remoteInvalid()
		}
		body, err := f.Open()
		if err != nil {
			return remoteInvalid()
		}
		readErr := add(f.Name, int64(f.UncompressedSize64), body)
		closeErr := body.Close()
		if readErr != nil || closeErr != nil {
			return remoteInvalid()
		}
	}
	return nil
}

func readDependencyTAR(data []byte, name string, add func(string, int64, io.Reader) error) error {
	gz, err := gzip.NewReader(bytes.NewReader(data))
	if err != nil {
		return remoteInvalid()
	}
	defer gz.Close()
	bounded := io.LimitReader(gz, maxArchiveBytes+int64(maxArchiveFiles)*2048+1)
	tr := tar.NewReader(bounded)
	count := 0
	for {
		entry, err := tr.Next()
		if errors.Is(err, io.EOF) {
			break
		}
		if err != nil {
			return remoteInvalid()
		}
		count++
		if count > maxArchiveFiles {
			return remoteInvalid()
		}
		if entry.Typeflag == tar.TypeXGlobalHeader {
			if !safePAXGlobal(entry.PAXRecords) {
				return remoteInvalid()
			}
			continue
		}
		if entry.Typeflag == tar.TypeDir {
			if _, _, _, ok := bowerfmt.ParsePath(
				name + "/0.0.0/" + strings.TrimSuffix(entry.Name, "/"),
			); !ok {
				return remoteInvalid()
			}
			continue
		}
		if entry.Typeflag != tar.TypeReg && entry.Typeflag != 0 {
			return remoteInvalid()
		}
		if err := add(entry.Name, entry.Size, tr); err != nil {
			return err
		}
	}
	// A complete gzip drain checks CRC/truncation and rejects expansion beyond
	// the same bound even after tar's end-of-archive marker.
	if _, err := io.Copy(io.Discard, bounded); err != nil {
		return remoteInvalid()
	}
	var extra [1]byte
	if n, err := gz.Read(extra[:]); n != 0 || !errors.Is(err, io.EOF) {
		return remoteInvalid()
	}
	return nil
}
