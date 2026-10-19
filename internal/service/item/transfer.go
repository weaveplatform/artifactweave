package item

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"path"
	"slices"
	"strings"
	"time"

	"github.com/weaveplatform/artifactweave/internal/format"
	"github.com/weaveplatform/artifactweave/internal/service/pkgindex"
	"github.com/weaveplatform/artifactweave/pkg/layout"
	"github.com/weaveplatform/artifactweave/pkg/repo"
	"github.com/weaveplatform/artifactweave/pkg/store"
	"github.com/weaveplatform/artifactweave/pkg/types"
)

// Levels of a copy or move message (the CopyOrMoveMessage enum).
const (
	LevelInfo    = "INFO"
	LevelWarning = "WARNING"
	LevelError   = "ERROR"
)

// ErrInvalidTransfer is a copy or move that cannot run at all: onto itself,
// into its own subtree, or below a file at the target (400).
var ErrInvalidTransfer = errors.New("item: invalid copy or move")

// ErrNoSource is a copy or move whose source does not exist (400 with a
// report). It wraps store.ErrNotFound, so a caller that tests for that,
// such as a promotion whose file went in the meantime, still sees a miss.
var ErrNoSource = fmt.Errorf("item: no source: %w", store.ErrNotFound)

// Message is one line of a copy or move report.
type Message struct {
	Level string
	Text  string
}

// Transfer is a copy or a move of the item at From.Path (a file, a folder
// and everything below it, or "" for the whole repository) to To.
type Transfer struct {
	From Target
	To   Target
	// IntoFolder is a target given with a trailing slash: the source lands
	// inside it under its own name, as it does inside an existing folder.
	IntoFolder bool
	Move       bool
	Dry        bool
	FailFast   bool
	// TranslateLayouts converts file paths from the source repository's
	// layout to the target's (suppressLayouts=0).
	TranslateLayouts bool
	// CanOverwrite is the caller's right to replace files at the target.
	CanOverwrite bool
	By           string
}

// TransferResult reports a copy or move. Files and Folders count what was
// (or, in a dry run, would be) transferred; Failed counts the items that
// were not.
type TransferResult struct {
	Messages []Message
	Files    int
	Folders  int
	Failed   int
}

// step is one planned item: the source and its target path, and the file a
// copied file replaces. A file also carries the properties it lands with,
// as the target's format admitted them, and what that format inspected.
type step struct {
	src        types.Item
	dst        string
	replace    types.Item
	props      types.Properties
	inspection any
}

// Transfer copies or moves t.From to t.To. Every item is planned first:
// an item that cannot land (a file where a folder must go, or the reverse,
// an overwrite the caller may not make, or a file the target's format
// refuses) is reported and skipped with its subtree, or ends the plan when
// t.FailFast. The planned items are then written in one transaction; a dry
// run stops after the plan.
func (s *Service) Transfer(ctx context.Context, t Transfer) (TransferResult, error) {
	var res TransferResult
	steps, err := s.plan(ctx, t, &res)
	if err != nil {
		return res, err
	}
	for _, st := range steps {
		if st.src.Folder {
			res.Folders++
		} else {
			res.Files++
		}
	}
	if !t.Dry && len(steps) > 0 {
		if err := s.Store.Tx(ctx, func(ctx context.Context) error {
			return s.apply(ctx, t, steps)
		}); err != nil {
			return TransferResult{}, err //nolint:wrapcheck // the transaction returns ours
		}
	}
	res.Messages = append(res.Messages, summary(t, res))
	return res, nil
}

// summary is the closing message, in the wording Artifactory's copy and move
// reports use for a completed operation.
func summary(t Transfer, res TransferResult) Message {
	verb, done := "copying", "copied"
	if t.Move {
		verb, done = "moving", "moved"
	}
	text := fmt.Sprintf("%s %s:%s to %s:%s", verb, t.From.Repo.Key, t.From.Path,
		t.To.Repo.Key, t.To.Path)
	if t.Dry {
		text = "Dry run for " + text
	}
	if res.Failed == 0 {
		return Message{
			LevelInfo,
			fmt.Sprintf("%s completed successfully, %d artifacts and %d folders were %s",
				text, res.Files, res.Folders, done),
		}
	}
	return Message{LevelWarning, fmt.Sprintf(
		"%s completed with errors, %d artifacts and %d folders were %s, %d items failed",
		text, res.Files, res.Folders, done, res.Failed)}
}

