package accesshttp

import (
	"context"
	"errors"
	"fmt"
	"net/http"

	"github.com/weaveplatform/artifactweave/internal/access/api"
	"github.com/weaveplatform/artifactweave/internal/access/core/port"
	"github.com/weaveplatform/artifactweave/internal/kernel"
	artgen "github.com/weaveplatform/artifactweave/internal/platform/gen/artifactory"
	"github.com/weaveplatform/artifactweave/internal/platform/httpkit"
	"github.com/weaveplatform/artifactweave/internal/platform/httpkit/apierror"
	"github.com/weaveplatform/artifactweave/internal/platform/keymaterial"
)

// The instance key's answers. 7.161.26's signing key UI services
// (InstallSigningKeyService, UpdateSigningKeyService) send these texts; the
// REST reference names only "a confirmation message" [I].
const (
	gpgPrivateInstalled  = "Private key is installed"
	gpgPublicInstalled   = "Public key is installed"
	gpgPassphraseUpdated = "Successfully updated signing pass-phrase"
)

// SetGpgPrivateKey stores the instance key's private half
// (setgpgprivatekey). It is checked to be one armored OpenPGP private key,
// but not unlocked: the passphrase may come after it. Whether the halves
// belong together is checked when the pair signs.
func (s SecurityAPI) SetGpgPrivateKey(
	ctx context.Context,
	req artgen.SetGpgPrivateKeyRequestObject,
) (artgen.SetGpgPrivateKeyResponseObject, error) {
	st, err := s.adminStore(ctx)
	if err != nil {
		return nil, err
	}
	value := ""
	if req.Body != nil {
		value = *req.Body
	}
	if err := keymaterial.CheckGPGPrivate(value); err != nil {
		return nil, httpkit.BadRequestf("%s", err.Error())
	}
	if err := s.stageGPG(ctx, st, port.KeypairPrivateKey, value); err != nil {
		return nil, err
	}
	return artgen.SetGpgPrivateKey200TextResponse(gpgPrivateInstalled), nil
}

// SetGpgPublicKey stores the instance key's public half
// (setgpgpublickey), which getGpgPublicKey and every repository signing
// with the instance key publish: it passes the complete-field public check
// every key pair's public half passes (#1727).
func (s SecurityAPI) SetGpgPublicKey(
	ctx context.Context,
	req artgen.SetGpgPublicKeyRequestObject,
) (artgen.SetGpgPublicKeyResponseObject, error) {
	st, err := s.adminStore(ctx)
	if err != nil {
		return nil, err
	}
	value := ""
	if req.Body != nil {
		value = *req.Body
	}
	if err := keymaterial.CheckPublic(keymaterial.GPG, value); err != nil {
		return nil, httpkit.BadRequestf("%s", err.Error())
	}
	if err := s.stageGPG(ctx, st, port.KeypairPublicKey, value); err != nil {
		return nil, err
	}
	return artgen.SetGpgPublicKey200TextResponse(gpgPublicInstalled), nil
}

// SetGpgPassPhrase stores the passphrase that unlocks the instance key's
// private half (setgpgpassphrase), sealed like every passphrase. Set
// before either half, it creates the pair holding the passphrase alone,
// where 7.161.26's UI refuses ("Failed to update a non existed key") [I].
func (s SecurityAPI) SetGpgPassPhrase(
	ctx context.Context,
	req artgen.SetGpgPassPhraseRequestObject,
) (artgen.SetGpgPassPhraseResponseObject, error) {
	st, err := s.adminStore(ctx)
	if err != nil {
		return nil, err
	}
	if err := s.stageGPG(ctx, st, port.KeypairPassphrase, req.Params.XGPGPASSPHRASE); err != nil {
		return nil, err
	}
	return artgen.SetGpgPassPhrase200TextResponse(gpgPassphraseUpdated), nil
}

// GetGpgPublicKey answers the instance key's public half
// (getgpgpublickey). Any caller the middleware lets through may read it,
// as 7.x lets any authenticated user, and anonymous while anonymous access
// is on. The stored value is checked again before it is sent, as every
// public export is (#1727).
func (s SecurityAPI) GetGpgPublicKey(
	ctx context.Context,
	_ artgen.GetGpgPublicKeyRequestObject,
) (artgen.GetGpgPublicKeyResponseObject, error) {
	st, err := s.content()
	if err != nil {
		return nil, err
	}
	k, err := st.Keys().Keypair(ctx, api.DefaultGPGKey)
	switch {
	case errors.Is(err, kernel.ErrNotFound) || err == nil && (k.PublicKey == "" || k.Type != api.KeypairGPG):
		// The page's 404 description; 7.x's message is not published [I].
		return nil, apierror.New(http.StatusNotFound, "No GPG public key has been configured")
	case err != nil:
		return nil, s.internalError(ctx, "get the instance GPG key", err)
	}
	if err := keymaterial.CheckPublic(keymaterial.GPG, k.PublicKey); err != nil {
		return nil, s.internalError(ctx, "read the instance GPG public key", err)
	}
	return artgen.GetGpgPublicKey200TextResponse(k.PublicKey), nil
}

// stageGPG sets one field of the instance key pair, creating the pair on
// the first write, in one statement, so two administrators setting
// different halves at once both keep theirs. A pair of that name created
// through the key pair API as another type is refused rather than given a
// GPG half [I].
func (s SecurityAPI) stageGPG(ctx context.Context, st Store, field port.KeypairField, value string) error {
	err := st.Tx(ctx, func(ctx context.Context) error {
		switch k, err := st.Keys().Keypair(ctx, api.DefaultGPGKey); {
		case errors.Is(err, kernel.ErrNotFound):
		case err != nil:
			return err //nolint:wrapcheck // wrapped below
		case k.Type != api.KeypairGPG:
			return errNotGPG
		}
		return st.Keys().SetKeypairField(ctx, api.DefaultGPGKey, field, value) //nolint:wrapcheck // wrapped below
	})
	switch {
	case errors.Is(err, errNotGPG):
		return httpkit.BadRequestf("Key pair '%s' is not a GPG key pair", api.DefaultGPGKey)
	case err != nil:
		return s.internalError(ctx, "store the instance GPG key", fmt.Errorf("%s: %w", api.DefaultGPGKey, err))
	}
	return nil
}

var errNotGPG = errors.New("the instance key pair is not a GPG pair")
