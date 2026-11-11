package accesshttp

import (
	"context"
	"errors"
	"fmt"
	"io"
	"mime"
	"net/http"
	"strings"

	"github.com/weaveplatform/artifactweave/internal/access/api"
	"github.com/weaveplatform/artifactweave/internal/access/core/port"
	artifactsapi "github.com/weaveplatform/artifactweave/internal/artifacts/api"
	"github.com/weaveplatform/artifactweave/internal/artifacts/api/repo"
	"github.com/weaveplatform/artifactweave/internal/kernel"
	artgen "github.com/weaveplatform/artifactweave/internal/platform/gen/artifactory"
	"github.com/weaveplatform/artifactweave/internal/platform/httpkit"
	"github.com/weaveplatform/artifactweave/internal/platform/httpkit/apierror"
	"github.com/weaveplatform/artifactweave/internal/platform/keymaterial"
)

// maxCertificateBundle bounds a certificate upload: a PEM certificate, its
// chain and key are a few kilobytes.
const maxCertificateBundle = 1 << 20

func keypairNotFound(name string) error {
	return apierror.New(http.StatusNotFound, fmt.Sprintf("Key pair '%s' does not exist", name))
}

// repoNotFound is the repositories API's answer for a missing repository,
// which the public key download gives in the same words.
func repoNotFound(key string) error {
	return apierror.New(http.StatusNotFound, fmt.Sprintf("Repository '%s' does not exist", key))
}

func certificateNotFound(alias string) error {
	return apierror.New(http.StatusNotFound, fmt.Sprintf("Certificate '%s' does not exist", alias))
}

func keypairSummary(k api.Keypair) artgen.KeyPairSummary {
	typ := string(k.Type)
	return artgen.KeyPairSummary{
		PairName: &k.Name, PairType: &typ, Alias: &k.Alias, PublicKey: &k.PublicKey,
	}
}

// keypairFrom validates a key pair body: the required fields, no vault key
// without a private key (artifactweave has no vault integration), and
// halves that belong together.
func keypairFrom(in *artgen.KeyPairInput) (api.Keypair, error) {
	if in == nil {
		return api.Keypair{}, httpkit.BadRequestf("A key pair body is required")
	}
	priv, pass := "", ""
	if in.PrivateKey != nil {
		priv = *in.PrivateKey
	}
	if in.Passphrase != nil {
		pass = *in.Passphrase
	}
	if in.VaultKey != nil && *in.VaultKey != "" && priv == "" {
		return api.Keypair{}, httpkit.BadRequestf(
			"Vault keys are not supported; supply privateKey",
		)
	}
	for field, value := range map[string]string{
		"pairName": in.PairName, "pairType": string(in.PairType), "alias": in.Alias,
		"privateKey": priv, "publicKey": in.PublicKey,
	} {
		if strings.TrimSpace(value) == "" {
			return api.Keypair{}, httpkit.BadRequestf("'%s' is required", field)
		}
	}
	if err := keymaterial.CheckPair(string(in.PairType), priv, in.PublicKey, pass); err != nil {
		return api.Keypair{}, httpkit.BadRequestf("%s", err.Error())
	}
	return api.Keypair{
		Name: in.PairName, Type: api.KeypairType(in.PairType), Alias: in.Alias,
		PublicKey: in.PublicKey, PrivateKey: priv, Passphrase: pass,
	}, nil
}

// adminStore is the store of an admin-only operation.
func (s SecurityAPI) adminStore(ctx context.Context) (Store, error) {
	if err := httpkit.AdminOnly(ctx); err != nil {
		return nil, err
	}
	return s.content()
}

// GetAllKeyPairs lists every key pair without private material.
func (s SecurityAPI) GetAllKeyPairs(
	ctx context.Context,
	_ artgen.GetAllKeyPairsRequestObject,
) (artgen.GetAllKeyPairsResponseObject, error) {
	st, err := s.adminStore(ctx)
	if err != nil {
		return nil, err
	}
	pairs, err := st.Keys().Keypairs(ctx)
	if err != nil {
		return nil, s.internalError(ctx, "list key pairs", err)
	}
	out := make(artgen.GetAllKeyPairs200JSONResponse, 0, len(pairs))
	for _, k := range pairs {
		out = append(out, keypairSummary(k))
	}
	return out, nil
}

