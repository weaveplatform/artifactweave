package accessapitest

import (
	"context"
	"slices"
	"strconv"
	"sync"
	"time"

	accessapi "github.com/weaveplatform/artifactweave/migration/access/api"
)

var _ accessapi.Tokens = (*Tokens)(nil)

// TokenCalls is what a Tokens recorded: each method's arguments, in call
// order.
type TokenCalls struct {
	Issue             []accessapi.TokenRequest
	IssueRegistry     []accessapi.RegistryTokenRequest
	RevokeValue       []string
	EncryptedPassword []string
	DeleteExpired     []time.Time
}

// Tokens is a Tokens that records every call, then answers it from its
// function fields, each with the signature of the method it answers. Where a
// field is nil it answers as a token service that refuses nothing:
//
//   - Issue and IssueRegistry return the n-th token asked for, counting both:
//     "access-token-n" with the id "token-n", and with the request's lifetime
//     in whole seconds. Issue adds "refresh-token-n" for a refreshable
//     request and "reference-token-n" for one that asks for a reference
//     token or has a purpose, as the contract says. A request without a
//     lifetime gets 0, since a fake has no default lifetime to apply.
//   - RevokeValue revokes, and DeleteExpired removes nothing.
//   - EncryptedPassword returns "encrypted-password-" and the user name, the
//     same value on every call, as the contract says.
//
// It is safe for concurrent use. The functions run outside its lock, so one
// may call Calls.
type Tokens struct {
	// IssueFunc answers Issue.
	IssueFunc func(ctx context.Context, r accessapi.TokenRequest) (accessapi.IssuedToken, error)
	// IssueRegistryFunc answers IssueRegistry.
	IssueRegistryFunc func(
		ctx context.Context,
		r accessapi.RegistryTokenRequest,
	) (accessapi.IssuedToken, error)
	// RevokeValueFunc answers RevokeValue.
	RevokeValueFunc func(ctx context.Context, raw string) error
	// EncryptedPasswordFunc answers EncryptedPassword.
	EncryptedPasswordFunc func(ctx context.Context, user string) (string, error)
	// DeleteExpiredFunc answers DeleteExpired.
	DeleteExpiredFunc func(ctx context.Context, before time.Time) (int64, error)

	mu    sync.Mutex
	calls TokenCalls
}

// Calls returns a copy of the calls recorded so far.
func (t *Tokens) Calls() TokenCalls {
	t.mu.Lock()
	defer t.mu.Unlock()
	return TokenCalls{
		Issue:             slices.Clone(t.calls.Issue),
		IssueRegistry:     slices.Clone(t.calls.IssueRegistry),
		RevokeValue:       slices.Clone(t.calls.RevokeValue),
		EncryptedPassword: slices.Clone(t.calls.EncryptedPassword),
		DeleteExpired:     slices.Clone(t.calls.DeleteExpired),
	}
}

// Issue records r and answers with IssueFunc, or with the next numbered
// token when it is nil.
func (t *Tokens) Issue(
	ctx context.Context,
	r accessapi.TokenRequest,
) (accessapi.IssuedToken, error) {
	t.mu.Lock()
	t.calls.Issue = append(t.calls.Issue, r)
	n := t.asked()
	t.mu.Unlock()
	if t.IssueFunc != nil {
		return t.IssueFunc(ctx, r)
	}
	issued := numbered(n)
	if r.ExpiresIn != nil {
		issued.ExpiresIn = int64(*r.ExpiresIn / time.Second)
	}
	if r.Refreshable {
		issued.RefreshToken = "refresh-token-" + strconv.Itoa(n)
	}
	if r.IncludeReference || r.Purpose != "" {
		issued.ReferenceToken = "reference-token-" + strconv.Itoa(n)
	}
	return issued, nil
}

// IssueRegistry records r and answers with IssueRegistryFunc, or with the
// next numbered token when it is nil.
func (t *Tokens) IssueRegistry(
	ctx context.Context,
	r accessapi.RegistryTokenRequest,
) (accessapi.IssuedToken, error) {
	t.mu.Lock()
	t.calls.IssueRegistry = append(t.calls.IssueRegistry, r)
	n := t.asked()
	t.mu.Unlock()
	if t.IssueRegistryFunc != nil {
		return t.IssueRegistryFunc(ctx, r)
	}
	issued := numbered(n)
	issued.ExpiresIn = int64(r.ExpiresIn / time.Second)
	return issued, nil
}

// RevokeValue records raw and answers with RevokeValueFunc, or nil when it is
// nil.
func (t *Tokens) RevokeValue(ctx context.Context, raw string) error {
	t.mu.Lock()
	t.calls.RevokeValue = append(t.calls.RevokeValue, raw)
	t.mu.Unlock()
	if t.RevokeValueFunc == nil {
		return nil
	}
	return t.RevokeValueFunc(ctx, raw)
}

// EncryptedPassword records user and answers with EncryptedPasswordFunc, or
// "encrypted-password-" and user when it is nil.
func (t *Tokens) EncryptedPassword(ctx context.Context, user string) (string, error) {
	t.mu.Lock()
	t.calls.EncryptedPassword = append(t.calls.EncryptedPassword, user)
	t.mu.Unlock()
	if t.EncryptedPasswordFunc == nil {
		return "encrypted-password-" + user, nil
	}
	return t.EncryptedPasswordFunc(ctx, user)
}

// DeleteExpired records before and answers with DeleteExpiredFunc, or 0 when
// it is nil.
func (t *Tokens) DeleteExpired(ctx context.Context, before time.Time) (int64, error) {
	t.mu.Lock()
	t.calls.DeleteExpired = append(t.calls.DeleteExpired, before)
	t.mu.Unlock()
	if t.DeleteExpiredFunc == nil {
		return 0, nil
	}
	return t.DeleteExpiredFunc(ctx, before)
}

// asked is how many tokens Issue and IssueRegistry have been asked for,
// counting the call being answered. Call it under the lock.
func (t *Tokens) asked() int { return len(t.calls.Issue) + len(t.calls.IssueRegistry) }

// numbered is the n-th token's access token and id.
func numbered(n int) accessapi.IssuedToken {
	return accessapi.IssuedToken{
		AccessToken: "access-token-" + strconv.Itoa(n),
		TokenID:     "token-" + strconv.Itoa(n),
	}
}
