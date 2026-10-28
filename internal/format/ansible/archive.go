package ansible

import (
	"archive/tar"
	"bytes"
	"compress/gzip"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"io"
	"path"
	"regexp"
	"strings"

	"golang.org/x/mod/semver"
)

const (
	MaxArchiveBytes  = 128 << 20
	maxExpandedBytes = 512 << 20
	maxFiles         = 4096
	maxMetadataBytes = 1 << 20
)

var (
	ErrArchive = errors.New("invalid Ansible collection archive")
	nameRE     = regexp.MustCompile(`^[a-z][a-z0-9_]{0,63}$`)
)

// Metadata is extracted from the Ansible collection's MANIFEST.json.
type Metadata struct {
	Namespace    string            `json:"namespace"`
	Name         string            `json:"name"`
	Version      string            `json:"version"`
	Dependencies map[string]string `json:"dependencies"`
	Description  string            `json:"description,omitempty"`
	SHA256       string            `json:"sha256"`
}

func ValidName(v string) bool { return nameRE.MatchString(v) }

func ValidVersion(v string) bool {
	core, _, _ := strings.Cut(v, "-")
	core, _, _ = strings.Cut(core, "+")
	return len(v) <= 128 && strings.Count(core, ".") == 2 && !strings.HasPrefix(v, "v") &&
		semver.IsValid("v"+v)
}

func Path(namespace, name, version string) string {
	return "collections/" + namespace + "/" + name + "/" + version + "/" +
		namespace + "-" + name + "-" + version + ".tar.gz"
}

func ParsePath(p string) (namespace, name, version string, ok bool) {
	parts := strings.Split(p, "/")
	if len(parts) != 5 || parts[0] != "collections" ||
		!ValidName(parts[1]) || !ValidName(parts[2]) || !ValidVersion(parts[3]) ||
		p != Path(parts[1], parts[2], parts[3]) {
		return "", "", "", false
	}
	return parts[1], parts[2], parts[3], true
}

type manifest struct {
	CollectionInfo struct {
		Namespace    string            `json:"namespace"`
		Name         string            `json:"name"`
		Version      string            `json:"version"`
		Dependencies map[string]string `json:"dependencies"`
		Description  string            `json:"description"`
	} `json:"collection_info"`
	FileManifest struct {
		Name   string `json:"name"`
		SHA256 string `json:"chksum_sha256"`
	} `json:"file_manifest_file"`
}

type fileManifest struct {
	Files []struct {
		Name   string `json:"name"`
		Type   string `json:"ftype"`
		SHA256 string `json:"chksum_sha256"`
	} `json:"files"`
}

// ReadArchive validates a bounded release archive before it can become a
// package. It verifies the FILES.json digest and every listed file digest.
func ReadArchive(r io.Reader, size int64) (Metadata, error) {
	if size <= 0 || size > MaxArchiveBytes {
		return Metadata{}, ErrArchive
	}
	limited := io.LimitReader(r, MaxArchiveBytes+1)
	h := sha256.New()
	compressed, err := io.ReadAll(io.TeeReader(limited, h))
	if err != nil || int64(len(compressed)) != size {
		return Metadata{}, ErrArchive
	}
	gz, err := gzip.NewReader(bytes.NewReader(compressed))
	if err != nil {
		return Metadata{}, ErrArchive
	}
	defer gz.Close()
	files, manifestBytes, fileBytes, err := readEntries(gz)
	if err != nil {
		return Metadata{}, err
	}

	if len(manifestBytes) == 0 || len(fileBytes) == 0 {
		return Metadata{}, ErrArchive
	}
	var m manifest
	if err := json.Unmarshal(manifestBytes, &m); err != nil ||
		!ValidName(m.CollectionInfo.Namespace) || !ValidName(m.CollectionInfo.Name) ||
		!ValidVersion(m.CollectionInfo.Version) ||
		m.FileManifest.Name != "FILES.json" ||
		!validHash(m.FileManifest.SHA256) ||
		!strings.EqualFold(m.FileManifest.SHA256, files["FILES.json"]) {
		return Metadata{}, ErrArchive
	}
	if err := validateFiles(fileBytes, files); err != nil {
		return Metadata{}, err
	}

	if !ValidDependencies(m.CollectionInfo.Dependencies) {
		return Metadata{}, ErrArchive
	}
	return Metadata{
		Namespace:    m.CollectionInfo.Namespace,
		Name:         m.CollectionInfo.Name,
		Version:      m.CollectionInfo.Version,
		Dependencies: m.CollectionInfo.Dependencies,
		Description:  m.CollectionInfo.Description,
		SHA256:       hex.EncodeToString(h.Sum(nil)),
	}, nil
}

