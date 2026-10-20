package terraformstate

import (
	"bytes"
	"context"
	"errors"
	"fmt"

	"github.com/weaveplatform/artifactweave/internal/crypt"
	tfbe "github.com/weaveplatform/artifactweave/internal/format/terraformbackend"
	"github.com/weaveplatform/artifactweave/internal/service/item"
	"github.com/weaveplatform/artifactweave/pkg/aql"
	"github.com/weaveplatform/artifactweave/pkg/repo"
	"github.com/weaveplatform/artifactweave/pkg/store"
	"github.com/weaveplatform/artifactweave/pkg/types"
)

// ResealReport is what Reseal did.
type ResealReport struct {
	// Resealed counts the states sealed anew under the active key version.
	Resealed int
	// Unreadable lists the states sealed under an older version that did
	// not open, by repository and path. They were lost before the
	// rotation: retiring the old version loses nothing more.
	Unreadable []string
}

// Reseal seals every state of every terraformbackend repository that is
// sealed under an older key version anew under the active one, so the
// older master key can be retired (decision 0041, "encryptStates"), as
// `artifactweave keys rotate` does for the sealed columns. Each state's
// item keeps its path, times and properties; only its content, its
// checksums and its size change, and the index follows.
func (s *Service) Reseal(ctx context.Context) (ResealReport, error) {
	var rep ResealReport
	if s.Keys == nil {
		return rep, ErrNoKeyring
	}
	repos, err := s.store().Repos().List(ctx, store.RepoFilter{Class: repo.Local})
	if err != nil {
		return rep, fmt.Errorf("terraformstate: repositories: %w", err)
	}
	for _, c := range repos {
		if !tfbe.Serves(c.PackageType) {
			continue
		}
		hits, err := s.store().Items().Search(ctx, &aql.Query{Criteria: aql.And{
			aql.RepoSet{Refs: []aql.RepoRef{{Key: c.Key}}},
			&aql.FieldCriterion{Field: aql.FieldType, Op: aql.OpEq, Text: "file"},
			&aql.FieldCriterion{Field: aql.FieldPath, Op: aql.OpMatch, Text: tfbe.Root + "/*"},
		}}, aql.Options{})
		if err != nil {
			return rep, fmt.Errorf("terraformstate: states of %s: %w", c.Key, err)
		}
		for _, h := range hits {
			f, ok := tfbe.FileOf(h.Item)
			if !ok {
				continue
			}
			done, err := s.reseal(ctx, c, f.Workspace, h.Item)
			switch {
			case errors.Is(err, errUnreadable):
				rep.Unreadable = append(rep.Unreadable, c.Key+"/"+h.Item.FullPath())
			case err != nil:
				return rep, err
			case done:
				rep.Resealed++
			}
		}
	}
	return rep, nil
}

// errUnreadable is a state that is sealed under an older version and does
// not open.
var errUnreadable = errors.New("terraformstate: the state does not open")

// reseal seals it, a state of workspace ws, anew when an older version
// sealed it, and reports whether it did.
func (s *Service) reseal(
	ctx context.Context,
	c repo.Config,
	ws string,
	it types.Item,
) (bool, error) {
	stored, err := tfbe.ReadStored(ctx, s.Items.Blobs, it.Actual.SHA256)
	if err != nil {
		return false, err //nolint:wrapcheck // the format names it
	}
	if v, err := crypt.Version(stored); err != nil || v == s.Keys.Active() {
		// Plain, or sealed under the active version already.
		return false, nil
	}
	plain, err := tfbe.Open(s.Keys, c.Key, ws, stored)
	if err != nil {
		return false, fmt.Errorf("%w: %w", errUnreadable, err)
	}
	sealed, err := tfbe.Seal(s.Keys, c.Key, ws, plain)
	if err != nil {
		return false, err //nolint:wrapcheck // the format names it
	}
	meta := tfbe.MetaOf(s.Keys, c.Key, ws, sealed)
	info, err := s.Items.Upload(ctx, item.Deploy{
		Target: target(c, it.FullPath()), Body: bytes.NewReader(sealed),
	})
	if err != nil {
		return false, fmt.Errorf("terraformstate: store %s: %w", it.FullPath(), err)
	}
	actual := types.Checksums{
		SHA256: info.Digests.SHA256, SHA512: info.Digests.SHA512,
		SHA1: info.Digests.SHA1, MD5: info.Digests.MD5,
	}
	var done bool
	err = s.store().Tx(ctx, func(ctx context.Context) error {
		items := s.store().Items()
		cur, err := items.Get(ctx, c.Key, false, it.FullPath())
		if errors.Is(err, store.ErrNotFound) ||
			err == nil && cur.Actual.SHA256 != it.Actual.SHA256 {
			// Written or deleted since it was read: whatever is there now is
			// the active version's, or gone.
			return nil
		}
		if err != nil {
			return fmt.Errorf("terraformstate: get %s: %w", it.FullPath(), err)
		}
		if _, err := s.store().Blobs().Ref(ctx, types.Blob{
			Checksums: actual, Size: info.Size, Created: s.now(),
		}, 1); err != nil {
			return fmt.Errorf("terraformstate: reference %s: %w", it.FullPath(), err)
		}
		// The server wrote the state, so its checksums are its own, as they
		// were (item.Deploy.Trusted).
		cur.Actual, cur.Original, cur.Size = actual, actual, info.Size
		if cur, err = items.Put(ctx, cur); err != nil {
			return fmt.Errorf("terraformstate: put %s: %w", it.FullPath(), err)
		}
		if _, err := s.store().Blobs().Unref(ctx, it.Actual.SHA256, 1); err != nil {
			return fmt.Errorf("terraformstate: release %s: %w", it.FullPath(), err)
		}
		env := s.Items.Env()
		env.Inspection = &meta
		h := tfbe.Handler{Settings: tfbe.Settings{Keys: s.Keys}}
		if err := h.Deployed(ctx, env, c, cur); err != nil {
			return err //nolint:wrapcheck // the format's own error
		}
		done = true
		return nil
	})
	if err != nil {
		return false, err //nolint:wrapcheck // the transaction returns ours
	}
	return done, nil
}
