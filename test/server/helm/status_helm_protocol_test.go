package helm_test

import (
	"context"
	"testing"

	helmfmt "github.com/weaveplatform/artifactweave/internal/format/helm"
	"github.com/weaveplatform/artifactweave/internal/service/item"
	"github.com/weaveplatform/artifactweave/pkg/blob"
	"github.com/weaveplatform/artifactweave/pkg/store"
	"github.com/weaveplatform/artifactweave/pkg/types"
)

const (
	helmIndex        = "GET /artifactory/api/helm/{repoKey}/index.yaml"
	helmIndexHead    = "HEAD /artifactory/api/helm/{repoKey}/index.yaml"
	helmDownload     = "GET /artifactory/api/helm/{repoKey}/{path}"
	helmDownloadHead = "HEAD /artifactory/api/helm/{repoKey}/{path}"
)

var helmProtocolStatusPart = statusPart{
	handwritten: map[string]bool{
		helmIndex:        true,
		helmIndexHead:    true,
		helmDownload:     true,
		helmDownloadHead: true,
	},
	seed: func(t *testing.T, db store.Store, blobs blob.Store) {
		t.Helper()
		items := &item.Service{Store: db, Blobs: blobs}
		seedHelm(t, items, db, "helm-local", true, "protocol/web-1.0.0.tgz")
		seedHelmIndex(t, items)
	},
	rows: func() []statusCase {
		const root = "/artifactory/api/helm/helm-local/"
		return []statusCase{
			{helmIndex, root + "index.yaml", "admin", "", 200},
			{helmIndex, root + "index.yaml", "user", "", 403},
			{helmIndex, root + "index.yaml", "anonymous", "", 401},
			{helmIndex, "/artifactory/api/helm/missing/index.yaml", "admin", "", 404},
			{helmIndex, "/artifactory/api/helm/libs-local/index.yaml", "admin", "", 404},
			{helmIndex, "/artifactory/api/helm/helm-remote/index.yaml", "admin", "", 501},
			{helmIndex, "/artifactory/api/helm/helm-virtual/index.yaml", "admin", "", 501},
			{helmIndexHead, root + "index.yaml", "admin", "", 200},
			{helmIndexHead, root + "index.yaml", "user", "", 403},
			{helmDownload, root + "protocol/web-1.0.0.tgz", "admin", "", 200},
			{helmDownload, root + "protocol/web-1.0.0.tgz.prov", "admin", "", 200},
			{helmDownload, root + "missing.tgz", "admin", "", 404},
			{helmDownload, root + "protocol/web-1.0.0.tgz", "user", "", 403},
			{helmDownloadHead, root + "protocol/web-1.0.0.tgz", "admin", "", 200},
			{helmDownloadHead, root + "protocol/web-1.0.0.tgz", "user", "", 403},
		}
	},
}

func seedHelmIndex(t *testing.T, items *item.Service) {
	t.Helper()
	if err := (helmfmt.Handler{}).Work(
		context.Background(),
		items.Env(),
		types.WorkItem{Kind: helmfmt.KindIndex, Key: "helm-local"},
	); err != nil {
		t.Fatal(err)
	}
}
