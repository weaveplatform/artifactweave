package registry

import (
	"bytes"
	"context"
	"errors"
	"fmt"
	"strings"

	layout "github.com/weaveplatform/artifactweave/internal/format/docker"
	"github.com/weaveplatform/artifactweave/internal/service/item"
	"github.com/weaveplatform/artifactweave/pkg/blob"
	"github.com/weaveplatform/artifactweave/pkg/store"
	"github.com/weaveplatform/artifactweave/pkg/types"
)

// BlobsUnknownError is a manifest that references blobs the image does
// not hold: MANIFEST_BLOB_UNKNOWN, once for each digest.
type BlobsUnknownError struct {
	Digests []string
}

func (e *BlobsUnknownError) Error() string {
	return "registry: manifest references unknown blobs: " + strings.Join(e.Digests, ", ")
}

// Push is a manifest to store in an image.
type Push struct {
	Owner
	Manifest layout.Manifest
	// Body is the manifest exactly as the client sent it, which jf reads
	// back to take the config's digest (research 23, 5.1).
	Body []byte
	// Folders are where it is stored, each a direct child of the image: a
	// tag, or the digest folder with any tags pushed alongside it.
	Folders []string
}

// Replaces reports whether storing the manifest whose SHA-256 is hex in
// folder of o's image would replace what the folder holds: a different
// manifest, or files of any kind. Replacing needs Delete as well as Deploy
// (DV-035); pushing the manifest a folder already holds changes nothing,
// so a client repeating a push needs no more than Deploy.
func (s *Service) Replaces(ctx context.Context, o Owner, folder, hex string) (bool, error) {
	exists, same, err := s.folderState(ctx, o, folder, hex)
	return exists && !same, err
}

// folderState looks at folder of o's image: whether it exists as a folder,
// and whether it holds the manifest hex.
func (s *Service) folderState(
	ctx context.Context,
	o Owner,
	folder, hex string,
) (bool, bool, error) {
	items := s.Store.Items()
	dir := o.Name + "/" + folder
	f, err := items.Get(ctx, o.Repo.Key, false, dir)
	if errors.Is(err, store.ErrNotFound) || (err == nil && !f.Folder) {
		// A file in the way is the deploy's conflict to report.
		return false, false, nil
	}
	if err != nil {
		return false, false, fmt.Errorf("registry: get %s: %w", dir, err)
	}
	for _, file := range []string{layout.ManifestFile, layout.ListManifestFile} {
		it, err := items.Get(ctx, o.Repo.Key, false, dir+"/"+file)
		if errors.Is(err, store.ErrNotFound) {
			continue
		}
		if err != nil {
			return true, false, fmt.Errorf("registry: get %s/%s: %w", dir, file, err)
		}
		return true, it.Actual.SHA256 == hex, nil
	}
	return true, false, nil
}

// PutManifest stores p's manifest in each of its folders (decision 0031):
// the exact bytes as manifest.json, or list.manifest.json for an index or
// manifest list, with the properties jf and Docker clients read, and an
// image's config and layers linked beside it by reference to their stored
// content, never copied. Every blob an image needs, and every manifest an
// index lists, must already be in the image (a *BlobsUnknownError
// otherwise); a subject need not be. A folder holding the same manifest is
// left as it is; one holding another manifest, or anything else, is
// replaced, keeping a tag's previous manifest under Tag Retention. The
// repository's tag limits then apply to the image, all in one transaction.
func (s *Service) PutManifest(ctx context.Context, p Push) error {
	m := p.Manifest
	if err := s.childrenPresent(ctx, p.Owner, m.Children()); err != nil {
		return err
	}
	found, err := s.present(ctx, p.Owner, m.Blobs())
	if err != nil {
		return err
	}
	var labels map[string]string
	if !m.Index() {
		labels = s.labels(ctx, m)
	}
	info, err := s.commitBody(ctx, p.Body, m.Digest)
	if err != nil {
		return err
	}
	items := s.items()
	err = s.Store.Tx(ctx, func(ctx context.Context) error {
		for _, folder := range p.Folders {
			if err := s.putFolder(ctx, items, p, folder, labels, found, info); err != nil {
				return err
			}
		}
		return s.retain(ctx, items, p)
	})
	if err != nil {
		return err //nolint:wrapcheck // the transaction returns ours
	}
	return nil
}

