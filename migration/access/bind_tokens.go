package access

import (
	"context"
	"time"

	accessapi "github.com/weaveplatform/artifactweave/migration/access/api"
	"github.com/weaveplatform/artifactweave/migration/access/core/token"
)

var _ accessapi.Tokens = tokens{}

// tokens binds the token service. The access/api requests carry the
// service's fields in its order and types, so each converts to the
// service's request directly and issuance keeps every rule of decision 0005.
// Errors pass through as the service gave them, for errors.Is.
type tokens struct {
	svc *token.Service
	st  Store
}

func (b tokens) Issue(
	ctx context.Context,
	r accessapi.TokenRequest,
) (accessapi.IssuedToken, error) {
	issued, err := b.svc.Issue(ctx, token.Request(r))
	if err != nil {
		return accessapi.IssuedToken{}, err //nolint:wrapcheck // the service's errors are the contract's
	}
	return issuedOf(issued), nil
}

func (b tokens) IssueRegistry(
	ctx context.Context,
	r accessapi.RegistryTokenRequest,
) (accessapi.IssuedToken, error) {
	issued, err := b.svc.IssueRegistry(ctx, token.RegistryRequest(r))
	if err != nil {
		return accessapi.IssuedToken{}, err //nolint:wrapcheck // the service's errors are the contract's
	}
	return issuedOf(issued), nil
}

func (b tokens) RevokeValue(ctx context.Context, raw string) error {
	return b.svc.RevokeValue(ctx, raw) //nolint:wrapcheck // the service's errors are the contract's
}

func (b tokens) EncryptedPassword(ctx context.Context, user string) (string, error) {
	//nolint:wrapcheck // the service's errors are the contract's
	return b.svc.EncryptedPassword(ctx, user)
}

// DeleteExpired reads the store directly, as the garbage collector does: the
// service keeps no state about expired records.
func (b tokens) DeleteExpired(ctx context.Context, before time.Time) (int64, error) {
	//nolint:wrapcheck // the store's errors are the contract's; the collector wraps them
	return b.st.Tokens().DeleteExpired(ctx, before)
}

// issuedOf is what callers read of an issued token: its secrets and lifetime,
// and from its record its id and issue time.
func issuedOf(i token.Issued) accessapi.IssuedToken {
	return accessapi.IssuedToken{
		AccessToken:    i.AccessToken,
		RefreshToken:   i.RefreshToken,
		ReferenceToken: i.ReferenceToken,
		ExpiresIn:      i.ExpiresIn,
		TokenID:        i.Record.ID,
		IssuedAt:       i.Record.IssuedAt,
	}
}