func (s *Service) plan(ctx context.Context, t Transfer, res *TransferResult) ([]step, error) {
	items := s.Store.Items()
	srcFull := strings.Trim(t.From.Path, "/")
	toFull := strings.Trim(t.To.Path, "/")
	root := types.Item{RepoKey: t.From.Repo.Key, Cached: t.From.Cached, Folder: true}
	if srcFull != "" {
		var err error
		root, err = items.Get(ctx, t.From.Repo.Key, t.From.Cached, srcFull)
		switch {
		case errors.Is(err, store.ErrNotFound):
			return nil, fmt.Errorf("%w: %s:%s", ErrNoSource, t.From.Repo.Key, srcFull)
		case err != nil:
			return nil, fmt.Errorf("item: source %s: %w", srcFull, err)
		}
	}
	dstRoot := toFull
	if srcFull != "" {
		into := t.IntoFolder || toFull == ""
		if !into {
			existing, err := items.Get(ctx, t.To.Repo.Key, false, toFull)
			switch {
			case err == nil:
				into = existing.Folder
			case !errors.Is(err, store.ErrNotFound):
				return nil, fmt.Errorf("item: target %s: %w", toFull, err)
			}
		}
		if into {
			dstRoot = join(toFull, path.Base(srcFull))
		}
	}
	sameRepo := t.From.Repo.Key == t.To.Repo.Key && !t.From.Cached
	switch {
	case sameRepo && dstRoot == srcFull:
		return nil, fmt.Errorf("%w: the source and the target are the same", ErrInvalidTransfer)
	case sameRepo && root.Folder && (srcFull == "" || strings.HasPrefix(dstRoot, srcFull+"/")):
		return nil, fmt.Errorf(
			"%w: a folder cannot be copied or moved into itself",
			ErrInvalidTransfer,
		)
	}
	if err := s.checkAncestors(ctx, t.To, dstRoot); err != nil {
		return nil, err
	}
	// The source and everything below it, parents before their children.
	sources, err := s.affected(ctx, t.From, srcFull)
	if err != nil {
		return nil, err
	}
	tr, err := s.translator(ctx, t, toFull, res)
	if err != nil {
		return nil, err
	}
	return s.planItems(ctx, t, res, sources, srcFull, dstRoot, tr)
}

// planItems plans each of sources, the source at srcFull and everything
// below it, under dstRoot or, with tr, where tr translates each file.
func (s *Service) planItems(
	ctx context.Context,
	t Transfer,
	res *TransferResult,
	sources []types.Item,
	srcFull, dstRoot string,
	tr *translation,
) ([]step, error) {
	hooks, env := s.format(t.To.Repo.PackageType), s.env()
	var steps []step
	var skipped []string
	for _, src := range sources {
		full := src.FullPath()
		if slices.ContainsFunc(skipped, func(p string) bool {
			return strings.HasPrefix(full, p+"/")
		}) {
			res.Failed++
			continue
		}
		dst := join(dstRoot, strings.TrimPrefix(strings.TrimPrefix(full, srcFull), "/"))
		var problem, warning string
		if tr != nil {
			if src.Folder {
				continue
			}
			dst, problem, warning = tr.path(full)
		}
		var st step
		if problem == "" {
			var err error
			if st, problem, err = s.planStep(ctx, hooks, env, t, src, dst); err != nil {
				return nil, err
			}
		}
		if problem == "" && tr != nil {
			problem = tr.claim(st.dst, full)
		}
		if problem != "" {
			res.Failed++
			res.Messages = append(res.Messages, Message{LevelError, problem})
			if src.Folder {
				skipped = append(skipped, full)
			}
			if t.FailFast {
				break
			}
			continue
		}
		// Only a file that lands is reported as kept: one refused for
		// another reason has its error instead.
		if warning != "" {
			res.Messages = append(res.Messages, Message{LevelWarning, warning})
		}
		steps = append(steps, st)
	}
	return steps, nil
}

// planStep plans one item at dst: what the target's format admits there,
// then what is in its way.
func (s *Service) planStep(
	ctx context.Context,
	hooks format.Handler,
	env format.Env,
	t Transfer,
	src types.Item,
	dst string,
) (step, string, error) {
	st, problem, err := s.admitStep(ctx, hooks, env, t, src, dst)
	if err != nil || problem != "" {
		return st, problem, err
	}
	return s.check(ctx, t, st)
}

