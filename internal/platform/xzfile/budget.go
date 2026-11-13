package xzfile

import (
	"container/list"
	"context"
	"sync"
)

// DefaultBudget is the dictionary budget a process starts with, and the
// default of the xz.dictionary_budget setting: 1 GiB. It lets one decode
// of a block that names up to 1 GiB, or a hundred and twenty-eight of
// xz -6's 8 MiB, hold its dictionary at once; a block that names more, up
// to MaxDict, decodes alone.
const DefaultBudget int64 = 1 << 30

// dictionaries is the process's budget. SetBudget sizes it once at start;
// tests size it for a test.
var dictionaries = &budget{size: DefaultBudget, waiting: list.New()}

// SetBudget sets how many bytes of LZMA2 dictionary the process's xz
// decodes may hold at once; 0 turns the budget off. A decode whose
// dictionary does not fit waits for others to give theirs back, and is
// never refused for it: Artifactory decodes xz with XZ for Java, which
// has no such bound, so a refusal would change an answer (#1354).
func SetBudget(n int64) {
	b := dictionaries
	b.mu.Lock()
	defer b.mu.Unlock()
	b.size = max(n, 0)
	b.grant()
}

// WithRequest returns ctx with a holder of the budget of its own, so the
// readers opened with it, or with a context made from it, count as one
// holder: one request. Without it each reader is a holder of its own.
//
// A holder already holding part of the budget is let past the decodes
// queued ahead of it, and in once nothing but it holds the budget, even
// beyond the budget. Without that, a request that reads a second xz file
// while a first is open would wait behind a decode that waits for the
// first one's dictionary, which only the waiting request can give back.
func WithRequest(ctx context.Context) context.Context {
	return context.WithValue(ctx, holderKey{}, &holder{})
}

type holderKey struct{}

// holder is one request's share of the budget.
type holder struct{ held int64 }

// budget is a weighted semaphore over dictionary bytes. It admits in
// order of arrival, so a large dictionary is not passed over for ever by
// smaller ones, except for a holder already inside (see WithRequest).
type budget struct {
	mu      sync.Mutex
	size    int64
	used    int64
	waiting *list.List // of *waiter
}

type waiter struct {
	h     *holder
	n     int64
	taken bool
	ready chan struct{}
}

// account is the part of the budget one reader holds: its current block's
// dictionary, or nothing between blocks. It is kept apart from the reader
// so a cleanup can give it back once the reader is unreachable.
type account struct {
	b *budget
	h *holder
	n int64
}

func newAccount(ctx context.Context) *account {
	h, ok := ctx.Value(holderKey{}).(*holder)
	if !ok {
		h = &holder{}
	}
	return &account{b: dictionaries, h: h}
}

// take waits until n bytes of dictionary fit, or ctx is done. A decode
// that fits at once goes ahead whatever ctx says, as it did before there
// was a budget. A wait that ctx ends takes nothing and returns ctx's
// error; if the budget was granted as ctx ended, it is given back.
func (a *account) take(ctx context.Context, n int64) error {
	b := a.b
	b.mu.Lock()
	if b.size == 0 {
		b.mu.Unlock()
		return nil
	}
	w := &waiter{h: a.h, n: n, ready: make(chan struct{})}
	b.waiting.PushBack(w)
	b.grant()
	if w.taken {
		a.n = w.n
		b.mu.Unlock()
		return nil
	}
	b.mu.Unlock()
	select {
	case <-w.ready:
		b.mu.Lock()
		a.n = w.n
		b.mu.Unlock()
		return nil
	case <-ctx.Done():
	}
	b.mu.Lock()
	defer b.mu.Unlock()
	if w.taken {
		b.giveBack(a.h, w.n)
	} else {
		for e := b.waiting.Front(); e != nil; e = e.Next() {
			if e.Value == w {
				b.waiting.Remove(e)
				break
			}
		}
	}
	// Leaving the queue may let the waiters behind this one in.
	b.grant()
	return ctx.Err() //nolint:wrapcheck // the caller's own cancellation, as it gave it
}

// release gives back what a holds. It is safe to call more than once, and
// from a cleanup.
func (a *account) release() {
	b := a.b
	b.mu.Lock()
	defer b.mu.Unlock()
	if a.n == 0 {
		return
	}
	b.giveBack(a.h, a.n)
	a.n = 0
	b.grant()
}

func (b *budget) giveBack(h *holder, n int64) {
	b.used -= n
	h.held -= n
}

// grant lets in, in order, every waiter whose dictionary fits, or that
// would be the only holder inside. Once one does not get in, the waiters
// behind it wait too, except a holder already inside, which may pass.
// With the budget off every waiter goes in, and holds nothing.
func (b *budget) grant() {
	blocked := false
	for e := b.waiting.Front(); e != nil; {
		w, next := e.Value.(*waiter), e.Next()
		switch {
		case b.size == 0:
			w.n = 0
			b.admit(e, w)
		case (!blocked || w.h.held > 0) && (b.used+w.n <= b.size || b.used == w.h.held):
			b.admit(e, w)
		case w.h.held == 0:
			blocked = true
		}
		e = next
	}
}

func (b *budget) admit(e *list.Element, w *waiter) {
	b.waiting.Remove(e)
	b.used += w.n
	w.h.held += w.n
	w.taken = true
	close(w.ready)
}
