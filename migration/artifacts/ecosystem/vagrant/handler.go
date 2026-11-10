package vagrant

import (
	"context"
	"fmt"
	"net/http"
	"slices"
	"strings"

	"github.com/weaveplatform/artifactweave/migration/artifacts/api/repo"
	"github.com/weaveplatform/artifactweave/migration/artifacts/core/format"
	"github.com/weaveplatform/artifactweave/migration/artifacts/core/model"
	"github.com/weaveplatform/artifactweave/migration/artifacts/core/pkgindex"
	"github.com/weaveplatform/artifactweave/migration/artifacts/ecosystem/generic"
	"github.com/weaveplatform/artifactweave/migration/kernel"
)

// Serves reports whether t is the Vagrant package type.
func Serves(t repo.PackageType) bool { return t == repo.Vagrant }

type (
	// Handler is the Vagrant format's hooks: it admits a box only with a
	// valid identity whose provider its metadata.json confirms, records the
	// box's metadata as derived properties, and indexes each box as a
	// version of its box_name from those properties alone. It embeds the
	// generic hooks, so every path is classed as generic content is.
	Handler    struct{ generic.Handler }
	inspection struct {
		sha string
		box Box
	}
)

// Inspect reads and validates the stored box of a write to a .box path
// before the write's transaction, and returns its metadata for Admit and
// Deployed, so a box is read once per write. A checksum deploy and any
// other path inspect nothing. An invalid box is refused with 400; one that
// cannot be opened fails the write.
func (Handler) Inspect(
	ctx context.Context,
	env format.Env,
	_ repo.Config,
	w format.Write,
) (any, error) {
	if w.Op == format.OpChecksum || !strings.HasSuffix(w.Path, ".box") {
		return inspection{}, nil
	}
	b, err := readStored(ctx, env, w.Checksums.SHA256)
	return inspection{sha: w.Checksums.SHA256, box: b}, err
}

// Admit applies the generic deploy policy, wrapping its refusal, then
// passes a checksum deploy. Any other write must be to a local or
// federated repository (400), at a .box path (400), with exactly one valid
// box_name, box_provider and box_version property (400), and of a valid
// box (400) whose metadata.json names the provider box_provider does
// (400). It reuses Inspect's reading when it covers the same content, and
// reads the stored box itself otherwise.
func (Handler) Admit(
	ctx context.Context,
	env format.Env,
	c repo.Config,
	w format.Write,
) (format.Write, error) {
	w, err := (generic.Handler{}).Admit(ctx, env, c, w)
	if err != nil {
		return w, fmt.Errorf("vagrant admission: %w", err)
	}
	if w.Op == format.OpChecksum {
		return w, nil
	}
	if c.Class != repo.Local && c.Class != repo.Federated {
		return w, format.Rejectf(
			http.StatusBadRequest,
			"Vagrant supports local and federated repositories",
		)
	}
	if !strings.HasSuffix(w.Path, ".box") {
		return w, format.Rejectf(http.StatusBadRequest, "Vagrant artifacts must be .box archives")
	}
	id, ok := Properties(w.Properties)
	if !ok {
		return w, format.Rejectf(
			http.StatusBadRequest,
			"Vagrant boxes require one valid box_name, box_provider and box_version property",
		)
	}
	b, err := boxFor(ctx, env, w.Checksums.SHA256)
	if err != nil {
		return w, err
	}
	if id.Provider != b.Provider {
		return w, format.Rejectf(http.StatusBadRequest, "box_provider differs from metadata.json")
	}
	return w, nil
}

// Deployed records a stored box's provider, architecture and SHA-256 as
// its derived vagrant.* properties, then rebuilds the package index from
// every box's properties. Folders, a remote's cached items and files at
// other paths change nothing; a box that no longer reads as valid fails
// with 400.
func (h Handler) Deployed(ctx context.Context, env format.Env, c repo.Config, it model.Item) error {
	if it.Folder || it.Cached || !strings.HasSuffix(it.FullPath(), ".box") {
		return nil
	}
	b, err := boxFor(ctx, env, it.Actual.SHA256)
	if err != nil {
		return err
	}
	if err := saveProof(ctx, env, it, b); err != nil {
		return err
	}
	return rebuild(ctx, env, c)
}

// Deleting does nothing: Deleted rebuilds the index once the box is gone.
func (Handler) Deleting(context.Context, format.Env, repo.Config, model.Item) error { return nil }

// Deleted rebuilds the package index from the properties of the boxes
// left.
func (Handler) Deleted(ctx context.Context, env format.Env, c repo.Config, _ []model.Item) error {
	return rebuild(ctx, env, c)
}

// AdmitAnnotation refuses (400) a property change that sets or removes a
// derived property (vagrant.provider, vagrant.architecture or
// vagrant.sha256), which only the hooks write; any other change passes.
func (Handler) AdmitAnnotation(
	_ context.Context,
	_ format.Env,
	_ repo.Config,
	_ model.Item,
	a format.Annotation,
) error {
	for _, key := range []string{propProvider, propArchitecture, propDigest} {
		if _, ok := a.Set[key]; ok || slices.Contains(a.Removed, key) {
			return format.Rejectf(http.StatusBadRequest, "Vagrant archive properties are derived")
		}
	}
	return nil
}