func validHash(v string) bool {
	if len(v) != 64 {
		return false
	}
	_, err := hex.DecodeString(v)
	return err == nil
}

func safeEntry(v string) bool {
	v = strings.TrimSuffix(v, "/")
	if v == "" || strings.HasPrefix(v, "/") || strings.ContainsAny(v, "\\\x00") ||
		path.Clean(v) != v {
		return false
	}
	for _, part := range strings.Split(v, "/") {
		if part == "" || part == "." || part == ".." {
			return false
		}
	}
	return true
}

// ValidDependencies accepts Galaxy's comma-separated semantic-version ranges.
func ValidDependencies(deps map[string]string) bool {
	if len(deps) > 256 {
		return false
	}
	for dependency, constraint := range deps {
		parts := strings.Split(dependency, ".")
		if len(parts) != 2 || !ValidName(parts[0]) || !ValidName(parts[1]) ||
			len(constraint) > 1024 ||
			constraint == "" {
			return false
		}
		if constraint == "*" {
			continue
		}
		for _, term := range strings.Split(constraint, ",") {
			term = strings.TrimSpace(term)
			for _, op := range []string{"!=", ">=", "<=", "==", "=", ">", "<"} {
				if strings.HasPrefix(term, op) {
					term = strings.TrimSpace(strings.TrimPrefix(term, op))
					break
				}
			}
			if !ValidVersion(term) {
				return false
			}
		}
	}
	return true
}

func readEntries(gz io.Reader) (map[string]string, []byte, []byte, error) {
	expanded := &io.LimitedReader{R: gz, N: maxExpandedBytes + maxFiles*2048 + 1}
	t := tar.NewReader(expanded)
	files := map[string]string{}
	var manifestBytes, fileBytes []byte
	var total int64
	for count := 0; ; count++ {
		if count > maxFiles {
			return nil, nil, nil, ErrArchive
		}
		hdr, e := t.Next()
		if errors.Is(e, io.EOF) {
			break
		}
		if e != nil || hdr == nil || !safeEntry(hdr.Name) || hdr.Size < 0 ||
			hdr.Size > maxExpandedBytes || total+hdr.Size > maxExpandedBytes ||
			files[strings.TrimSuffix(hdr.Name, "/")] != "" {
			return nil, nil, nil, ErrArchive
		}
		total += hdr.Size
		if hdr.Typeflag == tar.TypeDir {
			if hdr.Size != 0 {
				return nil, nil, nil, ErrArchive
			}
			files[strings.TrimSuffix(hdr.Name, "/")] = "directory"
			continue
		}
		if hdr.Typeflag != tar.TypeReg {
			return nil, nil, nil, ErrArchive
		}
		if (hdr.Name == "MANIFEST.json" || hdr.Name == "FILES.json") &&
			hdr.Size > maxMetadataBytes {
			return nil, nil, nil, ErrArchive
		}
		sink := io.Discard
		var capture *strings.Builder
		if hdr.Name == "MANIFEST.json" || hdr.Name == "FILES.json" {
			capture = &strings.Builder{}
			sink = capture
		}
		fh := sha256.New()
		if _, e = io.CopyN(io.MultiWriter(fh, sink), t, hdr.Size); e != nil {
			return nil, nil, nil, ErrArchive
		}
		files[hdr.Name] = hex.EncodeToString(fh.Sum(nil))
		if capture != nil {
			if hdr.Name == "MANIFEST.json" {
				manifestBytes = []byte(capture.String())
			} else {
				fileBytes = []byte(capture.String())
			}
		}
	}
	if _, err := io.Copy(io.Discard, expanded); err != nil || expanded.N == 0 {
		return nil, nil, nil, ErrArchive
	}
	return files, manifestBytes, fileBytes, nil
}

func validateFiles(fileBytes []byte, files map[string]string) error {
	var fm fileManifest
	if err := json.Unmarshal(fileBytes, &fm); err != nil || len(fm.Files) > maxFiles {
		return ErrArchive
	}
	listed := map[string]bool{}
	for _, f := range fm.Files {
		if f.Type == "dir" {
			if f.Name == "." {
				continue
			}
			if !safeEntry(f.Name) || files[strings.TrimSuffix(f.Name, "/")] != "directory" ||
				listed[f.Name] {
				return ErrArchive
			}
			listed[f.Name] = true
			continue
		}
		if f.Type != "file" || !safeEntry(f.Name) || listed[f.Name] || !validHash(f.SHA256) ||
			!strings.EqualFold(files[f.Name], f.SHA256) {
			return ErrArchive
		}
		listed[f.Name] = true
	}
	for name, digest := range files {
		if digest != "directory" && name != "MANIFEST.json" && name != "FILES.json" &&
			!listed[name] {
			return ErrArchive
		}
	}
	return nil
}
