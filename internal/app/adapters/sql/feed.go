package sql

import (
	"sync/atomic"

	artapi "github.com/weaveplatform/artifactweave/internal/artifacts/api"
)

// feedBox holds the ItemFeed in one pointer, so a store's runtime can set
// it while servers already read it.
type feedBox struct{ artapi.ItemFeed }

// itemFeed is the store's ItemFeed slot (SetItemFeed). It lives on the
// store because every item service the runtime builds over the store must
// report to the same feed (item.Store).
type itemFeed struct{ p atomic.Pointer[feedBox] }

// SetItemFeed makes f hear of every client change to an item made through
// this store (artapi.ItemFeed); nil stops it. The runtime sets the
// federation module's once stream delivery is open.
func (s *Store) SetItemFeed(f artapi.ItemFeed) {
	if f == nil {
		s.feed.p.Store(nil)
		return
	}
	s.feed.p.Store(&feedBox{f})
}

// ItemFeed is what SetItemFeed set, nil before it.
func (s *Store) ItemFeed() artapi.ItemFeed {
	if b := s.feed.p.Load(); b != nil {
		return b.ItemFeed
	}
	return nil
}
