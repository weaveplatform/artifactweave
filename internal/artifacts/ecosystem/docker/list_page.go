package docker

import (
	"context"
	"fmt"
	"strings"

	"github.com/weaveplatform/artifactweave/internal/artifacts/core/port"
)

// TagPages lists the tags of one image in the specification's order
// (Compare), reading the store a page of manifest folders at a time
// (#1402), so that a tags list holds the page it answers rather than every
// tag of the image. It is the paged form of Tags, which promotion uses to
// take every tag.
type TagPages struct {
	pages         port.DockerPageStore
	repoKey, name string
	batch         int
	cursor        port.DockerFolderQuery
	buf           []string
	done          bool
	// found is set once a folder holding a manifest, a tag or a digest
	// folder, has been read: the image is known.
	found bool
}

// TagsAfter returns the tags of the image name in repoKey that follow last
// in Compare's order, all of them when last is empty, reading batch folders
// a query.
func TagsAfter(
	pages port.DockerPageStore, repoKey, name, last string, batch int,
) *TagPages {
	// Fold, not the store's DockerFold, starts the listing: every tag is
	// ASCII, so the store orders tags as Compare does, and only Fold places
	// a last a client sends with other letters where Compare would.
	return &TagPages{
		pages: pages, repoKey: repoKey, name: name, batch: batch,
		cursor: port.DockerFolderQuery{
			RepoKey: repoKey, Image: name, AfterFold: Fold(last), After: last, Limit: batch,
		},
	}
}

// Next returns the next tag, or false when there are no more.
func (p *TagPages) Next(ctx context.Context) (string, bool, error) {
	for {
		folder, ok, err := p.folder(ctx)
		if err != nil || !ok {
			return "", false, err
		}
		if tag, _ := manifestFolder(folder); tag {
			return folder, true, nil
		}
	}
}

// folder returns the next folder holding a manifest, whatever its name.
func (p *TagPages) folder(ctx context.Context) (string, bool, error) {
	for len(p.buf) == 0 {
		if p.done {
			return "", false, nil
		}
		page, err := p.pages.ManifestFolders(ctx, p.cursor)
		if err != nil {
			return "", false, fmt.Errorf("docker: tags of %s: %w", p.name, err)
		}
		p.buf, p.done = page, len(page) < p.batch
	}
	folder := p.buf[0]
	p.buf = p.buf[1:]
	// The store's own fold continues after a folder it returned.
	p.cursor.AfterFold, p.cursor.After = port.DockerFold(folder), folder
	_, ok := manifestFolder(folder)
	p.found = p.found || ok
	return folder, true, nil
}

// foundBatch is how many folders Found reads a query: the first folder of
// an image is nearly always a tag or a digest folder.
const foundBatch = 16

// Found reports whether the image holds a manifest at all, in a tag or a
// digest folder: what the registry answers NAME_UNKNOWN for when it does
// not, whatever page was asked for. Once Next has read such a folder it
// needs no query.
func (p *TagPages) Found(ctx context.Context) (bool, error) {
	all := TagsAfter(p.pages, p.repoKey, p.name, "", foundBatch)
	for !p.found && !all.found {
		if _, ok, err := all.folder(ctx); err != nil || !ok {
			return false, err
		}
	}
	return true, nil
}

// ImagePages lists the images of one repository, each a folder holding a
// manifest in a tag or digest folder of its own (decision 0031), in byte
// order, reading the store a page of manifests at a time (#1402). Accept
// judges the images a catalog may list: those it refuses are skipped
// before the page is cut.
type ImagePages struct {
	pages  port.DockerPageStore
	batch  int
	accept func(string) bool
	cursor port.DockerImageQuery
	buf    []port.DockerImageManifest
	done   bool
	empty  bool
}

// ImagesAfter returns the images of repoKey that accept takes and that
// follow last in Compare's order, all of them when last is empty. With a
// prefix, as the registry-wide catalog names images <repo>/<image>, last
// is compared with the prefixed names. It reads batch manifests a query.
//
// The store orders images by their bytes. An image the catalog lists has a
// valid name, which is lower case, so that is Compare's order, and with a
// prefix too: one repository's names share it.
func ImagesAfter(
	pages port.DockerPageStore,
	repoKey, prefix, last string,
	accept func(string) bool,
	batch int,
) *ImagePages {
	p := &ImagePages{pages: pages, batch: batch, accept: accept}
	p.cursor = port.DockerImageQuery{RepoKey: repoKey, Limit: batch}
	if last == "" {
		return p
	}
	// A name follows last when its fold does, or when the folds are equal
	// and its bytes follow last's. Below the prefix every name is lower
	// case, so the image must follow, or equal and then pass, last's fold
	// past the prefix's. A last whose fold leaves the prefix's names puts
	// all of them on one side of it.
	folded, foldedPrefix := Fold(last), Fold(prefix)
	rest, within := strings.CutPrefix(folded, foldedPrefix)
	switch {
	case within:
		p.cursor.After = rest
		p.cursor.Within = prefix+rest > last
	case foldedPrefix < folded:
		p.empty = true
	}
	return p
}

// Next returns the next image, or false when there are no more.
func (p *ImagePages) Next(ctx context.Context) (string, bool, error) {
	if p.empty {
		return "", false, nil
	}
	for {
		if len(p.buf) == 0 {
			if p.done {
				return "", false, nil
			}
			page, err := p.pages.ImageManifests(ctx, p.cursor)
			if err != nil {
				return "", false, fmt.Errorf("docker: images of %s: %w", p.cursor.RepoKey, err)
			}
			p.buf, p.done = page, len(page) < p.batch
			continue
		}
		m := p.buf[0]
		p.buf = p.buf[1:]
		p.cursor.After, p.cursor.Within, p.cursor.AfterFolder = m.Image, true, m.Folder
		_, ok := manifestFolder(m.Folder)
		accepted := p.accept(m.Image)
		if !ok && accepted {
			// Another folder of the image may still hold a manifest.
			continue
		}
		// The image is listed or refused once: its other manifests are
		// skipped, in this page and by the next query.
		for len(p.buf) > 0 && p.buf[0].Image == m.Image {
			p.buf = p.buf[1:]
		}
		p.cursor.Within, p.cursor.AfterFolder = false, ""
		if accepted {
			return m.Image, true, nil
		}
	}
}
