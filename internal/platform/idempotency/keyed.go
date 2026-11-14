package idempotency

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"net/http"
	"strings"

	"github.com/weaveplatform/artifactweave/internal/kernel"
	"github.com/weaveplatform/artifactweave/internal/platform/gen/operations"
	"github.com/weaveplatform/artifactweave/internal/platform/httpkit"
	"github.com/weaveplatform/artifactweave/internal/platform/httpkit/apierror"
)

// Header is the request header that carries an idempotency key.
const Header = "Idempotency-Key"

// Binding is what one keyed operation gives the replay beyond its handler,
// which the replay runs as the effect. The request each function receives is
// the generated strict server's request object for the operation.
type Binding struct {
	// Name names the operation's effect in the answer to a request that
	// runs out of budget, "Protected <Name> exceeded its wait budget";
	// empty means "request".
	Name string
	// Resource returns the canonical resource the request addresses, as the
	// operation derives it from its path and query: aliases resolved and
	// timestamps at the store's microsecond precision. The scope digests it
	// as JSON. It runs before the key is read, so it makes the checks the
	// handler makes first and returns the handler's error for a request
	// they refuse. Nil scopes the key to the credential and the operation
	// alone.
	Resource func(ctx context.Context, request any) (any, error)
	// Fingerprint returns the request parameters that change what the
	// effect does without selecting its target, such as a dry-run flag,
	// which the fingerprint digests as JSON after the body. Nil means none.
	// A parameter that only checks consistency belongs in neither.
	Fingerprint func(ctx context.Context, request any) (any, error)
	// Reload confirms a committed effect and answers for it: it locks and
	// loads the resource the receipt names, rechecks the current
	// credential and permissions, and returns the operation's success
	// response built from the committed state. It runs after the handler
	// and on every replay, in the same transaction; only a replay's answer
	// is its response.
	Reload func(ctx context.Context, request any, receipt ReplayReceipt) (any, error)
	// Error maps an error from the handler, Reload or the replay's store to
	// the operation's answer. Nil passes an *apierror.Error through and
	// answers anything else 500.
	Error func(ctx context.Context, request any, err error) error
}

// Keyed reports whether r carries an Idempotency-Key, so that the router
// keeps the exact body bytes the fingerprint digests.
func Keyed(r *http.Request) bool {
	_, ok := r.Header[Header]
	return ok
}

// ParseKey checks a raw Idempotency-Key value, a quoted string of 1 to 128
// ASCII letters, digits, ".", "_", ":" or "-" (decision 0043, a bounded
// subset of the draft's structured string), and returns the SHA-256 hex
// digest of the key inside the quotes, never the key itself.
func ParseKey(raw string) (string, error) {
	if len(raw) < 3 || len(raw) > 130 || raw[0] != '"' || raw[len(raw)-1] != '"' {
		return "", httpkit.BadRequestf(
			"Idempotency-Key must be a quoted string of 1-128 letters, digits, . _ : or -",
		)
	}
	key := raw[1 : len(raw)-1]
	for _, c := range key {
		if c >= 'a' && c <= 'z' || c >= 'A' && c <= 'Z' || c >= '0' && c <= '9' ||
			c == '.' || c == '_' || c == ':' || c == '-' {
			continue
		}
		return "", httpkit.BadRequestf("Idempotency-Key contains an unsupported character")
	}
	return digest([]byte(key)), nil
}

// Report records id, the positive, stable ID of the resource that a keyed
// handler's effect created or changed, for the receipt. A handler under the
// replay calls it on every success; one that answers without it has its
// work rolled back and gets no receipt, which is how a typed error
// response that is not a Go error stays unreplayed. Outside the replay it
// does nothing.
func Report(ctx context.Context, id int64) {
	if s, ok := ctx.Value(reportKey{}).(*int64); ok {
		*s = id
	}
}

type reportKey struct{}

var (
	// errUnreported rolls back a handler's work that reported no resource.
	errUnreported = errors.New("idempotency: the handler reported no resource")
	// errUnbound is the logged cause of the 500 a keyed operation without a
	// binding answers.
	errUnbound = errors.New("idempotency: an operation declares a key but has no binding")
)

// Strict returns the generated strict servers' middleware for service's
// operations in table. It wraps each operation that declares the shared
// IdempotencyKey parameter, and passes every other one through untouched.
// A wrapped operation called without a key also runs as it does unwrapped.
// With a key it runs under the replay with its binding from bindings, keyed
// by operation ID; one without a binding answers 500 rather than run
// unprotected. H is the generated package's StrictHandlerFunc, whose
// middleware receives the Go name of the operation ID.
func Strict[H ~func(context.Context, http.ResponseWriter, *http.Request, any) (any, error)](
	p Replay,
	service string,
	table []operations.Operation,
	bindings map[string]Binding,
) func(H, string) H {
	keyed := map[string]string{}
	for _, op := range table {
		if op.Service == service && op.IdempotencyKey {
			keyed[goName(op.OperationID)] = op.OperationID
		}
	}
	return func(next H, name string) H {
		id, ok := keyed[name]
		if !ok {
			return next
		}
		b, bound := bindings[id]
		bound = bound && b.Reload != nil
		return func(ctx context.Context, w http.ResponseWriter, r *http.Request, request any) (any, error) {
			values, present := r.Header[Header]
			switch {
			case !present:
				return next(ctx, w, r, request)
			case !bound:
				httpkit.NoteCause(ctx, fmt.Errorf("%w: %s", errUnbound, id))
				return nil, apierror.New(http.StatusInternalServerError, "internal error")
			}
			return p.serve(ctx, id, b, values[0], request, func(ctx context.Context) (any, error) {
				return next(ctx, w, r, request)
			})
		}
	}
}