// admitStep plans where a file lands as the target's format inspects and
// admits it, or says why the format refuses it. A folder lands as planned.
func (s *Service) admitStep(
	ctx context.Context,
	hooks format.Handler,
	env format.Env,
	t Transfer,
	src types.Item,
	dst string,
) (step, string, error) {
	st := step{src: src, dst: dst}
	if src.Folder {
		return st, "", nil
	}
	props, err := s.Store.Items().Properties(ctx, src.ID)
	if err != nil && !errors.Is(err, store.ErrNotFound) {
		return st, "", fmt.Errorf("item: properties of %s: %w", src.FullPath(), err)
	}
	op := format.OpCopy
	if t.Move {
		op = format.OpMove
	}
	w := format.Write{
		Op: op, Path: dst, Properties: props,
		Size: src.Size, Checksums: src.Actual, From: src,
	}
	if env, err = inspect(ctx, hooks, env, t.To.Repo, w); err == nil {
		w, err = admit(ctx, hooks, env, t.To.Repo, w)
	}
	var refused *format.Rejection
	if errors.As(err, &refused) {
		return st, fmt.Sprintf("Cannot transfer %s:%s to %s:%s: %s",
			t.From.Repo.Key, src.FullPath(), t.To.Repo.Key, dst, refused.Message), nil
	}
	if err != nil {
		return st, "", err
	}
	st.dst, st.props, st.inspection = w.Path, w.Properties, env.Inspection
	return st, "", nil
}

// check plans one item, or says why it cannot land.
func (s *Service) check(ctx context.Context, t Transfer, st step) (step, string, error) {
	src, dst := st.src, st.dst
	existing, err := s.Store.Items().Get(ctx, t.To.Repo.Key, false, dst)
	if errors.Is(err, store.ErrNotFound) {
		return st, "", nil
	}
	if err != nil {
		return st, "", fmt.Errorf("item: target %s: %w", dst, err)
	}
	label := fmt.Sprintf("%s:%s to %s:%s", t.From.Repo.Key, src.FullPath(), t.To.Repo.Key, dst)
	switch {
	case src.Folder && !existing.Folder:
		return st, "Cannot transfer the folder " + label + ": a file is in the way", nil
	case !src.Folder && existing.Folder:
		return st, "Cannot transfer the file " + label + ": a folder is in the way", nil
	case !src.Folder && !t.CanOverwrite:
		return st, "Cannot transfer " + label + ": no permission to overwrite the existing file", nil
	case !src.Folder:
		st.replace = existing
	}
	return st, "", nil
}

// checkAncestors refuses a target below an existing file.
func (s *Service) checkAncestors(ctx context.Context, t Target, dst string) error {
	segments := strings.Split(dst, "/")
	for i := 1; i < len(segments); i++ {
		p := strings.Join(segments[:i], "/")
		it, err := s.Store.Items().Get(ctx, t.Repo.Key, false, p)
		switch {
		case err == nil && !it.Folder:
			return fmt.Errorf("%w: the target %s is below the file %s", ErrInvalidTransfer, dst, p)
		case err != nil && !errors.Is(err, store.ErrNotFound):
			return fmt.Errorf("item: target %s: %w", p, err)
		}
	}
	return nil
}

// translator returns the path conversion between the repositories' layouts,
// or nil when there is none to make. Layout patterns describe a path from
// the repository root, so a translation needs the target to be that root;
// otherwise the paths are kept, with a warning.
func (s *Service) translator(
	ctx context.Context,
	t Transfer,
	toFull string,
	res *TransferResult,
) (*translation, error) {
	from, to := t.From.Repo.RepoLayoutRef, t.To.Repo.RepoLayoutRef
	if !t.TranslateLayouts || from == "" || to == "" || from == to {
		return nil, nil //nolint:nilnil // no translation to make
	}
	if toFull != "" {
		res.Messages = append(res.Messages, Message{
			LevelWarning,
			"Layout translation needs the target repository's root as the target; paths are kept",
		})
		return nil, nil //nolint:nilnil // no translation to make
	}
	src, err := s.Layout(ctx, from)
	if err != nil {
		return nil, err
	}
	dst, err := s.Layout(ctx, to)
	if err != nil {
		return nil, err
	}
	return &translation{t: t, from: src, to: dst, landed: map[string]string{}}, nil
}

// translation converts one transfer's file paths between two layouts. It
// never lands two source paths on one target path, which would write one
// version over another (#496).
type translation struct {
	t        Transfer
	from, to *layout.Layout
	// landed maps each planned target path to its source.
	landed map[string]string
}

