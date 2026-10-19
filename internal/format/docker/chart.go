package docker

import (
	"context"
	"encoding/json"
	"io"

	"github.com/weaveplatform/artifactweave/internal/format"
	"github.com/weaveplatform/artifactweave/pkg/types"
)

// MediaTypeHelmConfig is the config of a Helm chart pushed as an OCI
// artifact: its Chart.yaml as JSON, beside a chart layer and, for a signed
// chart, a provenance layer (Helm's pkg/registry/constants.go; research 30,
// 6.1). The registry stores a chart like any image; only the package index
// reads the config.
const MediaTypeHelmConfig = "application/vnd.cncf.helm.config.v1+json"

// Chart is the chart a tag holds, as its config names it. Version is the
// chart's own, which may hold the "+" that Helm writes "_" in the tag.
//
//nolint:tagliatelle // Chart.yaml's field names, which the config carries
type Chart struct {
	Name       string `json:"name"`
	Version    string `json:"version"`
	AppVersion string `json:"appVersion,omitempty"`
}

// tagMeta is a tag's metadata in the package index (decision 0039): the
// chart it holds, so a package API can show the chart's version where the
// tag is the version.
type tagMeta struct {
	Chart Chart `json:"chart"`
}

// tagMetadata returns the package index metadata of the tag whose manifest
// is m: for a Helm chart its config's name, version and appVersion, and nil
// for any other image, an index, and a config that names no chart. Content
// that cannot be read gives none, as a config gives no properties
// (ReadConfig): a push never fails for its metadata, and a reindex records
// it once the content reads again. Without a blob store, as in a hook run
// on a store alone, it reads nothing.
func tagMetadata(ctx context.Context, blobs format.Blobs, m types.Item) []byte {
	if blobs == nil || m.Name != ManifestFile {
		return nil
	}
	body := readBlob(ctx, blobs, m.Actual.SHA256, MaxManifestSize)
	// Helm sends its manifest without a mediaType, so only the config is
	// read rather than the whole manifest through ParseManifest.
	var w struct {
		Config *Descriptor `json:"config"`
	}
	if json.Unmarshal(body, &w) != nil || w.Config == nil ||
		w.Config.MediaType != MediaTypeHelmConfig {
		return nil
	}
	hex, ok := SHA256Hex(w.Config.Digest)
	if !ok {
		return nil
	}
	var c Chart
	if json.Unmarshal(readBlob(ctx, blobs, hex, maxConfigSize), &c) != nil ||
		c.Name == "" || c.Version == "" {
		return nil
	}
	// Three strings always marshal.
	out, _ := json.Marshal(tagMeta{Chart: c})
	return out
}

// readBlob reads the content stored under sha256, at most limit bytes: nil
// when it cannot be read, is not stored, which a manifest deployed through
// the item API may name, or is larger.
func readBlob(ctx context.Context, blobs format.Blobs, sha256 string, limit int) []byte {
	r, _, err := blobs.Open(ctx, sha256)
	if err != nil {
		return nil
	}
	defer func() { _ = r.Close() }()
	body, err := io.ReadAll(io.LimitReader(r, int64(limit)+1))
	if err != nil || len(body) > limit {
		return nil
	}
	return body
}