// goName is the name oapi-codegen gives an operation ID, which the strict
// middleware receives. Every ID in the specs is alphanumeric, which the
// operations table test keeps, so only the first letter changes.
func goName(id string) string {
	if id == "" {
		return id
	}
	return strings.ToUpper(id[:1]) + id[1:]
}

// serve runs one keyed request: the binding's resource, the key and the
// credential checks, then the handler and the reload under Do.
func (p Replay) serve(
	ctx context.Context,
	operation string,
	b Binding,
	raw string,
	request any,
	handler func(context.Context) (any, error),
) (any, error) {
	resource, err := b.resource(ctx, request)
	if err != nil {
		return nil, err
	}
	key, err := ParseKey(raw)
	if err != nil {
		return nil, err
	}
	caller, err := httpkit.Principal(ctx)
	if err != nil {
		return nil, err //nolint:wrapcheck // a typed answer
	}
	// The scope needs a stable credential reference: anonymous callers and
	// registry tokens have none that a later retry could present again.
	if caller.Anonymous || caller.Registry != nil || !caller.Password && caller.TokenID == "" {
		return nil, httpkit.Denied(caller) //nolint:wrapcheck // a typed answer
	}
	if p.Store == nil || p.Tx == nil {
		return nil, apierror.ErrNotImplemented
	}
	r, err := p.request(ctx, operation, b, caller, resource, key, request)
	if err != nil {
		return nil, b.mapError(ctx, request, err)
	}
	var (
		response             any
		effectErr, reloadErr error
	)
	_, err = p.Do(ctx, r, Effect{
		Run: func(ctx context.Context) (int64, error) {
			var id int64
			out, err := handler(context.WithValue(ctx, reportKey{}, &id))
			response = out
			switch {
			case err != nil:
				effectErr = err
				return 0, err
			case id == 0:
				return 0, errUnreported
			}
			return id, nil
		},
		Reload: func(ctx context.Context, receipt ReplayReceipt) error {
			out, err := b.Reload(ctx, request, receipt)
			if err != nil {
				reloadErr = err
				return err
			}
			if receipt.Replayed {
				response = out
			}
			return nil
		},
	})
	switch {
	case err == nil:
		return response, nil
	case errors.Is(err, errUnreported) && effectErr == nil && reloadErr == nil:
		return response, nil
	case errors.Is(err, ErrReplayMismatch):
		return nil, apierror.New(
			http.StatusUnprocessableEntity,
			"Idempotency-Key was used with different request bytes",
		)
	case errors.Is(err, context.DeadlineExceeded):
		e := apierror.New(
			http.StatusServiceUnavailable,
			fmt.Sprintf("Protected %s exceeded its wait budget; retry the same key", b.name()),
		)
		e.RetryAfterSeconds = 1
		return nil, e
	}
	// The handler's and the reload's own errors, not the transaction's: a
	// SQL unit of work joins every nested failure into the error it
	// returns, and the answer is the operation's, not that chain's.
	switch {
	case effectErr != nil:
		err = effectErr
	case reloadErr != nil:
		err = reloadErr
	}
	return nil, b.mapError(ctx, request, err)
}

// request builds the replay request: the scope digests the credential
// reference, the operation and the canonical resource; the fingerprint the
// exact body and the binding's effect parameters.
func (p Replay) request(
	ctx context.Context,
	operation string,
	b Binding,
	caller kernel.Principal,
	resource any,
	key string,
	request any,
) (ReplayRequest, error) {
	var r ReplayRequest
	scope, err := json.Marshal(struct {
		Credential kernel.CredentialReference `json:"credential"`
		Operation  string                     `json:"operation"`
		Resource   any                        `json:"resource"`
	}{caller.CredentialReference(), operation, resource})
	if err != nil {
		return r, fmt.Errorf("idempotency: scope: %w", err)
	}
	fingerprint := httpkit.RawBody(ctx)
	if b.Fingerprint != nil {
		extra, err := b.Fingerprint(ctx, request)
		if err != nil {
			return r, err
		}
		params, err := json.Marshal(extra)
		if err != nil {
			return r, fmt.Errorf("idempotency: fingerprint: %w", err)
		}
		// Digested apart, so that no body can collide with another body
		// and its parameters.
		fingerprint = []byte(digest(fingerprint) + digest(params))
	}
	now := p.now()
	return ReplayRequest{
		Scope: digest(scope), Key: key, Fingerprint: digest(fingerprint),
		Now: now, Expires: now.Add(Retention),
	}, nil
}

func (b Binding) resource(ctx context.Context, request any) (any, error) {
	if b.Resource == nil {
		// An operation whose path selects nothing, scoped by the
		// credential and the operation alone.
		return struct{}{}, nil
	}
	return b.Resource(ctx, request)
}

func (b Binding) name() string {
	if b.Name == "" {
		return "request"
	}
	return b.Name
}

func (b Binding) mapError(ctx context.Context, request any, err error) error {
	if b.Error != nil {
		return b.Error(ctx, request, err)
	}
	var typed *apierror.Error
	if errors.As(err, &typed) {
		return typed
	}
	httpkit.NoteCause(ctx, err)
	return apierror.New(http.StatusInternalServerError, "internal error")
}

func digest(value []byte) string {
	sum := sha256.Sum256(value)
	return hex.EncodeToString(sum[:])
}