// path is where the file at p lands, or the problem why it may not: a path
// the target layout would give another source path too
// (layout.Translate). A path the source layout does not read, or the
// target cannot build, is kept, with a warning that says so (#505), as
// Artifactory notes conversion failures among a copy's warnings
// (RTFACT-3944); without one a client could not tell a kept path from a
// translated one.
func (tr *translation) path(p string) (dst, problem, warning string) {
	out, err := layout.Translate(tr.from, tr.to, p)
	var lossy *layout.LossError
	switch {
	case errors.As(err, &lossy):
		return p, fmt.Sprintf("Cannot transfer %s:%s to %s:%s: %v",
			tr.t.From.Repo.Key, p, tr.t.To.Repo.Key, lossy.Target, lossy), ""
	case err != nil:
		return p, "", tr.untranslated(p, err)
	}
	return out, "", ""
}

// untranslated is the warning for the file at p, which the translation
// keeps at its own path because of err.
func (tr *translation) untranslated(p string, err error) string {
	from, to := tr.from.Definition().Name, tr.to.Definition().Name
	why := fmt.Sprintf("layout %s cannot build a path from what layout %s reads in it", to, from)
	switch {
	case errors.Is(err, layout.ErrNoMatch):
		why = fmt.Sprintf("layout %s does not read it", from)
	case errors.Is(err, layout.ErrUnbuildable):
		why = fmt.Sprintf("layout %s cannot build a path", to)
	}
	done := "copied"
	if tr.t.Move {
		done = "moved"
	}
	return fmt.Sprintf("Cannot translate %s:%s from layout %s to layout %s: %s; it is %s to %s:%s",
		tr.t.From.Repo.Key, p, from, to, why, done, tr.t.To.Repo.Key, p)
}

// claim plans the file at src to land at dst, or says why it may not. A
// kept path and a translated one can meet, which layout.Translate cannot
// see: the second of them is refused.
func (tr *translation) claim(dst, src string) string {
	if other, taken := tr.landed[dst]; taken {
		return fmt.Sprintf("Cannot transfer %s:%s to %s:%s: %s:%s lands there too",
			tr.t.From.Repo.Key, src, tr.t.To.Repo.Key, dst, tr.t.From.Repo.Key, other)
	}
	tr.landed[dst] = src
	return ""
}

// Layout compiles a repository layout: one the descriptor stored, else a
// bundled one.
func (s *Service) Layout(ctx context.Context, name string) (*layout.Layout, error) {
	doc, err := s.Store.Configs().Get(ctx, types.ConfigLayout, name)
	if errors.Is(err, store.ErrNotFound) {
		l, err := layout.Lookup(name)
		if err != nil {
			return nil, fmt.Errorf("item: layout %s: %w", name, err)
		}
		return l, nil
	}
	if err != nil {
		return nil, fmt.Errorf("item: layout %s: %w", name, err)
	}
	// The stored document carries the descriptor's camelCase keys, which
	// internal/descriptor validated; read them as it does, through a map.
	var m map[string]any
	if err := json.Unmarshal(doc.Document, &m); err != nil {
		return nil, fmt.Errorf("item: layout %s: %w", name, err)
	}
	str := func(k string) string { v, _ := m[k].(string); return v }
	distinct, _ := m["distinctiveDescriptorPathPattern"].(bool)
	def := layout.Definition{
		Name: name, ArtifactPathPattern: str("artifactPathPattern"),
		DistinctiveDescriptorPathPattern: distinct,
		DescriptorPathPattern:            str("descriptorPathPattern"),
		FolderIntegrationRevisionRegExp:  str("folderIntegrationRevisionRegExp"),
		FileIntegrationRevisionRegExp:    str("fileIntegrationRevisionRegExp"),
	}
	l, err := layout.Compile(def)
	if err != nil {
		return nil, fmt.Errorf("item: layout %s: %w", name, err)
	}
	return l, nil
}

// apply writes the planned items, each followed by its format's hooks,
// then, for a move, removes the sources.
func (s *Service) apply(ctx context.Context, t Transfer, steps []step) error {
	now := s.now()
	items := s.Store.Items()
	hooks, env := s.format(t.To.Repo.PackageType), s.env()
	for _, st := range steps {
		props := st.props
		if st.src.Folder {
			var err error
			props, err = items.Properties(ctx, st.src.ID)
			if err != nil && !errors.Is(err, store.ErrNotFound) {
				return fmt.Errorf("item: properties of %s: %w", st.src.FullPath(), err)
			}
		}
		landed, err := s.land(ctx, t, st, now)
		if err != nil {
			return err
		}
		if err := s.carry(ctx, t, st, landed, props, now); err != nil {
			return err
		}
		if err := s.landed(ctx, hooks, env, t.To.Repo, st, landed, props, now); err != nil {
			return err
		}
	}
	if t.Move {
		return s.removeSources(ctx, t, steps)
	}
	return nil
}