// CreateKeyPair stores a pair, replacing one of the same name as 7.x does
// (createkeypair). Repositories refer to a pair by name, so those using a
// replaced pair sign with the new material from the next request. The
// Terraform provider requires 201 with an empty object either way.
func (s SecurityAPI) CreateKeyPair(
	ctx context.Context,
	req artgen.CreateKeyPairRequestObject,
) (artgen.CreateKeyPairResponseObject, error) {
	st, err := s.adminStore(ctx)
	if err != nil {
		return nil, err
	}
	k, err := keypairFrom(req.Body)
	if err != nil {
		return nil, err
	}
	if err := putKeypair(ctx, st.Keys(), k); err != nil {
		return nil, s.internalError(ctx, "create key pair", err)
	}
	return artgen.CreateKeyPair201JSONResponse{}, nil
}

// putKeypair creates k or replaces the pair of its name. A pair deleted
// between the refused create and the update is created after all, so a
// concurrent delete cannot fail the request with a 500.
func putKeypair(ctx context.Context, keys port.KeyStore, k api.Keypair) error {
	err := keys.CreateKeypair(ctx, k)
	if errors.Is(err, kernel.ErrConflict) {
		err = keys.UpdateKeypair(ctx, k)
		if errors.Is(err, kernel.ErrNotFound) {
			err = keys.CreateKeypair(ctx, k)
		}
	}
	if err != nil {
		return fmt.Errorf("put key pair %q: %w", k.Name, err)
	}
	return nil
}

// UpdateKeyPair replaces the pair named in the body; repositories that
// reference it sign with the new material from the next request.
func (s SecurityAPI) UpdateKeyPair(
	ctx context.Context,
	req artgen.UpdateKeyPairRequestObject,
) (artgen.UpdateKeyPairResponseObject, error) {
	st, err := s.adminStore(ctx)
	if err != nil {
		return nil, err
	}
	k, err := keypairFrom(req.Body)
	if err != nil {
		return nil, err
	}
	switch err := st.Keys().UpdateKeypair(ctx, k); {
	case errors.Is(err, kernel.ErrNotFound):
		return nil, keypairNotFound(k.Name)
	case err != nil:
		return nil, s.internalError(ctx, "update key pair", err)
	}
	return artgen.UpdateKeyPair200JSONResponse(keypairSummary(k)), nil
}

// VerifyKeyPair checks a pair without storing it.
func (s SecurityAPI) VerifyKeyPair(
	ctx context.Context,
	req artgen.VerifyKeyPairRequestObject,
) (artgen.VerifyKeyPairResponseObject, error) {
	if err := httpkit.AdminOnly(ctx); err != nil {
		return nil, err
	}
	if _, err := keypairFrom(req.Body); err != nil {
		return nil, err
	}
	return artgen.VerifyKeyPair200TextResponse("Key was verified."), nil
}

// GetKeyPair returns one pair's readable projection, which the Terraform
// provider reads after every create. Any caller the middleware lets through
// may read it, as 7.x lets any authenticated user, and anonymous while
// anonymous access is on (getkeypair). That is safe only because the
// projection carries the public half alone: keypairSummary must never gain
// the private key or the passphrase.
func (s SecurityAPI) GetKeyPair(
	ctx context.Context,
	req artgen.GetKeyPairRequestObject,
) (artgen.GetKeyPairResponseObject, error) {
	st, err := s.content()
	if err != nil {
		return nil, err
	}
	switch k, err := st.Keys().Keypair(ctx, req.KeyPairName); {
	case errors.Is(err, kernel.ErrNotFound):
		return nil, keypairNotFound(req.KeyPairName)
	case err != nil:
		return nil, s.internalError(ctx, "get key pair", err)
	default:
		return artgen.GetKeyPair200JSONResponse(keypairSummary(k)), nil
	}
}

