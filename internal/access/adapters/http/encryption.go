package accesshttp

import (
	"context"

	artgen "github.com/weaveplatform/artifactweave/internal/platform/gen/artifactory"
	"github.com/weaveplatform/artifactweave/internal/platform/httpkit"
)

// keyEncryptionDone is the body 7.x answers both key encryption operations
// with: SystemResource.encrypt and decrypt return the entity "DONE" as
// text/plain (7.161.26), as the reference pages show.
const keyEncryptionDone = "DONE"

// ActivateArtifactoryKeyEncryption answers an administrator DONE and
// changes nothing: secrets are sealed at all times (decision 0017), the
// state 7.x's encrypt puts the configuration in. A user who is not an
// administrator gets 403 and an anonymous caller 401, as SystemResource's
// @RolesAllowed(ROLE_ADMIN) refuses them.
func (s SecurityAPI) ActivateArtifactoryKeyEncryption(
	ctx context.Context,
	_ artgen.ActivateArtifactoryKeyEncryptionRequestObject,
) (artgen.ActivateArtifactoryKeyEncryptionResponseObject, error) {
	if err := httpkit.AdminOnly(ctx); err != nil {
		return nil, err
	}
	return artgen.ActivateArtifactoryKeyEncryption200TextResponse(keyEncryptionDone), nil
}

// DeactivateArtifactoryKeyEncryption answers an administrator DONE and
// changes nothing, so secrets stay sealed (decision 0017). 7.x's decrypt
// answers 409 "Cannot decrypt without artifactory key file" only before
// any key exists and 405 only on SaaS; a server whose secrets are always
// sealed is never in the first state, and artifactweave is self-managed.
// 7.x's own decrypted state lasts only until the next configuration change
// (the reference page), so a client cannot rely on it either.
func (s SecurityAPI) DeactivateArtifactoryKeyEncryption(
	ctx context.Context,
	_ artgen.DeactivateArtifactoryKeyEncryptionRequestObject,
) (artgen.DeactivateArtifactoryKeyEncryptionResponseObject, error) {
	if err := httpkit.AdminOnly(ctx); err != nil {
		return nil, err
	}
	return artgen.DeactivateArtifactoryKeyEncryption200TextResponse(keyEncryptionDone), nil
}
