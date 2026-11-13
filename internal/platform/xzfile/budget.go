package xzfile

import (
	"container/list"
	"context"
	"sync"
)

// DefaultBudget is the dictionary budget a process starts with, and the
// default of the xz.dictionary_budget setting: 1 GiB. It lets one decode
// whose dictionary fills up to 1 GiB, or a hundred and twenty-eight that
// fill xz -6's 8 MiB, hold their dictionaries at once; one that fills
// more, up to MaxDict, grows alone. A file shorter than its dictionary
// holds only what it fills, from 64 KiB (#1413).
const DefaultBudget int64 = 1 << 30

// dictionaries is the process's budget. SetBudget sizes it once at start;
// tests size it for a test.
var dictionaries = &budget{size: DefaultBudget, waiting: list.New()}

// SetBudget sets how many bytes of LZMA2 dictionary the process's xz
// decodes may hold at once; 0 turns the budget off. A decode whose
// dictionary does not fit, as it starts or as it grows, waits for others
// to give theirs back, and is never refused for it: Artifactory decodes
// xz with XZ for Java, which has no such bound, so a refusal would change
// an answer (#1354).
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
// smaller ones, except for a holder already inside (see WithRequest),
// which a dictionary that grows always is.
type budget struct {
	mu      sync.Mutex
	size    int64
	used    int64
	waiting *list.List // of *waiter
	// holders counts the holders that hold part of the budget.
	holders int
	// alone is the holder let past the budget because every holder inside
	// waited to grow (stalled), until it holds nothing.
	alone *holder
}

type waiter struct {
	h     *holder
	n     int64
	taken bool
	ready chan struct{}
}

// account is the part of the budget one reader holds: what its current
// block's dictionary has grown to, or nothing between blocks. It is kept
// apart from the reader so a cleanup can give it back once the reader is
// unreachable.
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

// hold waits until the reader holds n bytes of dictionary in all, taking
// what it does not hold yet, or until ctx is done. A share that fits at
// once goes ahead whatever ctx says, as it did before there was a budget.
// A wait that ctx ends takes nothing more and returns ctx's error; if the
// share was granted as ctx ended, it is given back.
func (a *account) hold(ctx context.Context, n int64) error {
	b := a.b
	b.mu.Lock()
	if b.size == 0 || n <= a.n {
		b.mu.Unlock()
		return nil
	}
	w := &waiter{h: a.h, n: n - a.n, ready: make(chan struct{})}
	b.waiting.PushBack(w)
	b.grant()
	if w.taken {
		a.n += w.n
		b.mu.Unlock()
		return nil
	}
	b.mu.Unlock()
	select {
	case <-w.ready:
		b.mu.Lock()
		a.n += w.n
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
	if n > 0 && h.held == 0 {
		b.holders--
		if b.alone == h {
			b.alone = nil
		}
	}
}

// grant lets in, in order, every waiter whose share fits, or whose holder
// would be the only one inside, or is let past the budget alone. Once one
// does not get in, the waiters behind it wait too, except a holder already
// inside, which may pass. With the budget off every waiter goes in, and
// holds nothing.
//
// A dictionary that grows waits for its growth while it holds what it has
// grown to (#1413), so decodes that grow at once can each wait for what
// the others hold. When every holder inside waits, none of them will give
// any back, so the one that has waited longest goes in past the budget,
// and alone: no other goes past it until that one holds nothing. The
// budget is then exceeded by that one decode's dictionary at most.
func (b *budget) grant() {
	b.admitInOrder()
	if b.alone == nil && b.size > 0 {
		if h := b.stalled(); h != nil {
			b.alone = h
			b.admitInOrder()
		}
	}
}

func (b *budget) admitInOrder() {
	blocked := false
	for e := b.waiting.Front(); e != nil; {
		w, next := e.Value.(*waiter), e.Next()
		switch {
		case b.size == 0:
			w.n = 0
			b.admit(e, w)
		case (!blocked || w.h.held > 0) &&
			(b.used+w.n <= b.size || b.used == w.h.held || w.h == b.alone):
			b.admit(e, w)
		case w.h.held == 0:
			blocked = true
		}
		e = next
	}
}

// stalled returns the holder of the first waiter that holds part of the
// budget, if every holder that holds part of it is waiting for more. A
// holder whose readers run on more than one goroutine may be let in sooner
// than it needed, which costs memory but never a wait.
func (b *budget) stalled() *holder {
	var first *holder
	waiting := map[*holder]bool{}
	for e := b.waiting.Front(); e != nil; e = e.Next() {
		if h := e.Value.(*waiter).h; h.held > 0 {
			if first == nil {
				first = h
			}
			waiting[h] = true
		}
	}
	if len(waiting) < b.holders {
		return nil
	}
	return first
}

func (b *budget) admit(e *list.Element, w *waiter) {
	b.waiting.Remove(e)
	if w.n > 0 && w.h.held == 0 {
		b.holders++
	}
	b.used += w.n
	w.h.held += w.n
	w.taken = true
	close(w.ready)
}