// DeleteKeyPair removes a pair, in use or not: 7.x documents no refusal
// (deletekeypair answers 200, 401, 403, 404 and 500 alone), and the
// Terraform provider may destroy a pair before the repositories that name
// it. Those repositories lose their reference in the same transaction.
// Left behind, it would name a pair that does not exist, which every later
// update of the repository would then be refused for (repos.CheckKeypairs).
func (s SecurityAPI) DeleteKeyPair(
	ctx context.Context,
	req artgen.DeleteKeyPairRequestObject,
) (artgen.DeleteKeyPairResponseObject, error) {
	st, err := s.adminStore(ctx)
	if err != nil {
		return nil, err
	}
	name := req.KeyPairName
	err = st.Tx(ctx, func(ctx context.Context) error {
		if _, err := st.Keys().Keypair(ctx, name); err != nil {
			return err //nolint:wrapcheck // ErrNotFound is the 404 below
		}
		if err := dropKeypairRefs(ctx, s.deps.Repositories, name); err != nil {
			return err
		}
		return st.Keys().DeleteKeypair(ctx, name) //nolint:wrapcheck // wrapped below
	})
	switch {
	case errors.Is(err, kernel.ErrNotFound):
		return nil, keypairNotFound(name)
	case err != nil:
		return nil, s.internalError(ctx, "delete key pair", err)
	}
	return artgen.DeleteKeyPair200TextResponse("OK"), nil
}

// dropKeypairRefs clears the key pair name from every repository that signs
// with it, as its primary or secondary pair. Each call takes ctx, so inside
// DeleteKeyPair's transaction the cleared references commit or roll back
// with the delete.
func dropKeypairRefs(ctx context.Context, repos artifactsapi.Repositories, name string) error {
	using, err := repos.UsingKeypair(ctx, name)
	if err != nil {
		return fmt.Errorf("repositories using key pair %q: %w", name, err)
	}
	for _, key := range using {
		c, err := repos.Get(ctx, key)
		if err != nil {
			return fmt.Errorf("repository %s: %w", key, err)
		}
		if c.PrimaryKeyPairRef == name {
			c.PrimaryKeyPairRef = ""
		}
		if c.SecondaryKeyPairRef == name {
			c.SecondaryKeyPairRef = ""
		}
		if err := repos.Update(ctx, c); err != nil {
			return fmt.Errorf("repository %s: %w", key, err)
		}
	}
	return nil
}

// publicKeyFile is the file name a repository's public key is sent under:
// the pair's alias, which the jfrog/artifactory provider documents as the
// file name of this download, then its type (rsa or gpg) and pub.
func publicKeyFile(k api.Keypair) string {
	return k.Alias + "." + strings.ToLower(string(k.Type)) + ".pub"
}

// GetKeyPairPublicKeyPerRepository returns the public half of the repository's
// signing pair, including Alpine, Debian and RPM virtual repositories' keyPair, which
// clients fetch to verify signed metadata. 7.x asks only for an authenticated
// user, or anonymous while anonymous access is on
// (getkeypairpublickeyperrepository), and no read
// permission on the repository, so a caller that may not read a repository
// still learns whether it exists and which key signs it.
func (s SecurityAPI) GetKeyPairPublicKeyPerRepository(
	ctx context.Context,
	req artgen.GetKeyPairPublicKeyPerRepositoryRequestObject,
) (artgen.GetKeyPairPublicKeyPerRepositoryResponseObject, error) {
	st, err := s.content()
	if err != nil {
		return nil, err
	}
	cfg, err := s.deps.Repositories.Get(ctx, req.RepoKey)
	switch {
	case errors.Is(err, kernel.ErrNotFound):
		return nil, repoNotFound(req.RepoKey)
	case err != nil:
		return nil, s.internalError(ctx, "get repository", err)
	}
	ref := cfg.PrimaryKeyPairRef
	if cfg.Class == repo.Virtual &&
		(cfg.PackageType == repo.Alpine || cfg.PackageType == repo.Debian ||
			cfg.PackageType == repo.RPM) {
		ref = cfg.KeyPair
	}
	if ref == "" {
		return nil, apierror.New(http.StatusNotFound,
			fmt.Sprintf("Repository '%s' has no key pair assigned", req.RepoKey))
	}
	switch k, err := st.Keys().Keypair(ctx, ref); {
	case errors.Is(err, kernel.ErrNotFound):
		return nil, keypairNotFound(ref)
	case err != nil:
		return nil, s.internalError(ctx, "get key pair", err)
	default:
		// An attachment with its file name, as the page's curl -O -J needs
		// to save the key; FormatMediaType quotes or encodes an alias that
		// is not a plain token.
		disposition := mime.FormatMediaType("attachment",
			map[string]string{"filename": publicKeyFile(k)})
		return artgen.GetKeyPairPublicKeyPerRepository200TextResponse{
			Body: k.PublicKey,
			Headers: artgen.GetKeyPairPublicKeyPerRepository200ResponseHeaders{
				ContentDisposition: &disposition,
			},
		}, nil
	}
}

