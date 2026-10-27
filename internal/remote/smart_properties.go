package remote

import (
	"context"
	"encoding/json"
	"errors"
	"net/url"
	"strings"
	"time"

	"github.com/weaveplatform/artifactweave/internal/source"
	"github.com/weaveplatform/artifactweave/pkg/repo"
	"github.com/weaveplatform/artifactweave/pkg/store"
	"github.com/weaveplatform/artifactweave/pkg/types"
)

func smartDue(q source.Request, state *types.RemoteSmartState, now time.Time) bool {
	properties := smartProperties(q.Repo) && smartPropertyPath(q)
	absence := smartAbsence(q.Repo)
	if !properties && !absence {
		return false
	}
	if state == nil {
		return true
	}
	ttl := smartTTL(q.Repo)
	return properties && !now.Before(state.PropertiesAt.Add(ttl)) ||
		absence && !now.Before(state.OriginAt.Add(ttl))
}

func smartStoragePath(q source.Request) string {
	// A Maven layout translation is also the source's storage path. Registry
	// UpstreamPath is an API route, while Path names its canonical stored file.
	if q.Resource.UpstreamPath != "" && q.Repo.PackageType != repo.Docker &&
		q.Repo.PackageType != repo.OCI && q.Repo.PackageType != repo.HelmOCI {
		path, err := url.PathUnescape(q.Resource.UpstreamPath)
		if err == nil {
			return path
		}
	}
	return q.Resource.Path
}

func (s *Smart) properties(
	ctx context.Context,
	q source.Request,
	permit source.Permit,
	opts Options,
	peer SmartPeer,
) (types.Properties, bool, error) {
	target, err := peer.StorageURL(smartStoragePath(q), url.Values{"properties": {""}})
	if err != nil {
		return nil, false, err
	}
	doc, noStore, err := s.document(ctx, q, permit, opts, target)
	if errors.Is(err, store.ErrNotFound) {
		// The documented properties API returns 404 for an existing item with
		// no properties too. Only a separate item-info miss confirms absence.
		present, e := s.present(ctx, q, permit, opts, peer)
		return make(types.Properties), present, e
	}
	if err != nil {
		return nil, false, err
	}
	if noStore {
		return nil, false, source.ErrUnavailable
	}
	var props types.Properties
	if json.Unmarshal(doc["properties"], &props) != nil || props == nil || len(props) > 1024 {
		return nil, false, source.ErrInvalidResult
	}
	for key, values := range props {
		if types.ValidatePropertyKey(key) != nil || len(values) == 0 || len(values) > 64 {
			return nil, false, source.ErrInvalidResult
		}
		for i, value := range values {
			values[i] = types.TruncatePropertyValue(value, types.PropertyValueMax)
		}
		props.Set(key, values...)
		if smartSystemProperty(key) {
			delete(props, key)
		}
	}
	return props, true, nil
}

func smartSystemProperty(key string) bool {
	for _, prefix := range []string{"artifactory.", "artifactweave.", "docker.", "oci.", "npm.", "pypi.", "nuget.", "maven."} {
		if strings.HasPrefix(key, prefix) {
			return true
		}
	}
	return false
}

func (s *Smart) present(
	ctx context.Context,
	q source.Request,
	permit source.Permit,
	opts Options,
	peer SmartPeer,
) (bool, error) {
	target, err := peer.StorageURL(smartStoragePath(q), nil)
	if err != nil {
		return false, err
	}
	doc, noStore, err := s.document(ctx, q, permit, opts, target)
	if errors.Is(err, store.ErrNotFound) {
		return false, nil
	}
	if err != nil {
		return false, err
	}
	var path, repository string
	_ = json.Unmarshal(doc["path"], &path)
	_ = json.Unmarshal(doc["repo"], &repository)
	if noStore || strings.TrimPrefix(path, "/") != smartStoragePath(q) ||
		strings.TrimSuffix(repository, types.CacheSuffix) != peer.Repository {
		return false, source.ErrInvalidResult
	}
	return true, nil
}
