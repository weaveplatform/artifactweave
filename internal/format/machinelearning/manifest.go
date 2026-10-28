package machinelearning

import (
	"bytes"
	"context"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/http"
	"strings"
	"time"

	"github.com/weaveplatform/artifactweave/internal/format"
	"github.com/weaveplatform/artifactweave/pkg/repo"
	"github.com/weaveplatform/artifactweave/pkg/store"
	"github.com/weaveplatform/artifactweave/pkg/types"
)

type Checksums struct {
	SHA256 string `json:"sha2"`
}
type Artifact struct {
	Path        string    `json:"artifact_path"`
	Size        int64     `json:"size"`
	Checksums   Checksums `json:"checksums"`
	DownloadURL string    `json:"download_url,omitempty"`
}
type Serialization struct {
	Framework        string `json:"framework"`
	FrameworkVersion string `json:"framework_version"`
	Format           string `json:"serialization_format"`
	Runtime          string `json:"runtime"`
	RuntimeVersion   string `json:"runtime_version"`
}
type Manifest struct {
	Created      string        `json:"created_date"`
	Artifacts    []Artifact    `json:"model_artifacts"`
	Format       Serialization `json:"model_format"`
	Dependencies []Artifact    `json:"dependency_artifacts,omitempty"`
	Code         *Artifact     `json:"code_artifacts,omitempty"`
	ID           string        `json:"id,omitempty"`
	Version      string        `json:"version,omitempty"`
}
type FileEntry struct {
	Path     string
	Artifact Artifact
}

func (m Manifest) Files() []FileEntry {
	files := make([]FileEntry, 0, len(m.Artifacts)+len(m.Dependencies)+1)
	for _, a := range m.Artifacts {
		files = append(files, FileEntry{Path: "files/" + a.Path, Artifact: a})
	}
	for _, a := range m.Dependencies {
		files = append(files, FileEntry{Path: "dependencies/" + a.Path, Artifact: a})
	}
	if m.Code != nil {
		files = append(files, FileEntry{Path: "code/" + m.Code.Path, Artifact: *m.Code})
	}
	return files
}

func Decode(body io.Reader) (Manifest, error) {
	var m Manifest
	data, err := io.ReadAll(io.LimitReader(body, MaxManifestSize+1))
	if err != nil {
		return m, fmt.Errorf("machinelearning: read manifest: %w", err)
	}
	if len(data) > MaxManifestSize || !unambiguousJSON(data) || json.Unmarshal(data, &m) != nil {
		return m, invalid("invalid or oversized model manifest")
	}
	// FrogML 2.1 emits UTC timestamps without a zone suffix.
	if _, err := time.Parse(time.RFC3339Nano, m.Created); err != nil && !sdkTimestamp(m.Created) {
		return m, invalid("invalid model creation time")
	}
	if len(m.Artifacts) == 0 || len(m.Files()) > MaxFiles {
		return m, invalid("invalid number of model files")
	}
	if m.Format.Framework == "" || m.Format.Format == "" || m.Format.Runtime == "" ||
		m.Format.RuntimeVersion == "" {
		return m, invalid("missing model serialization metadata")
	}
	for _, value := range []string{m.Format.Framework, m.Format.FrameworkVersion, m.Format.Format, m.Format.Runtime, m.Format.RuntimeVersion} {
		if len(value) > 128 || strings.ContainsAny(value, "\x00\r\n") {
			return m, invalid("invalid model serialization metadata")
		}
	}
	seen := map[string]bool{}
	for _, file := range m.Files() {
		a := file.Artifact
		_, err := hex.DecodeString(a.Checksums.SHA256)
		if !File(a.Path) || a.Size < 0 || a.Size > MaxFileSize || len(a.Checksums.SHA256) != 64 ||
			strings.ToLower(a.Checksums.SHA256) != a.Checksums.SHA256 ||
			err != nil ||
			seen[file.Path] {
			return m, invalid("invalid or duplicate model artifact")
		}
		seen[file.Path] = true
	}
	return m, nil
}

func invalid(message string) error { return format.Rejectf(http.StatusBadRequest, "%s", message) }

// Read opens the bounded manifest, independently of any HTTP route.
func Read(
	ctx context.Context,
	env format.Env,
	c repo.Config,
	id Identity,
) (Manifest, types.Item, error) {
	it, err := env.Store.Items().Get(ctx, c.Key, false, id.Manifest())
	if err != nil {
		return Manifest{}, it, fmt.Errorf("machinelearning: find manifest: %w", err)
	}
	if it.Folder || it.Size > MaxManifestSize {
		return Manifest{}, it, invalid("invalid stored model manifest")
	}
	r, _, err := env.Blobs.Open(ctx, it.Actual.SHA256)
	if err != nil {
		return Manifest{}, it, fmt.Errorf("machinelearning: open manifest: %w", err)
	}
	defer r.Close()
	m, err := Decode(r)
	if err == nil && (m.ID != id.Name || m.Version != id.Version) {
		err = invalid("model manifest identity differs")
	}
	return m, it, err
}

// Verify checks the exact declared files against immutable stored checksums.
// It returns ErrNotFound for a version made incomplete by item deletion.
func Verify(
	ctx context.Context,
	env format.Env,
	c repo.Config,
	prefix string,
	m Manifest,
) ([]types.Item, error) {
	files := make([]types.Item, 0, len(m.Files()))
	for _, file := range m.Files() {
		it, err := env.Store.Items().Get(ctx, c.Key, false, prefix+"/"+file.Path)
		if err != nil {
			return nil, fmt.Errorf("machinelearning: find artifact: %w", err)
		}
		if it.Folder || it.Size != file.Artifact.Size ||
			it.Actual.SHA256 != file.Artifact.Checksums.SHA256 {
			return nil, format.Rejectf(
				http.StatusConflict,
				"model artifact checksum or size differs",
			)
		}
		if _, err := env.Blobs.Stat(ctx, it.Actual.SHA256); err != nil {
			return nil, fmt.Errorf("machinelearning: stat artifact: %w", err)
		}
		files = append(files, it)
	}
	return files, nil
}

func Missing(err error) bool { return errors.Is(err, store.ErrNotFound) }

func sdkTimestamp(s string) bool {
	_, err := time.Parse("2006-01-02T15:04:05.999999999", s)
	return err == nil
}

// The SDK schema has lowercase keys. Reject duplicate fields and Go's
// case-insensitive aliases before decoding identity and checksum metadata.
func unambiguousJSON(data []byte) bool {
	d := json.NewDecoder(bytes.NewReader(data))
	if !jsonValue(d, 0) {
		return false
	}
	_, err := d.Token()
	return errors.Is(err, io.EOF)
}

func jsonValue(d *json.Decoder, depth int) bool {
	if depth > 32 {
		return false
	}
	token, err := d.Token()
	if err != nil {
		return false
	}
	switch token {
	case json.Delim('{'):
		seen := map[string]bool{}
		for d.More() {
			key, err := d.Token()
			name, ok := key.(string)
			if err != nil || !ok || seen[name] || strings.ToLower(name) != name {
				return false
			}
			seen[name] = true
			if !jsonValue(d, depth+1) {
				return false
			}
		}
		token, err = d.Token()
		return err == nil && token == json.Delim('}')
	case json.Delim('['):
		for d.More() {
			if !jsonValue(d, depth+1) {
				return false
			}
		}
		token, err = d.Token()
		return err == nil && token == json.Delim(']')
	default:
		return true
	}
}
