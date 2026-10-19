package server

import (
	"context"
	"errors"
	"fmt"
	"io"
	"net/http"
	"strings"

	"github.com/weaveplatform/artifactweave/internal/keymaterial"
	"github.com/weaveplatform/artifactweave/internal/server/apierror"
	artgen "github.com/weaveplatform/artifactweave/internal/server/gen/artifactory"
	"github.com/weaveplatform/artifactweave/pkg/repo"
	"github.com/weaveplatform/artifactweave/pkg/store"
	"github.com/weaveplatform/artifactweave/pkg/types"
)

// maxCertificateBundle bounds a certificate upload: a PEM certificate, its
// chain and key are a few kilobytes.
const maxCertificateBundle = 1 << 20

func keypairNotFound(name string) error {
	return apierror.New(http.StatusNotFound, fmt.Sprintf("Key pair '%s' does not exist", name))
}

func certificateNotFound(alias string) error {
	return apierror.New(http.StatusNotFound, fmt.Sprintf("Certificate '%s' does not exist", alias))
}

func keypairSummary(k types.Keypair) artgen.KeyPairSummary {
	typ := string(k.Type)
	return artgen.KeyPairSummary{
		PairName: &k.Name, PairType: &typ, Alias: &k.Alias, PublicKey: &k.PublicKey,
	}
}

// keypairFrom validates a key pair body: the required fields, no vault key
// without a private key (artifactweave has no vault integration), and
// halves that belong together.
func keypairFrom(in *artgen.KeyPairInput) (types.Keypair, error) {
	if in == nil {
		return types.Keypair{}, badRequest("A key pair body is required")
	}
	priv, pass := "", ""
	if in.PrivateKey != nil {
		priv = *in.PrivateKey
	}
	if in.Passphrase != nil {
		pass = *in.Passphrase
	}
	if in.VaultKey != nil && *in.VaultKey != "" && priv == "" {
		return types.Keypair{}, badRequest("Vault keys are not supported; supply privateKey")
	}
	for field, value := range map[string]string{
		"pairName": in.PairName, "pairType": string(in.PairType), "alias": in.Alias,
		"privateKey": priv, "publicKey": in.PublicKey,
	} {
		if strings.TrimSpace(value) == "" {
			return types.Keypair{}, badRequest("'%s' is required", field)
		}
	}
	if err := keymaterial.CheckPair(string(in.PairType), priv, in.PublicKey, pass); err != nil {
		return types.Keypair{}, badRequest("%s", err.Error())
	}
	return types.Keypair{
		Name: in.PairName, Type: types.KeypairType(in.PairType), Alias: in.Alias,
		PublicKey: in.PublicKey, PrivateKey: priv, Passphrase: pass,
	}, nil
}

// adminStore is the store of an admin-only operation.
func (s artifactoryService) adminStore(ctx context.Context) (store.Store, error) {
	if err := adminOnly(ctx); err != nil {
		return nil, err
	}
	return s.content()
}