// carry gives landed, the item st wrote, the source's properties, and a
// move the source's statistics too. A file landing on another replaces it
// whole, as 6.20's mover deletes the target before it writes the source's
// info and properties (BaseRepoPathMover.overrideTargetFileIfExist), so
// none of the replaced file's properties or statistics are left. A folder
// landing on another keeps the properties the source does not set.
func (s *Service) carry(
	ctx context.Context,
	t Transfer,
	st step,
	landed types.Item,
	props types.Properties,
	now time.Time,
) error {
	items := s.Store.Items()
	replaced := st.replace.ID != 0
	switch {
	case replaced:
		if err := items.SetProperties(ctx, landed.ID, props, now); err != nil {
			return fmt.Errorf("item: properties of %s: %w", st.dst, err)
		}
	case len(props) > 0:
		if err := items.PatchProperties(ctx, landed.ID, props, nil, now); err != nil {
			return fmt.Errorf("item: properties of %s: %w", st.dst, err)
		}
	}
	var stats types.ItemStats
	if t.Move && st.src.ID != 0 {
		var err error
		if stats, err = items.Stats(ctx, st.src.ID); err != nil {
			return fmt.Errorf("item: statistics of %s: %w", st.src.FullPath(), err)
		}
	}
	if stats == (types.ItemStats{}) && !replaced {
		return nil
	}
	if err := items.PutStats(ctx, landed.ID, stats); err != nil {
		return fmt.Errorf("item: statistics of %s: %w", st.dst, err)
	}
	return nil
}

// landed runs the target format's hooks for an item a copy or move wrote,
// once its properties are in place: Deployed for a file, and Annotated for
// the properties it carried over.
func (s *Service) landed(
	ctx context.Context,
	hooks format.Handler,
	env format.Env,
	c repo.Config,
	st step,
	it types.Item,
	props types.Properties,
	now time.Time,
) error {
	if !it.Folder {
		env.Inspection = st.inspection
		if err := hooks.Deployed(ctx, env, c, it); err != nil {
			return err //nolint:wrapcheck // the format's own error
		}
	}
	return s.annotated(ctx, hooks, env, c, it, format.Annotation{Set: props, At: now})
}

// land writes one planned item at its target, without hooks. A file keeps
// its own history either way: Artifactory 7.x keeps a copied or moved
// item's creation and last-modified times unless a feature flag, off by
// default, says otherwise (7.125.4 release notes); who created and last
// modified it, and when it was last updated, follow 6.20, whose mover copies
// the whole ItemInfo (DbMutableItem.fillInfo). A folder a copy creates is
// new, created now by the caller; one a move creates keeps its history.
func (s *Service) land(
	ctx context.Context,
	t Transfer,
	st step,
	now time.Time,
) (types.Item, error) {
	items := s.Store.Items()
	if st.src.Folder {
		if err := s.ensureFolders(ctx, t.To, path.Dir(st.dst), t.By, now); err != nil {
			return types.Item{}, err
		}
		folder, err := items.Get(ctx, t.To.Repo.Key, false, st.dst)
		if err == nil {
			return folder, nil
		}
		if !errors.Is(err, store.ErrNotFound) {
			return types.Item{}, fmt.Errorf("item: get %s: %w", st.dst, err)
		}
		f := types.Item{
			RepoKey: t.To.Repo.Key, Name: st.dst, Folder: true,
			Created: now, CreatedBy: t.By, Modified: now, ModifiedBy: t.By, Updated: now,
		}
		if t.Move {
			f.Created, f.CreatedBy = st.src.Created, st.src.CreatedBy
			f.Modified, f.ModifiedBy = st.src.Modified, st.src.ModifiedBy
		}
		out, err := items.Put(ctx, f)
		if err != nil {
			return types.Item{}, fmt.Errorf("item: create folder %s: %w", st.dst, err)
		}
		return out, nil
	}
	if err := s.ensureFolders(ctx, t.To, path.Dir(st.dst), t.By, now); err != nil {
		return types.Item{}, err
	}
	it := types.Item{
		RepoKey: t.To.Repo.Key, Name: st.dst,
		Size: st.src.Size, Actual: st.src.Actual, Original: st.src.Original,
		MimeType: st.src.MimeType,
		Created:  st.src.Created, CreatedBy: st.src.CreatedBy,
		Modified: st.src.Modified, ModifiedBy: st.src.ModifiedBy,
		Updated: st.src.Updated,
	}
	out, err := items.Put(ctx, it)
	if err != nil {
		return types.Item{}, fmt.Errorf("item: put %s: %w", st.dst, err)
	}
	// The copy shares the content: one more reference, no new blob.
	if _, err := s.Store.Blobs().Ref(ctx, types.Blob{
		Checksums: st.src.Actual, Size: st.src.Size, Created: now,
	}, 1); err != nil {
		return types.Item{}, fmt.Errorf("item: reference blob: %w", err)
	}
	if st.replace.ID != 0 && st.replace.Actual.SHA256 != "" {
		if _, err := s.Store.Blobs().Unref(ctx, st.replace.Actual.SHA256, 1); err != nil {
			return types.Item{}, fmt.Errorf("item: release overwritten blob: %w", err)
		}
	}
	return out, nil
}