// GetCertificates lists the client certificates remote repositories may
// present to upstreams.
func (s SecurityAPI) GetCertificates(
	ctx context.Context,
	_ artgen.GetCertificatesRequestObject,
) (artgen.GetCertificatesResponseObject, error) {
	st, err := s.adminStore(ctx)
	if err != nil {
		return nil, err
	}
	certs, err := st.Keys().Certificates(ctx)
	if err != nil {
		return nil, s.internalError(ctx, "list certificates", err)
	}
	out := make(artgen.GetCertificates200JSONResponse, 0, len(certs))
	for _, c := range certs {
		out = append(out, artgen.CertificateInfo{
			CertificateAlias: &c.Alias, FingerPrint: &c.Fingerprint,
			IssuedTo: &c.IssuedTo, IssuedBy: &c.IssuedBy,
			IssuedOn: &c.IssuedOn, ValidUntil: &c.ValidUntil,
		})
	}
	return out, nil
}

// AddCertificate stores a PEM bundle of a certificate and its private key
// under the alias, replacing any certificate already there.
func (s SecurityAPI) AddCertificate(
	ctx context.Context,
	req artgen.AddCertificateRequestObject,
) (artgen.AddCertificateResponseObject, error) {
	st, err := s.adminStore(ctx)
	if err != nil {
		return nil, err
	}
	alias := strings.TrimSpace(req.CertificateAlias)
	if alias == "" {
		return nil, httpkit.BadRequestf("A certificate alias is required")
	}
	if req.Body == nil {
		return nil, httpkit.BadRequestf("A PEM certificate bundle is required")
	}
	data, err := io.ReadAll(io.LimitReader(req.Body, maxCertificateBundle+1))
	switch {
	case err != nil:
		return nil, httpkit.BadRequestf("Cannot read the certificate bundle")
	case len(data) > maxCertificateBundle:
		return nil, httpkit.BadRequestf(
			"The certificate bundle is larger than %d bytes",
			maxCertificateBundle,
		)
	}
	b, err := keymaterial.ParseBundle(data)
	if err != nil {
		return nil, httpkit.BadRequestf("%s", err.Error())
	}
	if err := st.Keys().PutCertificate(ctx, api.Certificate{
		Alias: alias, PEM: b.Certificates, PrivateKey: b.PrivateKey,
		IssuedTo: b.IssuedTo, IssuedBy: b.IssuedBy, IssuedOn: b.IssuedOn,
		ValidUntil: b.ValidUntil, Fingerprint: b.Fingerprint,
	}); err != nil {
		return nil, s.internalError(ctx, "store certificate", err)
	}
	status := http.StatusOK
	msg := []string{fmt.Sprintf("Successfully added certificate '%s'", alias)}
	return artgen.AddCertificate200JSONResponse{Status: &status, Message: &msg}, nil
}

// DeleteCertificate removes a certificate no remote repository presents.
func (s SecurityAPI) DeleteCertificate(
	ctx context.Context,
	req artgen.DeleteCertificateRequestObject,
) (artgen.DeleteCertificateResponseObject, error) {
	st, err := s.adminStore(ctx)
	if err != nil {
		return nil, err
	}
	alias := req.CertificateAlias
	remotes, err := s.deps.Repositories.List(ctx, artifactsapi.RepoFilter{Class: repo.Remote})
	if err != nil {
		return nil, s.internalError(ctx, "list remote repositories", err)
	}
	var using []string
	for _, r := range remotes {
		if r.ClientTLSCertificate == alias {
			using = append(using, r.Key)
		}
	}
	if len(using) > 0 {
		return nil, httpkit.BadRequestf(
			"Certificate '%s' is used by repositories: %s",
			alias,
			strings.Join(using, ", "),
		)
	}
	switch err := st.Keys().DeleteCertificate(ctx, alias); {
	case errors.Is(err, kernel.ErrNotFound):
		return nil, certificateNotFound(alias)
	case err != nil:
		return nil, s.internalError(ctx, "delete certificate", err)
	}
	status := http.StatusOK
	msg := fmt.Sprintf("Successfully deleted certificate '%s'", alias)
	return artgen.DeleteCertificate200JSONResponse{Status: &status, Message: &msg}, nil
}
