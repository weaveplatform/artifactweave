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

	"github.com/weaveplatform/artifactweave/internal/artifacts/api/repo"
	"github.com/weaveplatform/artifactweave/internal/artifacts/core/format"
	"github.com/weaveplatform/artifactweave/internal/artifacts/core/model"
	"github.com/weaveplatform/artifactweave/internal/kernel"
)

// Checksums is a declared file's digest. The SDK calls its SHA-256 "sha2".
type Checksums struct {
	SHA256 string `json:"sha2"`
}

// Artifact is one declared file: its path within its category, its size and
// SHA-256, and the download URL that only the manifest GET's answer carries.
type Artifact struct {
	Path        string    `json:"artifact_path"`
	Size        int64     `json:"size"`
	Checksums   Checksums `json:"checksums"`
	DownloadURL string    `json:"download_url,omitempty"`
}

// Serialization is the model's format metadata. Every field but the
// framework version is required, and none may exceed 128 bytes or hold a
// NUL, CR or LF.
type Serialization struct {
	Framework        string `json:"framework"`
	FrameworkVersion string `json:"framework_version"`
	Format           string `json:"serialization_format"`
	Runtime          string `json:"runtime"`
	RuntimeVersion   string `json:"runtime_version"`
}

// Manifest is the FrogML SDK's model manifest: the creation time, the model,
// dependency and code artifacts, the serialization metadata, and the
// model's name (ID) and version, which a stored manifest must repeat (Read).
type Manifest struct {
	Created      string        `json:"created_date"`
	Artifacts    []Artifact    `json:"model_artifacts"`
	Format       Serialization `json:"model_format"`
	Dependencies []Artifact    `json:"dependency_artifacts,omitempty"`
	Code         *Artifact     `json:"code_artifacts,omitempty"`
	ID           string        `json:"id,omitempty"`
	Version      string        `json:"version,omitempty"`
}

// FileEntry is a declared file's path below the version's folder, category
// included, and the artifact that declares it.
type FileEntry struct {
	Path     string
	Artifact Artifact
}

// Files lists the declared files in manifest order: the model artifacts
// under files/, the dependencies under dependencies/ and the code artifact
// under code/.
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

// Decode reads one manifest of at most MaxManifestSize bytes and refuses
// (400) one that is malformed, ambiguous or out of bounds: a duplicate or
// upper-case key, nesting deeper than 32 levels, a creation time in neither
// RFC 3339 nor the SDK's zone-less UTC form, no model artifact or more than
// MaxFiles files, missing or invalid serialization metadata, or a file with
// an invalid path, a size outside 0 to MaxFileSize, a SHA-256 that is not
// 64 lower-case hexadecimal characters, or a path declared twice.
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
) (Manifest, model.Item, error) {
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
) ([]model.Item, error) {
	files := make([]model.Item, 0, len(m.Files()))
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

// Missing reports whether err, from Read or Verify, means an item is not
// stored (kernel.ErrNotFound): the version has no manifest or has lost a
// declared file, so it is unpublished or withdrawn rather than broken.
func Missing(err error) bool { return errors.Is(err, kernel.ErrNotFound) }

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
