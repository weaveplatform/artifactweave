package alpine

import (
	"bytes"
	"context"
	"crypto/rsa"
	"errors"
	"fmt"
	"strings"

	"github.com/weaveplatform/artifactweave/internal/format"
	"github.com/weaveplatform/artifactweave/internal/keymaterial"
	"github.com/weaveplatform/artifactweave/pkg/repo"
	"github.com/weaveplatform/artifactweave/pkg/store"
	"github.com/weaveplatform/artifactweave/pkg/types"
)

const maxIndexEntries = 100000

// Work derives every current architecture index from the stored packages.
// A single repository work key coalesces bursts of writes and restores, and
// a rerun reads the committed tree afresh.
func (Handler) Work(ctx context.Context, env format.Env, work types.WorkItem) error {
	if work.Kind != KindIndex {
		return nil
	}
	c, err := env.Store.Repos().Get(ctx, work.Key)
	switch {
	case errors.Is(err, store.ErrNotFound):
		return nil
	case err != nil:
		return fmt.Errorf("alpine: repository: %w", err)
	case !Serves(c.PackageType) || !derives(c):
		return nil
	}
	items, err := files(ctx, env.Store, c)
	if err != nil {
		return err
	}
	byFolder := map[string][]Package{}
	oldIndexes := map[string]bool{}
	count := 0
	for _, it := range items {
		p := it.FullPath()
		if strings.HasSuffix(p, "/"+IndexName) {
			oldIndexes[p] = true
			continue
		}
		if !strings.HasSuffix(p, ".apk") {
			continue
		}
		pkg, err := packageFor(ctx, env, it)
		if err != nil {
			return err
		}
		folder := p[:strings.LastIndexByte(p, '/')]
		byFolder[folder] = append(byFolder[folder], pkg)
		count++
		if count > maxIndexEntries {
			return fmt.Errorf("%w: more than %d entries", errInvalidIndex, maxIndexEntries)
		}
	}
	keyName, key, err := SigningKey(ctx, env.Store, c)
	if err != nil {
		return err
	}
	for folder, packages := range byFolder {
		indexPath := folder + "/" + IndexName
		body, err := EncodeIndex(packages, "alpine "+c.Key+" "+folder, keyName, key)
		if err != nil {
			return err
		}
		if _, err := env.Derive(ctx, format.Derived{
			Repo: c, Path: indexPath, Body: bytes.NewReader(body), By: "_system_",
		}); err != nil {
			return fmt.Errorf("alpine: derive %s: %w", indexPath, err)
		}
		delete(oldIndexes, indexPath)
	}
	for path := range oldIndexes {
		if err := env.Underive(ctx, c, false, path); err != nil {
			return fmt.Errorf("alpine: remove stale %s: %w", path, err)
		}
	}
	return nil
}

// SigningKey selects the repository's configured apk signing pair. Virtual
// repositories use keyPair; local and federated repositories use the primary
// keypair shared with Artifactory's public-key download endpoint.
func SigningKey(
	ctx context.Context,
	st store.Store,
	c repo.Config,
) (string, *rsa.PrivateKey, error) {
	ref := c.PrimaryKeyPairRef
	if c.Class == repo.Virtual {
		ref = c.KeyPair
	}
	if ref == "" {
		return "", nil, nil
	}
	pair, err := st.Keys().Keypair(ctx, ref)
	if err != nil {
		return "", nil, fmt.Errorf("alpine: signing key %s: %w", ref, err)
	}
	if pair.Type != types.KeypairRSA {
		return "", nil, fmt.Errorf("%w: signing key %s must be rsa", errInvalidIndex, pair.Name)
	}
	private, err := keymaterial.RSAPrivateKey(pair.PrivateKey, pair.Passphrase)
	if err != nil {
		return "", nil, fmt.Errorf("alpine: signing key %s: %w", pair.Name, err)
	}
	// The public-key download endpoint names the file from the alias, which
	// apk uses to match the .SIGN entry against /etc/apk/keys.
	name := pair.Alias
	if name == "" {
		name = pair.Name
	}
	if !strings.HasSuffix(name, ".rsa.pub") {
		name += ".rsa.pub"
	}
	return name, private, nil
}
