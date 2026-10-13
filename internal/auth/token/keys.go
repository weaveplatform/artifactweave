package token

import (
	"context"
	"crypto/rand"
	"crypto/rsa"
	"crypto/x509"
	"encoding/binary"
	"encoding/pem"
	"errors"
	"fmt"
	"strings"
	"time"

	"github.com/weaveplatform/artifactweave/pkg/store"
	"github.com/weaveplatform/artifactweave/pkg/types"
)

const (
	// keyPrefix marks the KeyStore signing keys that sign access tokens; the
	// store also holds release-bundle signing keys (decision 0023), whose
	// IsDefault flag this package leaves alone.
	keyPrefix = "access-token-"
	// stateSetting holds which access-token keys are retired and until when.
	stateSetting = "token.signing_keys"
	// DefaultKeyBits is the RSA key size.
	DefaultKeyBits = 2048
)

// keyState records retired keys: each keeps verifying until the time given,
// the latest expiry of a token stored when it was retired, or for ever (the
// zero time) when one of those tokens never expires.
type keyState struct {
	Retired map[string]time.Time `json:"retired,omitempty"`
}

// keyset is the verification view of the keys: the active key signs; every
// key in public verifies.
type keyset struct {
	active string
	priv   *rsa.PrivateKey
	public map[string]*rsa.PublicKey
}

// newKID orders keys by creation: sixteen hex digits of the creation time
// then eight random ones, so the newest unretired key sorts last.
func newKID(now time.Time) string {
	var r [4]byte
	_, _ = rand.Read(r[:])
	return fmt.Sprintf(
		"%016x%08x",
		uint64(now.UnixNano()),
		binary.BigEndian.Uint32(r[:]),
	) //nolint:gosec // a time
}

func (s *Service) state(ctx context.Context) (keyState, error) {
	var st keyState
	err := s.st.Settings().Get(ctx, stateSetting, &st)
	if errors.Is(err, store.ErrNotFound) {
		return keyState{}, nil
	}
	if err != nil {
		return keyState{}, fmt.Errorf("token: key state: %w", err)
	}
	return st, nil
}

// load reads the keys. Keys retired until a time now past no longer verify.
// With two instances creating the first key at once, both keys verify and
// the newer one signs, so neither instance's tokens are refused.
func (s *Service) load(ctx context.Context) (keyset, error) {
	state, err := s.state(ctx)
	if err != nil {
		return keyset{}, err
	}
	all, err := s.st.Keys().SigningKeys(ctx)
	if err != nil {
		return keyset{}, fmt.Errorf("token: signing keys: %w", err)
	}
	now := s.now()
	ks := keyset{public: map[string]*rsa.PublicKey{}}
	var activeKey types.SigningKey
	for _, k := range all {
		kid, ok := strings.CutPrefix(k.Alias, keyPrefix)
		if !ok {
			continue
		}
		until, retired := state.Retired[kid]
		if retired && !until.IsZero() && !now.Before(until) {
			continue
		}
		pub, err := parsePublic(k.PublicKey)
		if err != nil {
			return keyset{}, fmt.Errorf("token: key %s: %w", kid, err)
		}
		ks.public[kid] = pub
		if !retired && kid > ks.active {
			ks.active, activeKey = kid, k
		}
	}
	if ks.active != "" {
		if ks.priv, err = parsePrivate(activeKey.PrivateKey); err != nil {
			return keyset{}, fmt.Errorf("token: key %s: %w", ks.active, err)
		}
	}
	return ks, nil
}

// createKey stores a new key; the next load makes it the active one.
func (s *Service) createKey(ctx context.Context) error {
	key, err := rsa.GenerateKey(rand.Reader, s.bits)
	if err != nil {
		return fmt.Errorf("token: generate key: %w", err)
	}
	der, err := x509.MarshalPKCS8PrivateKey(key)
	if err != nil {
		return fmt.Errorf("token: encode key: %w", err)
	}
	pubDER, err := x509.MarshalPKIXPublicKey(&key.PublicKey)
	if err != nil {
		return fmt.Errorf("token: encode key: %w", err)
	}
	err = s.st.Keys().PutSigningKey(ctx, types.SigningKey{
		Alias:      keyPrefix + newKID(s.now()),
		PublicKey:  string(pem.EncodeToMemory(&pem.Block{Type: "PUBLIC KEY", Bytes: pubDER})),
		PrivateKey: string(pem.EncodeToMemory(&pem.Block{Type: "PRIVATE KEY", Bytes: der})),
	})
	if err != nil {
		return fmt.Errorf("token: store key: %w", err)
	}
	return nil
}

// Rotate makes a new signing key active. The keys it replaces keep
// verifying until the latest expiry of the tokens stored now, or for ever
// when one of them never expires, so no token issued before the rotation is
// refused early.
func (s *Service) Rotate(ctx context.Context) error {
	s.mu.Lock()
	defer s.mu.Unlock()
	until, err := s.latestExpiry(ctx)
	if err != nil {
		return err
	}
	state, err := s.state(ctx)
	if err != nil {
		return err
	}
	if state.Retired == nil {
		state.Retired = map[string]time.Time{}
	}
	for kid := range s.keys.public {
		if _, retired := state.Retired[kid]; !retired {
			state.Retired[kid] = until
		}
	}
	if err := s.createKey(ctx); err != nil {
		return err
	}
	if err := s.st.Settings().Put(ctx, stateSetting, state); err != nil {
		return fmt.Errorf("token: key state: %w", err)
	}
	return s.reload(ctx)
}

// latestExpiry is the latest expiry of the stored tokens, the zero time when
// one never expires.
func (s *Service) latestExpiry(ctx context.Context) (time.Time, error) {
	var latest time.Time
	p := store.Page{Limit: 500}
	for {
		page, next, err := s.st.Tokens().List(ctx, store.TokenFilter{}, p)
		if err != nil {
			return time.Time{}, fmt.Errorf("token: list: %w", err)
		}
		for _, t := range page {
			if t.ExpiresAt.IsZero() {
				return time.Time{}, nil
			}
			if t.ExpiresAt.After(latest) {
				latest = t.ExpiresAt
			}
		}
		if next == "" {
			return latest, nil
		}
		p.After = next
	}
}

func parsePrivate(s string) (*rsa.PrivateKey, error) {
	b, _ := pem.Decode([]byte(s))
	if b == nil {
		return nil, errBadKey
	}
	k, err := x509.ParsePKCS8PrivateKey(b.Bytes)
	if err != nil {
		return nil, fmt.Errorf("%w: %w", errBadKey, err)
	}
	rk, ok := k.(*rsa.PrivateKey)
	if !ok {
		return nil, errBadKey
	}
	return rk, nil
}

func parsePublic(s string) (*rsa.PublicKey, error) {
	b, _ := pem.Decode([]byte(s))
	if b == nil {
		return nil, errBadKey
	}
	k, err := x509.ParsePKIXPublicKey(b.Bytes)
	if err != nil {
		return nil, fmt.Errorf("%w: %w", errBadKey, err)
	}
	rk, ok := k.(*rsa.PublicKey)
	if !ok {
		return nil, errBadKey
	}
	return rk, nil
}
