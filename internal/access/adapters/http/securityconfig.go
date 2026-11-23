package accesshttp

import (
	"context"
	"errors"
	"fmt"
	"strings"

	accessapi "github.com/weaveplatform/artifactweave/internal/access/api"
	"github.com/weaveplatform/artifactweave/internal/access/core/authn"
	"github.com/weaveplatform/artifactweave/internal/kernel"
	artgen "github.com/weaveplatform/artifactweave/internal/platform/gen/artifactory"
	"github.com/weaveplatform/artifactweave/internal/platform/settings"
)

// GetSecurityConfig returns the general security settings in one read, the
// read artifactory_general_security refreshes from. 7.161.26 serves it from
// the UI's GeneralSecurityConfigResource, whose Jersey application also
// answers under /api, so the provider reaches it as /api/securityconfig. It
// carries only the settings artifactweave has: the build-info, hidden
// resource, password reset, central OAuth and curation fields 7.x adds are
// left out rather than reported as off [I]. The encryption policy is spelled
// in upper case, as EncryptionPolicy's JSON names are and as the provider
// compares it with its plan.
func (s SecurityAPI) GetSecurityConfig(
	ctx context.Context,
	_ artgen.GetSecurityConfigRequestObject,
) (artgen.GetSecurityConfigResponseObject, error) {
	st, err := s.adminStore(ctx)
	if err != nil {
		return nil, err
	}
	doc, err := securityConfig(ctx, st.Settings())
	if err != nil {
		return nil, s.internalError(ctx, "get security configuration", err)
	}
	return artgen.GetSecurityConfig200JSONResponse(doc), nil
}

func securityConfig(ctx context.Context, store settings.SettingsStore) (artgen.SecurityConfig, error) {
	var doc artgen.SecurityConfig
	anonymous := false
	if err := store.Get(ctx, settings.SettingAnonymousAccess, &anonymous); err != nil &&
		!errors.Is(err, kernel.ErrNotFound) {
		return doc, fmt.Errorf("anonymous access: %w", err)
	}
	basic, err := authn.BasicAuthEnabled(ctx, store)
	if err != nil {
		return doc, err //nolint:wrapcheck // already wrapped
	}
	lock, err := authn.LockPolicy(ctx, store)
	if err != nil {
		return doc, err //nolint:wrapcheck // already wrapped
	}
	expiry, err := authn.ExpirationPolicy(ctx, store)
	if err != nil {
		return doc, err //nolint:wrapcheck // already wrapped
	}
	encryption, err := authn.PasswordEncryption(ctx, store)
	if err != nil {
		return doc, err //nolint:wrapcheck // already wrapped
	}
	doc.AnonAccessEnabled, doc.BasicAuthEnabled = anonymous, basic
	doc.UserLockPolicy = artgen.UserLockPolicy{Enabled: &lock.Enabled, LoginAttempts: &lock.LoginAttempts}
	doc.PasswordSettings = artgen.SecurityConfigPasswordSettings{
		EncryptionPolicy:   artgen.SecurityConfigPasswordSettingsEncryptionPolicy(strings.ToUpper(string(encryption))),
		EncryptionRequired: encryption == accessapi.EncryptionRequired,
		EncryptionEnabled:  encryption != accessapi.EncryptionUnsupported,
		ExpirationPolicy:   expirationDoc(expiry),
	}
	return doc, nil
}
