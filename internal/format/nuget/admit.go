package nuget

import (
	"context"
	"errors"
	"fmt"
	"net/http"
	"path"

	"github.com/weaveplatform/artifactweave/internal/format"
	"github.com/weaveplatform/artifactweave/internal/format/generic"
	"github.com/weaveplatform/artifactweave/pkg/repo"
	"github.com/weaveplatform/artifactweave/pkg/store"
)

// Admit applies Enforce Layout's rules (decision 0038, "Storage and the
// package index") to every write that lands a .nupkg or .snupkg in the
// repository: a REST deploy, a checksum deploy, an archive's entry, a
// push, a copy and a move. Then come the deploy policy of every package
// type, blackedOut and the include and exclude patterns. The package
// checks go first because decision 0038 orders a push's 409 for a
// duplicate before the item routes' own refusals. A checksum upload for an
// existing file lands no package, and a remote repository's cache stores
// what its upstream serves, so neither is checked.
func (Handler) Admit(
	ctx context.Context,
	env format.Env,
	c repo.Config,
	w format.Write,
) (format.Write, error) {
	if w.Op != format.OpChecksum && !w.Cached {
		if err := checkPackage(ctx, env, c, w); err != nil {
			return w, err
		}
	}
	if err := generic.CheckBlackedOut(c, w.Path); err != nil {
		return w, err //nolint:wrapcheck // the generic policy's RejectionError, answered as it is
	}
	if err := generic.CheckPatterns(c, w.Path); err != nil {
		return w, err //nolint:wrapcheck // the generic policy's RejectionError, answered as it is
	}
	return w, nil
}

// checkPackage refuses with 409 a .nupkg or .snupkg that is no valid
// package, one whose file name is not its canonical name compared ignoring
// case, and one of a version that already has a file of its kind at
// another path: a second package, or second symbols. A write at the
// version's own path is an overwrite, which the item routes govern, and a
// move of the version's own file elsewhere in the repository takes it
// along. JFrog documents 409 for a name that breaks the conventions and
// for a duplicate; [I] the messages are inferred (#216).
func checkPackage(ctx context.Context, env format.Env, c repo.Config, w format.Write) error {
	k := kindOf(w.Path)
	in, ok := env.Inspection.(*inspection)
	if k == other || !ok {
		return nil
	}
	n := in.nuspec
	if n == nil {
		return format.Rejectf(http.StatusConflict, "The repository '%s' rejected '%s': %s.",
			c.Key, w.Path, in.invalid)
	}
	if !canonical(w.Path, k, n) {
		want := FileName(n.ID, n.Version)
		if k == symbolsFile {
			want = SymbolsFileName(n.ID, n.Version)
		}
		return format.Rejectf(http.StatusConflict,
			"The repository '%s' rejected '%s': the file name of %s %s must be %s.",
			c.Key, w.Path, n.ID, n.Version.Normalized(), want)
	}
	taken, err := held(ctx, env.Store, c, keyOf(n), k, w)
	if err != nil || !taken {
		return err
	}
	what := ""
	if k == symbolsFile {
		what = "the symbols of "
	}
	return format.Rejectf(http.StatusConflict, "The repository '%s' already contains %s%s %s",
		c.Key, what, n.ID, n.Version.Normalized())
}

// held reports whether version key of c has a file of kind k other than
// the one w writes or moves.
func held(
	ctx context.Context,
	st store.Store,
	c repo.Config,
	key versionKey,
	k kind,
	w format.Write,
) (bool, error) {
	p, err := st.Packages().Find(ctx, c.Key, key.pkg)
	if errors.Is(err, store.ErrNotFound) {
		return false, nil
	}
	if err != nil {
		return false, fmt.Errorf("nuget: package %s: %w", key.pkg, err)
	}
	pv, err := st.Packages().Version(ctx, p.ID, key.version)
	if errors.Is(err, store.ErrNotFound) {
		return false, nil
	}
	if err != nil {
		return false, fmt.Errorf("nuget: version %s of %s: %w", key.version, key.pkg, err)
	}
	ids, err := st.Packages().Files(ctx, pv.ID)
	if err != nil {
		return false, fmt.Errorf("nuget: files of %s %s: %w", key.pkg, key.version, err)
	}
	for _, id := range ids {
		if w.Op == format.OpMove && w.From.ID == id && w.From.RepoKey == c.Key {
			continue
		}
		it, err := st.Items().GetByID(ctx, id)
		if err != nil {
			return false, fmt.Errorf("nuget: file %d of %s %s: %w", id, key.pkg, key.version, err)
		}
		if kindOf(it.FullPath()) == k && it.FullPath() != path.Clean(w.Path) {
			return true, nil
		}
	}
	return false, nil
}
