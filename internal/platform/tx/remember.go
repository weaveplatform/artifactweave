package tx

import (
	"context"
	"sync"
)

type sessionKey struct{}

type session struct {
	mu     sync.Mutex
	values map[any]any
}

// WithSession returns ctx carrying a memo for Remember. It scopes one bounded
// operation whose effects all commit in transactions that check authority
// themselves, such as one step of a pull replication. A context derived from
// ctx, including one inside a transaction begun with it, shares the session;
// without a session Remember remembers nothing.
func WithSession(ctx context.Context) context.Context {
	if _, ok := ctx.Value(sessionKey{}).(*session); ok {
		return ctx
	}
	return context.WithValue(ctx, sessionKey{}, &session{})
}

// Remember returns what fn computed for key earlier and keeps a successful
// result, but only inside a session (see WithSession); otherwise it runs fn
// every time. An error is never kept, so a later call retries.
//
// inTx reports whether ctx is inside a transaction of the application store.
// There the result is kept only until that transaction ends, so every
// transaction that publishes an effect resolves its facts afresh; only facts
// that no other transaction can change meanwhile and that this one does not
// write may be remembered: those read after the transaction has taken the
// project authority guard, which every root transaction on the application
// store takes first. Outside a transaction the result is kept for the rest of
// the session, so an early check that guards no effect of its own resolves
// the same facts once rather than before every exchange.
func Remember[T any](ctx context.Context, inTx bool, key any, fn func() (T, error)) (T, error) {
	s, _ := ctx.Value(sessionKey{}).(*session)
	if s == nil {
		return fn()
	}
	if inTx {
		f, _ := ctx.Value(finalizersKey{}).(*Finalizers)
		if f == nil {
			return fn()
		}
		return remember(&f.mu, &f.remembered, func() bool { return !f.closed }, key, fn)
	}
	return remember(&s.mu, &s.values, func() bool { return true }, key, fn)
}

func remember[T any](mu *sync.Mutex, values *map[any]any, open func() bool, key any, fn func() (T, error)) (T, error) {
	mu.Lock()
	kept, ok := (*values)[key].(T)
	mu.Unlock()
	if ok {
		return kept, nil
	}
	value, err := fn()
	if err != nil {
		return value, err
	}
	mu.Lock()
	defer mu.Unlock()
	if open() {
		if *values == nil {
			*values = make(map[any]any)
		}
		(*values)[key] = value
	}
	return value, nil
}
