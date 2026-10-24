package cargo

import (
	"context"
	"encoding/json"
	"fmt"
	"net/http"
	"strings"

	"github.com/weaveplatform/artifactweave/internal/format"
	"github.com/weaveplatform/artifactweave/internal/format/generic"
	"github.com/weaveplatform/artifactweave/pkg/repo"
	"github.com/weaveplatform/artifactweave/pkg/store"
	"github.com/weaveplatform/artifactweave/pkg/types"
)

func Serves(t repo.PackageType) bool { return t == repo.Cargo }

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
		return inspection{}, format.Reject(http.StatusBadRequest, "Invalid Cargo crate: %v", err)
	}
	return inspection{sha, path, n, v}, nil
}

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

func (Handler) Admit(
	ctx context.Context,
	env format.Env,
	c repo.Config,
	w format.Write,
) (format.Write, error) {
	w, err := generic.Handler{}.Admit(ctx, env, c, w)
	if err != nil {
		return w, err
	}
	if w.Op == format.OpChecksum {
		return w, nil
	}
	if _, _, ok := ParseCratePath(w.Path); !ok {
		return w, format.Reject(
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

func (Handler) Deployed(ctx context.Context, env format.Env, _ repo.Config, it types.Item) error {
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
		); err != nil || !r.Valid() || r.Name != n || r.Version != v ||
			r.Checksum != it.Actual.SHA256 {
			return format.Reject(http.StatusBadRequest, "invalid Cargo index metadata")
		}
	}
	if len(props[YankedProperty]) == 1 {
		r.Yanked = props[YankedProperty][0] == "true"
	}
	line, err := Encode(r)
	if err != nil {
		return format.Reject(http.StatusBadRequest, "invalid Cargo index metadata")
	}
	return env.Store.Items().
		PatchProperties(ctx, it.ID, types.Properties{RecordProperty: {line}}, nil, it.Updated)
	//nolint:wrapcheck
}

func (Handler) Reindex(ctx context.Context, env format.Env, c repo.Config) error {
	if c.Class != repo.Local && c.Class != repo.Federated {
		return format.Reject(http.StatusBadRequest, "Cargo reindex requires a hosted repository")
	}
	after := ""
	for {
		items, next, err := env.Store.Items().
			Descendants(ctx, c.Key, false, "", store.Page{After: after, Limit: 100})
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