// removeSources deletes what a move transferred: each file (releasing the
// reference the target now holds a new one of), then each moved folder left
// empty, deepest first. Items that failed to move stay, with their folders.
func (s *Service) removeSources(ctx context.Context, t Transfer, steps []step) error {
	items := s.Store.Items()
	hooks, env := s.format(t.From.Repo.PackageType), s.env()
	var versions []types.PackageVersion
	var folders, files []types.Item
	for _, st := range steps {
		if st.src.Folder {
			folders = append(folders, st.src)
			continue
		}
		files = append(files, st.src)
		if err := hooks.Deleting(ctx, env, t.From.Repo, st.src); err != nil {
			return err //nolint:wrapcheck // the format's own error
		}
		pv, ok, err := pkgindex.VersionOf(ctx, s.Store, st.src.ID)
		if err != nil {
			return err //nolint:wrapcheck // pkgindex wraps its own
		}
		if ok {
			versions = append(versions, pv)
		}
		res, err := items.Delete(ctx, t.From.Repo.Key, false, st.src.FullPath())
		if err != nil {
			return fmt.Errorf("item: delete %s: %w", st.src.FullPath(), err)
		}
		for sha, n := range res.BlobRefs {
			if _, err := s.Store.Blobs().Unref(ctx, sha, n); err != nil {
				return fmt.Errorf("item: release blob %s: %w", sha, err)
			}
		}
	}
	for _, f := range slices.Backward(folders) {
		children, _, err := items.Children(ctx, t.From.Repo.Key, false, f.FullPath(),
			store.Page{Limit: 1})
		if err != nil {
			return fmt.Errorf("item: list %s: %w", f.FullPath(), err)
		}
		if len(children) > 0 {
			continue
		}
		if _, err := items.Delete(ctx, t.From.Repo.Key, false, f.FullPath()); err != nil {
			return fmt.Errorf("item: delete %s: %w", f.FullPath(), err)
		}
	}
	if err := pkgindex.Prune(ctx, s.Store, versions); err != nil {
		return err //nolint:wrapcheck // pkgindex wraps its own
	}
	if err := hooks.Deleted(ctx, env, t.From.Repo, files); err != nil {
		return err //nolint:wrapcheck // the format's own error
	}
	return nil
}

// moveFile is Env.Move: a move of the file at from to to within c, as
// Transfer makes it, that never lands over an item at to. Transfer would
// move a file into a folder in the way, so anything at to is refused here.
func (s *Service) moveFile(
	ctx context.Context,
	c repo.Config,
	from, to, by string,
) (string, error) {
	_, err := s.Store.Items().Get(ctx, c.Key, false, to)
	switch {
	case err == nil:
		return fmt.Sprintf("%s:%s is taken", c.Key, to), nil
	case !errors.Is(err, store.ErrNotFound):
		return "", fmt.Errorf("item: get %s: %w", to, err)
	}
	res, err := s.Transfer(ctx, Transfer{
		From: Target{Repo: c, Path: from}, To: Target{Repo: c, Path: to}, Move: true, By: by,
	})
	if err != nil {
		return "", err
	}
	for _, m := range res.Messages {
		if m.Level == LevelError {
			return m.Text, nil
		}
	}
	return "", nil
}

// join joins repository-relative paths, either of which may be empty.
func join(a, b string) string {
	switch {
	case a == "":
		return b
	case b == "":
		return a
	}
	return a + "/" + b
}
