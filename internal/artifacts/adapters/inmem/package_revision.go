package inmem

import (
	"crypto/rand"

	"github.com/weaveplatform/artifactweave/internal/platform/tx/memtx"
)

// The table joins the same unit of work as the represented version and links;
// rollback restores these opaque markers along with the content mutations.
func (p packages) revisions() *memtx.Table[int64, string] {
	return memtx.TableOf[int64, string](p.s.Store, "package_version_revisions")
}

func (p packages) versionChanged(id int64) { p.revisions().Put(id, rand.Text()) }

// Call under the store lock before deleting an item's links.
func (i items) versionsChanged(id int64) {
	p := packages(i)
	for link := range p.files().Rows {
		if link.ItemID == id {
			p.versionChanged(link.VersionID)
		}
	}
}
