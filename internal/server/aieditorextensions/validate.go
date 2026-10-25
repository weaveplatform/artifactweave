package aieditorextensions

import (
	"archive/zip"
	"bytes"
	"crypto/sha256"
	"encoding/base64"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"path"
	"strings"
)

const (
	maxManifest = 1 << 20
	maxPackage  = 512 << 20
	maxSig      = 1 << 20
)

var errAsset = errors.New("invalid extension asset")

type extensionManifest struct {
	Name      string `json:"name"`
	Publisher string `json:"publisher"`
	Version   string `json:"version"`
	Engines   struct {
		VSCode string `json:"vscode"`
	} `json:"engines"`
}

func validateManifest(body io.Reader, size int64, publisher, name, version string) error {
	if size < 1 || size > maxManifest {
		return errAsset
	}
	data, err := io.ReadAll(io.LimitReader(body, maxManifest+1))
	if err != nil || int64(len(data)) != size {
		return errAsset
	}
	var manifest extensionManifest
	if json.Unmarshal(data, &manifest) != nil ||
		strings.ToLower(manifest.Publisher) != publisher ||
		strings.ToLower(manifest.Name) != name ||
		manifest.Version != version || manifest.Engines.VSCode == "" ||
		len(manifest.Engines.VSCode) > 128 {
		return errAsset
	}
	return nil
}

func validatePackage(body io.ReadSeeker, size int64, publisher, name, version string) error {
	archive, err := readZIP(body, size, maxPackage)
	if err != nil || len(archive.File) == 0 || len(archive.File) > 50000 {
		return errAsset
	}
	var total uint64
	seen := make(map[string]bool, len(archive.File))
	var manifest *zip.File
	for _, file := range archive.File {
		if !safeZIPName(file.Name) || seen[file.Name] ||
			file.UncompressedSize64 > maxPackage || total > 2<<30-file.UncompressedSize64 {
			return errAsset
		}
		seen[file.Name] = true
		total += file.UncompressedSize64
		if file.Name == "extension/package.json" {
			manifest = file
		}
	}
	if manifest == nil || manifest.UncompressedSize64 > maxManifest {
		return errAsset
	}
	r, err := manifest.Open()
	if err != nil {
		return errAsset
	}
	defer r.Close()
	return validateManifest(r, int64(manifest.UncompressedSize64), publisher, name, version)
}

func validateSignature(body io.ReadSeeker, size int64, expectedSHA string) error {
	archive, err := readZIP(body, size, maxSig)
	if err != nil || len(archive.File) != 2 || !sha256Value(expectedSHA) {
		return errAsset
	}
	seen := map[string]bool{}
	var signatureManifest *zip.File
	for _, file := range archive.File {
		if file.Name != ".signature.manifest" && file.Name != ".signature.p7s" ||
			seen[file.Name] || file.UncompressedSize64 == 0 || file.UncompressedSize64 > maxSig {
			return errAsset
		}
		seen[file.Name] = true
		if file.Name == ".signature.manifest" {
			signatureManifest = file
		}
	}
	if !seen[".signature.manifest"] || !seen[".signature.p7s"] {
		return errAsset
	}
	r, err := signatureManifest.Open()
	if err != nil {
		return errAsset
	}
	defer r.Close()
	data, err := io.ReadAll(io.LimitReader(r, maxSig+1))
	if err != nil || uint64(len(data)) != signatureManifest.UncompressedSize64 {
		return errAsset
	}
	var statement struct {
		Package struct {
			Size    int64 `json:"size"`
			Digests struct {
				SHA256 string `json:"sha256"`
			} `json:"digests"`
		} `json:"package"`
	}
	if json.Unmarshal(data, &statement) != nil || statement.Package.Size < 1 ||
		statement.Package.Size > maxPackage {
		return errAsset
	}
	claimed, err := base64.StdEncoding.DecodeString(statement.Package.Digests.SHA256)
	if err != nil || len(claimed) != sha256.Size ||
		!strings.EqualFold(fmt.Sprintf("%x", claimed), expectedSHA) {
		return errAsset
	}
	return nil
}

func readZIP(body io.ReadSeeker, size, limit int64) (*zip.Reader, error) {
	if size < 1 || size > limit {
		return nil, errAsset
	}
	if readerAt, ok := body.(io.ReaderAt); ok {
		archive, err := zip.NewReader(readerAt, size)
		if err != nil {
			return nil, errAsset
		}
		return archive, nil
	}
	if size > 64<<20 {
		return nil, errAsset
	}
	data, err := io.ReadAll(io.LimitReader(body, size+1))
	if err != nil || int64(len(data)) != size {
		return nil, errAsset
	}
	archive, err := zip.NewReader(bytes.NewReader(data), size)
	if err != nil {
		return nil, errAsset
	}
	return archive, nil
}

func safeZIPName(raw string) bool {
	if raw == "" || len(raw) > 512 || strings.Contains(raw, "\\") ||
		strings.HasPrefix(raw, "/") || strings.Contains(raw, "//") ||
		path.Clean(raw) != strings.TrimSuffix(raw, "/") {
		return false
	}
	for _, segment := range strings.Split(strings.TrimSuffix(raw, "/"), "/") {
		if segment == "" || segment == "." || segment == ".." {
			return false
		}
	}
	return true
}
