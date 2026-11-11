package cargo

import (
	"context"
	"encoding/json"
	"fmt"
	"net/http"
	"strings"

	"github.com/weaveplatform/artifactweave/internal/artifacts/api/repo"
	"github.com/weaveplatform/artifactweave/internal/artifacts/core/format"
	"github.com/weaveplatform/artifactweave/internal/artifacts/core/model"
	"github.com/weaveplatform/artifactweave/internal/artifacts/ecosystem/generic"
	"github.com/weaveplatform/artifactweave/internal/kernel"
)

// Serves reports whether t is the Cargo package type.
func Serves(t repo.PackageType) bool { return t == repo.Cargo }

// Handler is the Cargo format's hooks: it admits a crate archive only at
// its canonical path and only when its Cargo.toml names the crate and
// version that path does, and keeps each stored archive's index record. It
// embeds the generic hooks, so every path is classed as generic content is.
type Handler struct{ generic.Handler }

type inspection struct{ sha, path, name, version string }

func inspect(ctx context.Context, env format.Env, sha, path string) (inspection, error) {
	rc, info, err := env.Blobs.Open(ctx, sha)
	if err != nil {
		return inspection{}, fmt.Errorf("cargo: open crate: %w", err)
	}
	defer rc.Close()
	n, v, err := Inspect(ctx, rc, info.Size, path)
	if err != nil {
		return inspection{}, format.Rejectf(http.StatusBadRequest, "Invalid Cargo crate: %v", err)
	}
	return inspection{sha, path, n, v}, nil
}

// Inspect reads and validates the stored archive of a write to a .crate
// path before the write's transaction, and returns what it found for Admit,
// so an archive is read once per write. A checksum deploy and any other
// file inspect nothing. An invalid crate is refused with 400; an archive
// that cannot be opened fails the write.
func (Handler) Inspect(
	ctx context.Context,
	env format.Env,
	_ repo.Config,
	w format.Write,
) (any, error) {
	if w.Op == format.OpChecksum || !strings.HasSuffix(w.Path, ".crate") {
		return inspection{}, nil
	}
	return inspect(ctx, env, w.Checksums.SHA256, w.Path)
}

// Admit applies the generic deploy policy, wrapping its refusal, then
// passes a checksum deploy. Any other write must be at a crate path,
// crates/<name>/<name>-<version>.crate (400), and its archive must be a
// valid crate whose Cargo.toml names that crate and version (400). It
// reuses Inspect's reading when it covers the same content and path, and
// reads the stored archive itself otherwise.
func (Handler) Admit(
	ctx context.Context,
	env format.Env,
	c repo.Config,
	w format.Write,
) (format.Write, error) {
	w, err := generic.Handler{}.Admit(ctx, env, c, w)
	if err != nil {
		return w, fmt.Errorf("cargo: admission policy: %w", err)
	}
	if w.Op == format.OpChecksum {
		return w, nil
	}
	if _, _, ok := ParseCratePath(w.Path); !ok {
		return w, format.Rejectf(
			http.StatusBadRequest,
			"Cargo crates require crates/<name>/<name>-<version>.crate",
		)
	}
	if in, ok := env.Inspection.(inspection); !ok || in.sha != w.Checksums.SHA256 ||
		in.path != w.Path {
		if _, err := inspect(ctx, env, w.Checksums.SHA256, w.Path); err != nil {
			return w, err
		}
	}
	return w, nil
}

// Deployed keeps a stored crate's index record: it checks the archive at a
// crate path again and writes RecordProperty as the record Encode makes,
// taken from the record already there, as a native publish sets it, or
// else from the path and checksum with no dependencies or features, with
// the yank flag from YankedProperty. A record that is invalid or names
// another crate, version or checksum is refused with 400, as is an archive
// that no longer validates. Folders and files at other paths change
// nothing.
func (Handler) Deployed(ctx context.Context, env format.Env, _ repo.Config, it model.Item) error {
	if it.Folder {
		return nil
	}
	n, v, ok := ParseCratePath(it.FullPath())
	if !ok {
		return nil
	}
	if _, err := inspect(ctx, env, it.Actual.SHA256, it.FullPath()); err != nil {
		return err
	}
	props, err := env.Store.Items().Properties(ctx, it.ID)
	if err != nil {
		return fmt.Errorf("cargo: properties: %w", err)
	}
	r := Record{
		Name:     n,
		Version:  v,
		Checksum: it.Actual.SHA256,
		Deps:     []Dependency{},
		Features: map[string][]string{},
	}
	if len(props[RecordProperty]) == 1 {
		if err := json.Unmarshal(
			[]byte(props[RecordProperty][0]),
			&r,
		); err != nil || !r.IsValid() || r.Name != n || r.Version != v ||
			r.Checksum != it.Actual.SHA256 {
			return format.Rejectf(http.StatusBadRequest, "invalid Cargo index metadata")
		}
	}
	if len(props[YankedProperty]) == 1 {
		r.Yanked = props[YankedProperty][0] == "true"
	}
	line, err := Encode(r)
	if err != nil {
		return format.Rejectf(http.StatusBadRequest, "invalid Cargo index metadata")
	}
	if err := env.Store.Items().PatchProperties(
		ctx, it.ID, model.Properties{RecordProperty: {line}}, nil, it.Updated,
	); err != nil {
		return fmt.Errorf("cargo: update index metadata: %w", err)
	}
	return nil
}

// Reindex runs Deployed over every stored .crate file of a local or
// federated repository and stops at the first that fails; any other class
// is refused with 400.
func (Handler) Reindex(ctx context.Context, env format.Env, c repo.Config) error {
	if c.Class != repo.Local && c.Class != repo.Federated {
		return format.Rejectf(http.StatusBadRequest, "Cargo reindex requires a hosted repository")
	}
	after := ""
	for {
		items, next, err := env.Store.Items().
			Descendants(ctx, c.Key, false, "", kernel.Page{After: after, Limit: 100})
		if err != nil {
			return fmt.Errorf("cargo: list crates: %w", err)
		}
		for _, it := range items {
			if it.Folder || !strings.HasSuffix(it.FullPath(), ".crate") {
				continue
			}
			if err := (Handler{}).Deployed(ctx, env, c, it); err != nil {
				return err
			}
		}
		if next == "" {
			return nil
		}
		after = next
	}
}

var (
	_ format.Handler   = Handler{}
	_ format.Inspector = Handler{}
	_ format.Admitter  = Handler{}
	_ format.Reindexer = Handler{}
)
