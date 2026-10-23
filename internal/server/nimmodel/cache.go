package nimmodel

import (
	"strings"

	"github.com/weaveplatform/artifactweave/internal/service/item"
	"github.com/weaveplatform/artifactweave/internal/source"
	"github.com/weaveplatform/artifactweave/pkg/repo"
	"github.com/weaveplatform/artifactweave/pkg/types"
)

// PrepareUpload publishes validated NIM metadata in the documented cache
// layout. The two identity properties support Artifactory's property search
// by model name or version without trusting fields supplied by an upstream.
// Other Nimmodel.* fields require a target response capture before mapping.
func PrepareUpload(q source.Request, u *item.CacheUpload) {
	if q.Repo.PackageType != repo.NIMModel || u.Kind != types.RemoteMetadata {
		return
	}
	parts := strings.Split(q.Resource.Path, "/")
	switch q.Resource.Variant {
	case "nim-tag-metadata-v1":
		if len(parts) < 5 || parts[len(parts)-1] != ".jfrog_nim_model_info.json" {
			return
		}
		u.Properties = types.Properties{
			"Nimmodel.name":      {parts[len(parts)-3]},
			"Nimmodel.versionId": {parts[len(parts)-2]},
		}
	case "nim-repository-v1":
		if len(parts) < 4 || parts[len(parts)-1] != "repo.json" {
			return
		}
		u.Properties = types.Properties{"Nimmodel.name": {parts[len(parts)-2]}}
	default:
		return
	}
	u.PublishMetadata = true
}
