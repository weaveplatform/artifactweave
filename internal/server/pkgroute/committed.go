package pkgroute

import (
	"net/http"

	"github.com/weaveplatform/artifactweave/internal/auth"
	"github.com/weaveplatform/artifactweave/internal/server/apierror"
	"github.com/weaveplatform/artifactweave/internal/service/item"
	"github.com/weaveplatform/artifactweave/pkg/blob"
	"github.com/weaveplatform/artifactweave/pkg/types"
)

// StoreResolved commits an upload whose inspection determines its destination.
// The resolved path needs Deploy before the transaction, while the item service
// checks Delete against the content actually present inside that transaction.
// The caller has already verified declared against info, including any digest
// algorithms the item service does not support.
func (k *Kit) StoreResolved(
	r *http.Request,
	filename string,
	info blob.Info,
	declared types.Checksums,
	resolve func(any) (string, error),
) (types.Item, error) {
	d := k.fileDeploy(r, filename, nil, declared)
	d.ResolvePath = func(inspection any) (string, error) {
		path, err := resolve(inspection)
		if err != nil {
			return "", err
		}
		if !can(r, d.Repo, path, types.ActionWrite) {
			return "", refusal(r)
		}
		return path, nil
	}
	it, err := k.o.Items().DeployCommitted(r.Context(), d, info)
	if err != nil {
		return types.Item{}, k.o.ItemError(r.Context(), "deploy", err)
	}
	return it, nil
}

// DeployItem delegates an item-path PUT to the server's complete deploy route,
// retaining checksum reuse, archive expansion, sidecars and folder semantics.
func (k *Kit) DeployItem(w http.ResponseWriter, r *http.Request) {
	if k.o.DeployItem == nil {
		k.Error(w, r, apierror.ErrNotImplemented)
		return
	}
	request := RequestFrom(r)
	k.o.DeployItem(w, r, request.Repo, request.Rest)
}

func (k *Kit) fileDeploy(
	r *http.Request,
	path string,
	props types.Properties,
	declared types.Checksums,
) item.Deploy {
	c := RequestFrom(r).Repo
	by, _ := auth.FromContext(r.Context())
	return item.Deploy{
		Target: item.Target{
			Repo: c,
			Path: path,
		},
		Properties:         props,
		Declared:           declared,
		By:                 by.Name,
		OverwriteIdentical: true,
		CanOverwritePath:   func(path string) bool { return can(r, c, path, types.ActionDelete) },
	}
}
