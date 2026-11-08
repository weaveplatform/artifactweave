package conda

import (
	"bytes"
	"compress/bzip2"
	"crypto/md5" //nolint:gosec // Conda's legacy metadata can declare MD5; SHA-256 is preferred.
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"strings"

	"github.com/klauspost/compress/zstd"
)

const (
	maxRepodataBytes = 64 << 20
	maxRepodataItems = 200000
)

var ErrInvalidRepodata = errors.New("conda: invalid repodata")

// Repodata is the Conda channel index for one platform subdirectory.
type Repodata struct {
	Info struct {
		Subdir string `json:"subdir"`
	} `json:"info"`
	Packages        map[string]Record `json:"packages"`
	PackagesConda   map[string]Record `json:"packages.conda"` //nolint:tagliatelle // Conda wire field is packages.conda.
	Removed         []string          `json:"removed"`
	RepodataVersion int               `json:"repodata_version"`
}

func EmptyRepodata(subdir string) Repodata {
	index := Repodata{
		Packages: map[string]Record{}, PackagesConda: map[string]Record{},
		Removed: []string{}, RepodataVersion: 1,
	}
	index.Info.Subdir = subdir
	return index
}

func validHexDigest(digest string, size int) bool {
	if len(digest) != size*2 || strings.ToLower(digest) != digest {
		return false
	}
	_, err := hex.DecodeString(digest)
	return err == nil
}

func validateRecord(filename, channel, subdir string, rec Record, extension string) error {
	id, ok := ParsePackagePath(IndexPath(channel, subdir, filename))
	if !ok || id.Extension != extension || rec.Name != id.Name ||
		rec.Version != id.Version || rec.Build != id.Build ||
		rec.BuildNumber < 0 || rec.BuildNumber > 1_000_000 ||
		rec.Subdir != "" && rec.Subdir != subdir ||
		rec.Size <= 0 || rec.Size > maxCompressedPackage ||
		rec.SHA256 != "" && !validHexDigest(rec.SHA256, sha256.Size) ||
		rec.MD5 != "" && !validHexDigest(rec.MD5, md5.Size) ||
		rec.SHA256 == "" && rec.MD5 == "" ||
		len(rec.Depends) > 512 || len(rec.Constrains) > 512 {
		return fmt.Errorf("%w: package record identity or digest", ErrInvalidRepodata)
	}
	for _, dep := range append(rec.Depends, rec.Constrains...) {
		if dep == "" || !validText(dep, 512) {
			return fmt.Errorf("%w: package dependency", ErrInvalidRepodata)
		}
	}
	return nil
}

// DecodeRepodata validates each disclosed package path and integrity claim
// before a remote index enters the cache or a virtual index includes it.
func DecodeRepodata(body []byte, path MetadataPath) (Repodata, error) {
	if len(body) == 0 || len(body) > maxRepodataBytes {
		return Repodata{}, fmt.Errorf("%w: size", ErrInvalidRepodata)
	}
	var raw []byte
	switch {
	case strings.HasSuffix(path.Filename, ".json"):
		raw = body
	case strings.HasSuffix(path.Filename, ".json.zst"):
		decoder, err := zstd.NewReader(bytes.NewReader(body),
			zstd.WithDecoderMaxMemory(maxRepodataBytes))
		if err != nil {
			return Repodata{}, fmt.Errorf("%w: zstd", ErrInvalidRepodata)
		}
		defer decoder.Close()
		raw, err = io.ReadAll(io.LimitReader(decoder, maxRepodataBytes+1))
		if err != nil {
			return Repodata{}, fmt.Errorf("%w: zstd content", ErrInvalidRepodata)
		}
	case strings.HasSuffix(path.Filename, ".json.bz2"):
		var err error
		raw, err = io.ReadAll(io.LimitReader(bzip2.NewReader(bytes.NewReader(body)),
			maxRepodataBytes+1))
		if err != nil {
			return Repodata{}, fmt.Errorf("%w: bzip2 content", ErrInvalidRepodata)
		}
	default:
		return Repodata{}, fmt.Errorf("%w: path", ErrInvalidRepodata)
	}
	if len(raw) == 0 || len(raw) > maxRepodataBytes {
		return Repodata{}, fmt.Errorf("%w: expanded size", ErrInvalidRepodata)
	}
	var index Repodata
	if err := json.Unmarshal(raw, &index); err != nil || index.Info.Subdir != "" &&
		index.Info.Subdir != path.Subdir || index.RepodataVersion < 0 ||
		index.RepodataVersion > 1 || len(index.Packages)+len(index.PackagesConda) > maxRepodataItems {
		return Repodata{}, fmt.Errorf("%w: json structure", ErrInvalidRepodata)
	}
	for name, rec := range index.Packages {
		if err := validateRecord(name, path.Channel, path.Subdir, rec, ".tar.bz2"); err != nil {
			return Repodata{}, err
		}
	}
	for name, rec := range index.PackagesConda {
		if err := validateRecord(name, path.Channel, path.Subdir, rec, ".conda"); err != nil {
			return Repodata{}, err
		}
	}
	if len(index.Removed) > maxRepodataItems {
		return Repodata{}, fmt.Errorf("%w: removed entries", ErrInvalidRepodata)
	}
	for _, name := range index.Removed {
		if _, ok := ParsePackagePath(IndexPath(path.Channel, path.Subdir, name)); !ok {
			return Repodata{}, fmt.Errorf("%w: removed package path", ErrInvalidRepodata)
		}
	}
	if index.Packages == nil {
		index.Packages = map[string]Record{}
	}
	if index.PackagesConda == nil {
		index.PackagesConda = map[string]Record{}
	}
	if index.Removed == nil {
		index.Removed = []string{}
	}
	index.Info.Subdir = path.Subdir
	return index, nil
}

func EncodeRepodata(index Repodata, channel, subdir string) (map[string][]byte, error) {
	if index.Info.Subdir != subdir ||
		len(index.Packages)+len(index.PackagesConda) > maxRepodataItems {
		return nil, fmt.Errorf("%w: encoded index", ErrInvalidRepodata)
	}
	for name, rec := range index.Packages {
		if err := validateRecord(name, channel, subdir, rec, ".tar.bz2"); err != nil {
			return nil, err
		}
	}
	for name, rec := range index.PackagesConda {
		if err := validateRecord(name, channel, subdir, rec, ".conda"); err != nil {
			return nil, err
		}
	}
	index.RepodataVersion = 1
	if index.Removed == nil {
		index.Removed = []string{}
	}
	raw, err := json.Marshal(index)
	if err != nil || len(raw) > maxRepodataBytes {
		return nil, fmt.Errorf("%w: json encoding", ErrInvalidRepodata)
	}
	var compressed bytes.Buffer
	encoder, err := zstd.NewWriter(&compressed, zstd.WithEncoderConcurrency(1))
	if err != nil {
		return nil, fmt.Errorf("conda: zstd encoder: %w", err)
	}
	if _, err := encoder.Write(raw); err != nil {
		_ = encoder.Close()
		return nil, fmt.Errorf("conda: zstd index: %w", err)
	}
	if err := encoder.Close(); err != nil {
		return nil, fmt.Errorf("conda: zstd close: %w", err)
	}
	return map[string][]byte{
		"repodata.json": raw, "repodata.json.zst": compressed.Bytes(),
		"current_repodata.json": raw, "current_repodata.json.zst": compressed.Bytes(),
	}, nil
}
