// Bridge (P9d): declared in migration/artifacts/adapters/storekit; PRO-02 deletes it.

package store

import "github.com/weaveplatform/artifactweave/migration/artifacts/adapters/storekit"

type RemoteCacheRecords = storekit.RemoteCacheRecords

var (
	NewRemoteCache     = storekit.NewRemoteCache
	FenceLayoutSources = storekit.FenceLayoutSources
)