// Annotated rebuilds the package index when a change to a stored box sets
// or removes box_name, box_provider or box_version, which place the box in
// the index. Folders, a remote's cached items and other changes leave the
// index as it is.
func (Handler) Annotated(
	ctx context.Context,
	env format.Env,
	c repo.Config,
	it model.Item,
	a format.Annotation,
) error {
	if it.Folder || it.Cached {
		return nil
	}
	for _, key := range []string{PropName, PropProvider, PropVersion} {
		if _, ok := a.Set[key]; ok || slices.Contains(a.Removed, key) {
			return rebuild(ctx, env, c)
		}
	}
	return nil
}

// Reindex reads every stored box at a .box path again and records its
// derived properties, then rebuilds the package index from every box's
// properties. A box that does not read as valid fails the reindex with
// 400.
func (Handler) Reindex(ctx context.Context, env format.Env, c repo.Config) error {
	if err := eachItem(ctx, env.Store, c.Key, func(it model.Item) error {
		b, err := readStored(ctx, env, it.Actual.SHA256)
		if err != nil {
			return err
		}
		return saveProof(ctx, env, it, b)
	}); err != nil {
		return err
	}
	return rebuild(ctx, env, c)
}

func boxFor(ctx context.Context, env format.Env, sha string) (Box, error) {
	if in, ok := env.Inspection.(inspection); ok && in.sha == sha {
		return in.box, nil
	}
	return readStored(ctx, env, sha)
}

func readStored(ctx context.Context, env format.Env, sha string) (Box, error) {
	rc, info, err := env.Blobs.Open(ctx, sha)
	if err != nil {
		return Box{}, fmt.Errorf("vagrant: open box: %w", err)
	}
	defer rc.Close()
	b, err := ReadBox(ctx, rc, info.Size)
	if err != nil {
		return Box{}, format.Rejectf(http.StatusBadRequest, "Invalid Vagrant archive: %v", err)
	}
	return b, nil
}

func saveProof(ctx context.Context, env format.Env, it model.Item, b Box) error {
	props := model.Properties{
		propProvider:     {b.Provider},
		propDigest:       {it.Actual.SHA256},
		propArchitecture: {b.Architecture},
	}
	if err := env.Store.Items().PatchProperties(ctx, it.ID, props, nil, it.Updated); err != nil {
		return fmt.Errorf("vagrant: archive properties: %w", err)
	}
	return nil
}

func eachItem(
	ctx context.Context,
	st format.Store,
	key string,
	visit func(model.Item) error,
) error {
	page := kernel.Page{Limit: kernel.MaxLimit}
	for {
		items, next, err := st.Items().Descendants(ctx, key, false, "", page)
		if err != nil {
			return fmt.Errorf("vagrant: list boxes: %w", err)
		}
		for _, it := range items {
			if it.Folder || !strings.HasSuffix(it.FullPath(), ".box") {
				continue
			}
			if err := visit(it); err != nil {
				return err
			}
		}
		if next == "" {
			return nil
		}
		page.After = next
	}
}

// Rebuilding uses only bounded properties recorded after archive validation;
// ordinary metadata and property updates never decompress existing VM disks.
func rebuild(ctx context.Context, env format.Env, c repo.Config) error {
	byName := map[string]*pkgindex.Indexed{}
	if err := eachItem(ctx, env.Store, c.Key, func(it model.Item) error {
		props, err := env.Store.Items().Properties(ctx, it.ID)
		if err != nil {
			return fmt.Errorf("vagrant: properties: %w", err)
		}
		id, _, ok := Verified(props, it.Actual.SHA256)
		if !ok {
			return nil
		}
		p := byName[id.Name]
		if p == nil {
			p = &pkgindex.Indexed{Name: id.Name, Created: it.Created}
			byName[id.Name] = p
		}
		if it.Created.Before(p.Created) {
			p.Created = it.Created
		}
		n := slices.IndexFunc(
			p.Versions,
			func(v pkgindex.IndexedVersion) bool { return v.Version == id.Version },
		)
		if n < 0 {
			n = len(p.Versions)
			p.Versions = append(
				p.Versions,
				pkgindex.IndexedVersion{Version: id.Version, Created: it.Created},
			)
		}
		v := &p.Versions[n]
		v.Files = append(v.Files, it.ID)
		if it.Created.Before(v.Created) {
			v.Created = it.Created
		}
		return nil
	}); err != nil {
		return err
	}
	want := make([]pkgindex.Indexed, 0, len(byName))
	for _, p := range byName {
		want = append(want, *p)
	}
	if err := pkgindex.Reconcile(ctx, env.Store, c, want); err != nil {
		return fmt.Errorf("vagrant: reconcile packages: %w", err)
	}
	return nil
}

var (
	_ format.Handler            = Handler{}
	_ format.Inspector          = Handler{}
	_ format.Admitter           = Handler{}
	_ format.Annotator          = Handler{}
	_ format.AnnotationAdmitter = Handler{}
	_ format.Reindexer          = Handler{}
)
