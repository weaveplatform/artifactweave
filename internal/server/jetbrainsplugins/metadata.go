package jetbrainsplugins

import (
	"context"
	"encoding/json"
	"io"

	"github.com/weaveplatform/artifactweave/internal/source"
)

const maxMetadataBytes = 16 << 20

func metadataBytes(body io.ReadSeeker, size int64) ([]byte, error) {
	if size < 0 || size > maxMetadataBytes {
		return nil, source.ErrInvalidResult
	}
	data, err := io.ReadAll(io.LimitReader(body, maxMetadataBytes+1))
	if err != nil || int64(len(data)) != size {
		return nil, source.ErrInvalidResult
	}
	return data, nil
}

func validateCompatible(expectedID string) func(context.Context, io.ReadSeeker, int64) error {
	return func(_ context.Context, body io.ReadSeeker, size int64) error {
		data, err := metadataBytes(body, size)
		if err != nil {
			return err
		}
		var updates []struct {
			ID          int64  `json:"id"`
			PluginID    int64  `json:"pluginId"` //nolint:tagliatelle // Marketplace wire spelling.
			Version     string `json:"version"`
			PluginXMLID string `json:"pluginXmlId"` //nolint:tagliatelle // Marketplace wire spelling.
		}
		if json.Unmarshal(data, &updates) != nil || updates == nil || len(updates) > 100 {
			return source.ErrInvalidResult
		}
		for _, u := range updates {
			if u.ID <= 0 || u.PluginID <= 0 || u.PluginXMLID != expectedID ||
				!safeVersion(u.Version) {
				return source.ErrInvalidResult
			}
		}
		return nil
	}
}

func validateSearch(_ context.Context, body io.ReadSeeker, size int64) error {
	data, err := metadataBytes(body, size)
	if err != nil {
		return err
	}
	var plugins []struct {
		ID       int64  `json:"id"`
		XMLID    string `json:"xmlId"`    //nolint:tagliatelle // Marketplace wire spelling.
		UpdateID int64  `json:"updateId"` //nolint:tagliatelle // Marketplace wire spelling.
		Name     string `json:"name"`
	}
	if json.Unmarshal(data, &plugins) != nil || plugins == nil || len(plugins) > 500 {
		return source.ErrInvalidResult
	}
	for _, p := range plugins {
		if p.ID <= 0 || p.UpdateID <= 0 || !safePluginID(p.XMLID) ||
			p.Name == "" || len(p.Name) > 512 {
			return source.ErrInvalidResult
		}
	}
	return nil
}

func validatePluginIDs(_ context.Context, body io.ReadSeeker, size int64) error {
	data, err := metadataBytes(body, size)
	if err != nil {
		return err
	}
	var ids []string
	if json.Unmarshal(data, &ids) != nil || ids == nil || len(ids) > 500_000 {
		return source.ErrInvalidResult
	}
	for _, id := range ids {
		if !safePluginID(id) {
			return source.ErrInvalidResult
		}
	}
	return nil
}

func validateUpdateMetadata(expectedID string) func(context.Context, io.ReadSeeker, int64) error {
	return func(_ context.Context, body io.ReadSeeker, size int64) error {
		data, err := metadataBytes(body, size)
		if err != nil {
			return err
		}
		var m struct {
			ID      int64  `json:"id"`
			XMLID   string `json:"xmlId"` //nolint:tagliatelle // Marketplace wire spelling.
			Name    string `json:"name"`
			Version string `json:"version"`
			Since   string `json:"since"`
			Until   string `json:"until"`
			Size    int64  `json:"size"`
		}
		if json.Unmarshal(data, &m) != nil || m.ID <= 0 || !safePluginID(m.XMLID) ||
			m.Name == "" || len(m.Name) > 512 || !safeVersion(m.Version) ||
			!safeBuild(m.Since) || m.Until != "" && !safeBuildWildcard(m.Until) ||
			m.Size <= 0 || m.Size > maxArchiveBytes || decimal(m.ID) != expectedID {
			return source.ErrInvalidResult
		}
		return nil
	}
}

func safeVersion(version string) bool {
	return len(version) <= 128 && safeFilename(version)
}

func safeBuildWildcard(build string) bool {
	if len(build) > 64 {
		return false
	}
	for _, ch := range build {
		if ch == '*' || ch >= 'a' && ch <= 'z' || ch >= 'A' && ch <= 'Z' ||
			ch >= '0' && ch <= '9' || ch == '-' || ch == '.' || ch == '_' {
			continue
		}
		return false
	}
	return build != ""
}
