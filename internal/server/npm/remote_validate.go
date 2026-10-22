package npm

import (
	"context"
	"encoding/json"
	"io"

	npmfmt "github.com/weaveplatform/artifactweave/internal/format/npm"
	"github.com/weaveplatform/artifactweave/pkg/repo"
)

// Admission validates every version independently of caller authorization. Raw
// shared metadata cannot become permanently invalid for another caller merely
// because the first reader could not see the malformed version.
func validatePackageMetadata(
	c repo.Config,
	name string,
) func(context.Context, io.ReadSeeker, int64) error {
	return func(_ context.Context, r io.ReadSeeker, size int64) error {
		if size > remoteMetadataLimit {
			return remoteProtocolError()
		}
		doc, err := decodeRemoteDocument(r)
		if err != nil {
			return err
		}
		var got string
		if !npmfmt.ValidName(name) || json.Unmarshal(doc["name"], &got) != nil || got != name {
			return remoteProtocolError()
		}
		var versions map[string]remoteDocument
		if json.Unmarshal(doc["versions"], &versions) != nil || versions == nil {
			return remoteProtocolError()
		}
		for version, manifest := range versions {
			if _, err = remoteDist(c, name, version, manifest); err != nil {
				return err
			}
		}
		var tags map[string]string
		if json.Unmarshal(doc["dist-tags"], &tags) != nil || tags == nil {
			return remoteProtocolError()
		}
		for _, version := range tags {
			if _, ok := versions[version]; !ok {
				return remoteProtocolError()
			}
		}
		return nil
	}
}

func validateSearchMetadata(_ context.Context, r io.ReadSeeker, size int64) error {
	if size > remoteMetadataLimit {
		return remoteProtocolError()
	}
	doc, err := decodeRemoteDocument(r)
	if err != nil {
		return err
	}
	var objects []remoteDocument
	if json.Unmarshal(doc["objects"], &objects) != nil || objects == nil {
		return remoteProtocolError()
	}
	for _, entry := range objects {
		var pkg struct {
			Name    string `json:"name"`
			Version string `json:"version"`
		}
		if json.Unmarshal(entry["package"], &pkg) != nil || !npmfmt.ValidName(pkg.Name) {
			return remoteProtocolError()
		}
		if _, valid := npmfmt.CleanVersion(pkg.Version); !valid {
			return remoteProtocolError()
		}
	}
	return nil
}

func decodeRemoteDocument(r io.Reader) (remoteDocument, error) {
	raw, err := io.ReadAll(io.LimitReader(r, remoteMetadataLimit+1))
	var doc remoteDocument
	// Unmarshal consumes the complete representation, rejecting trailing JSON or
	// junk before raw-cache publication, not only while serving the first caller.
	if err != nil || len(raw) > remoteMetadataLimit || json.Unmarshal(raw, &doc) != nil ||
		doc == nil {
		return nil, remoteProtocolError()
	}
	return doc, nil
}