// GetAllKeyPairs lists every key pair without private material.
func (s artifactoryService) GetAllKeyPairs(
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
func (s artifactoryService) CreateKeyPair(
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
func putKeypair(ctx context.Context, keys store.KeyStore, k types.Keypair) error {
	err := keys.CreateKeypair(ctx, k)
	if errors.Is(err, store.ErrConflict) {
		err = keys.UpdateKeypair(ctx, k)
		if errors.Is(err, store.ErrNotFound) {
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
func (s artifactoryService) UpdateKeyPair(
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
	case errors.Is(err, store.ErrNotFound):
		return nil, keypairNotFound(k.Name)
	case err != nil:
		return nil, s.internalError(ctx, "update key pair", err)
	}
	return artgen.UpdateKeyPair200JSONResponse(keypairSummary(k)), nil
}

// VerifyKeyPair checks a pair without storing it.
func (s artifactoryService) VerifyKeyPair(
	ctx context.Context,
	req artgen.VerifyKeyPairRequestObject,
) (artgen.VerifyKeyPairResponseObject, error) {
	if err := adminOnly(ctx); err != nil {
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
func (s artifactoryService) GetKeyPair(
	ctx context.Context,
	req artgen.GetKeyPairRequestObject,
) (artgen.GetKeyPairResponseObject, error) {
	st, err := s.content()
	if err != nil {
		return nil, err
	}
	switch k, err := st.Keys().Keypair(ctx, req.KeyPairName); {
	case errors.Is(err, store.ErrNotFound):
		return nil, keypairNotFound(req.KeyPairName)
	case err != nil:
		return nil, s.internalError(ctx, "get key pair", err)
	default:
		return artgen.GetKeyPair200JSONResponse(keypairSummary(k)), nil
	}
}

// DeleteKeyPair removes a pair no repository uses: a signed repository must
// not lose its key silently.
func (s artifactoryService) DeleteKeyPair(
	ctx context.Context,
	req artgen.DeleteKeyPairRequestObject,
) (artgen.DeleteKeyPairResponseObject, error) {
	st, err := s.adminStore(ctx)
	if err != nil {
		return nil, err
	}
	name := req.KeyPairName
	using, err := st.Repos().UsingKeypair(ctx, name)
	if err != nil {
		return nil, s.internalError(ctx, "repositories using a key pair", err)
	}
	if len(using) > 0 {
		return nil, badRequest(
			"Key pair '%s' is used by repositories: %s",
			name,
			strings.Join(using, ", "),
		)
	}
	switch err := st.Keys().DeleteKeypair(ctx, name); {
	case errors.Is(err, store.ErrNotFound):
		return nil, keypairNotFound(name)
	case err != nil:
		return nil, s.internalError(ctx, "delete key pair", err)
	}
	return artgen.DeleteKeyPair200TextResponse("OK"), nil
}

// GetKeyPairPublicKeyPerRepository returns the public half of a
// repository's primary pair, which package clients fetch to verify signed
// metadata. 7.x asks only for an authenticated user, or anonymous while
// anonymous access is on (getkeypairpublickeyperrepository), and no read
// permission on the repository, so a caller that may not read a repository
// still learns whether it exists and which key signs it.
func (s artifactoryService) GetKeyPairPublicKeyPerRepository(
	ctx context.Context,
	req artgen.GetKeyPairPublicKeyPerRepositoryRequestObject,
) (artgen.GetKeyPairPublicKeyPerRepositoryResponseObject, error) {
	st, err := s.content()
	if err != nil {
		return nil, err
	}
	cfg, err := st.Repos().Get(ctx, req.RepoKey)
	switch {
	case errors.Is(err, store.ErrNotFound):
		return nil, notFoundRepo(req.RepoKey)
	case err != nil:
		return nil, s.internalError(ctx, "get repository", err)
	case cfg.PrimaryKeyPairRef == "":
		return nil, apierror.New(http.StatusNotFound,
			fmt.Sprintf("Repository '%s' has no key pair assigned", req.RepoKey))
	}
	switch k, err := st.Keys().Keypair(ctx, cfg.PrimaryKeyPairRef); {
	case errors.Is(err, store.ErrNotFound):
		return nil, keypairNotFound(cfg.PrimaryKeyPairRef)
	case err != nil:
		return nil, s.internalError(ctx, "get key pair", err)
	default:
		return artgen.GetKeyPairPublicKeyPerRepository200TextResponse(k.PublicKey), nil
	}
}

// GetCertificates lists the client certificates remote repositories may
// present to upstreams.
func (s artifactoryService) GetCertificates(
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
func (s artifactoryService) AddCertificate(
	ctx context.Context,
	req artgen.AddCertificateRequestObject,
) (artgen.AddCertificateResponseObject, error) {
	st, err := s.adminStore(ctx)
	if err != nil {
		return nil, err
	}
	alias := strings.TrimSpace(req.CertificateAlias)
	if alias == "" {
		return nil, badRequest("A certificate alias is required")
	}
	if req.Body == nil {
		return nil, badRequest("A PEM certificate bundle is required")
	}
	data, err := io.ReadAll(io.LimitReader(req.Body, maxCertificateBundle+1))
	switch {
	case err != nil:
		return nil, badRequest("Cannot read the certificate bundle")
	case len(data) > maxCertificateBundle:
		return nil, badRequest(
			"The certificate bundle is larger than %d bytes",
			maxCertificateBundle,
		)
	}
	b, err := keymaterial.ParseBundle(data)
	if err != nil {
		return nil, badRequest("%s", err.Error())
	}
	if err := st.Keys().PutCertificate(ctx, types.Certificate{
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
func (s artifactoryService) DeleteCertificate(
	ctx context.Context,
	req artgen.DeleteCertificateRequestObject,
) (artgen.DeleteCertificateResponseObject, error) {
	st, err := s.adminStore(ctx)
	if err != nil {
		return nil, err
	}
	alias := req.CertificateAlias
	remotes, err := st.Repos().List(ctx, store.RepoFilter{Class: repo.Remote})
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
		return nil, badRequest(
			"Certificate '%s' is used by repositories: %s",
			alias,
			strings.Join(using, ", "),
		)
	}
	switch err := st.Keys().DeleteCertificate(ctx, alias); {
	case errors.Is(err, store.ErrNotFound):
		return nil, certificateNotFound(alias)
	case err != nil:
		return nil, s.internalError(ctx, "delete certificate", err)
	}
	status := http.StatusOK
	msg := fmt.Sprintf("Successfully deleted certificate '%s'", alias)
	return artgen.DeleteCertificate200JSONResponse{Status: &status, Message: &msg}, nil
}