// present finds each blob digest in o's image and returns the SHA-256s it
// found, or a *BlobsUnknownError naming those it did not. A digest under
// another algorithm cannot be stored here, so it is never found.
func (s *Service) present(ctx context.Context, o Owner, digests []string) ([]string, error) {
	var found, missing []string
	for _, d := range digests {
		hex, ok := layout.SHA256Hex(d)
		if ok {
			_, err := layout.FindBlob(ctx, s.Store.Items(), o.Repo.Key, o.Name, hex)
			switch {
			case err == nil:
				found = append(found, hex)
				continue
			case !errors.Is(err, store.ErrNotFound):
				return nil, err //nolint:wrapcheck // the layout names itself
			}
		}
		missing = append(missing, d)
	}
	if len(missing) > 0 {
		return nil, &BlobsUnknownError{Digests: missing}
	}
	return found, nil
}

// childrenPresent checks that each manifest an index lists is stored in o's
// image, by tag or by digest: clients push the platform manifests first,
// and jf finds them there (research 23, 5.1). Those missing are a
// *BlobsUnknownError, MANIFEST_BLOB_UNKNOWN as decision 0031 answers it; a
// digest under another algorithm cannot be stored here, so it is missing.
func (s *Service) childrenPresent(ctx context.Context, o Owner, digests []string) error {
	var missing []string
	for _, d := range digests {
		if hex, ok := layout.SHA256Hex(d); ok {
			_, err := layout.FindManifest(ctx, s.Store.Items(), o.Repo.Key, o.Name, hex)
			switch {
			case err == nil:
				continue
			case !errors.Is(err, store.ErrNotFound):
				return err //nolint:wrapcheck // the layout names itself
			}
		}
		missing = append(missing, d)
	}
	if len(missing) > 0 {
		return &BlobsUnknownError{Digests: missing}
	}
	return nil
}

// labels reads the image's labels from its config blob, which present
// found in the image. A config that cannot be read gives none: labels
// never fail a push.
func (s *Service) labels(ctx context.Context, m layout.Manifest) map[string]string {
	hex, _ := layout.SHA256Hex(m.Config.Digest)
	r, _, err := s.Blobs.Open(ctx, hex)
	if err != nil {
		return nil
	}
	defer func() { _ = r.Close() }()
	return layout.Labels(m.ConfigType(), r)
}

// commitBody stores the manifest's bytes in the blob store, verified
// against its digest, before the transaction that records it.
func (s *Service) commitBody(ctx context.Context, body []byte, hex string) (blob.Info, error) {
	up, err := s.Blobs.BeginUpload(ctx, blob.WithExpectedSHA256(hex))
	if err != nil {
		return blob.Info{}, fmt.Errorf("registry: begin manifest upload: %w", err)
	}
	return commit(ctx, up, bytes.NewReader(body), hex)
}

// putFolder writes the manifest into one folder inside PutManifest's
// transaction.
func (s *Service) putFolder(
	ctx context.Context,
	items *item.Service,
	p Push,
	folder string,
	labels map[string]string,
	blobs []string,
	info blob.Info,
) error {
	m := p.Manifest
	dir := p.Name + "/" + folder
	exists, same, err := s.folderState(ctx, p.Owner, folder, m.Digest)
	switch {
	case err != nil:
		return err
	case same:
		return nil
	case exists:
		if err := s.replace(ctx, items, p, folder); err != nil {
			return err
		}
	}
	_, err = items.DeployCommitted(ctx, item.Deploy{
		Target:     item.Target{Repo: p.Repo, Path: dir + "/" + m.File()},
		Declared:   types.Checksums{SHA256: m.Digest},
		Properties: layout.Properties(m, p.Name, folder, labels),
		By:         p.By,
	}, info)
	if err != nil {
		return fmt.Errorf("registry: store manifest in %s: %w", dir, err)
	}
	for _, hex := range blobs {
		_, err := items.DeployByChecksum(ctx, item.Deploy{
			Target:   item.Target{Repo: p.Repo, Path: dir + "/" + layout.BlobFile(hex)},
			Declared: types.Checksums{SHA256: hex},
			By:       p.By,
		})
		if errors.Is(err, item.ErrNoBlob) {
			// Garbage collection took the content since it was found.
			return &BlobsUnknownError{Digests: []string{"sha256:" + hex}}
		}
		if err != nil {
			return fmt.Errorf("registry: link %s into %s: %w", hex, dir, err)
		}
	}
	return nil
}
