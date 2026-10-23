package luarocks

import (
	"bytes"
	"context"
	"errors"
	"fmt"
	"io"
	"regexp"
	"slices"
	"strings"

	"github.com/weaveplatform/artifactweave/internal/format"
	"github.com/weaveplatform/artifactweave/pkg/repo"
	"github.com/weaveplatform/artifactweave/pkg/store"
	"github.com/weaveplatform/artifactweave/pkg/types"
)

var luaVersions = []string{"5.1", "5.2", "5.3", "5.4", "5.5"}

const (
	maxManifestEntries = 100000
	maxManifestBytes   = 32 << 20
)

var errManifestLimit = errors.New("luarocks: manifest limit exceeded")

type manifestEntry struct{ name, version, arch string }

// Work builds one plain Lua manifest and the five version-specific variants
// the current LuaRocks client requests. The tree is read afresh on each run,
// so queued bursts and restores settle to the final committed state.
func (Handler) Work(ctx context.Context, env format.Env, work types.WorkItem) error {
	if work.Kind != KindManifest {
		return nil
	}
	c, err := env.Store.Repos().Get(ctx, work.Key)
	switch {
	case errors.Is(err, store.ErrNotFound):
		return nil
	case err != nil:
		return fmt.Errorf("luarocks: repository: %w", err)
	case !Serves(c.PackageType) || !derives(c, false):
		return nil
	}
	its, err := files(ctx, env.Store, c)
	if err != nil {
		return err
	}
	entries := make([]manifestEntry, 0)
	for _, it := range its {
		id, ok := Parse(it.FullPath())
		if !ok {
			continue
		}
		entries = append(entries, manifestEntry{id.Name, id.Version, id.Arch})
		if len(entries) > maxManifestEntries {
			return fmt.Errorf("%w: more than %d entries", errManifestLimit, maxManifestEntries)
		}
	}
	slices.SortFunc(entries, func(a, b manifestEntry) int {
		if v := strings.Compare(a.name, b.name); v != 0 {
			return v
		}
		if v := strings.Compare(a.version, b.version); v != 0 {
			return v
		}
		return strings.Compare(a.arch, b.arch)
	})
	for _, version := range append([]string{""}, luaVersions...) {
		filtered := entries
		if version != "" {
			filtered, err = filterVersion(ctx, env, c, entries, version)
			if err != nil {
				return err
			}
		}
		data, err := encodeManifest(filtered)
		if err != nil {
			return err
		}
		path := ManifestPath
		if version != "" {
			path += "-" + version
		}
		if _, err := env.Derive(
			ctx,
			format.Derived{Repo: c, Path: path, Body: bytes.NewReader(data), By: "_system_"},
		); err != nil {
			return fmt.Errorf("luarocks: derive %s: %w", path, err)
		}
	}
	return nil
}

// encodeManifest writes Lua assignments in the same structural form as
// luarocks-admin: repository[name][version] is a sequence of arch records.
// Names and versions are quoted, never evaluated as source from a rockspec.
func encodeManifest(entries []manifestEntry) ([]byte, error) {
	var out bytes.Buffer
	out.WriteString("commands = {}\nmodules = {}\nrepository = {\n")
	lastName, lastVersion := "", ""
	for _, e := range entries {
		if e.name != lastName {
			if lastName != "" {
				out.WriteString("      },\n   },\n")
			}
			fmt.Fprintf(&out, "   [%q] = {\n", e.name)
			lastName, lastVersion = e.name, ""
		}
		if e.version != lastVersion {
			if lastVersion != "" {
				out.WriteString("      },\n")
			}
			fmt.Fprintf(&out, "      [%q] = {\n", e.version)
			lastVersion = e.version
		}
		fmt.Fprintf(&out, "         { arch = %q },\n", e.arch)
		if out.Len() > maxManifestBytes {
			return nil, fmt.Errorf("%w: more than %d bytes", errManifestLimit, maxManifestBytes)
		}
	}
	if lastVersion != "" {
		out.WriteString("      },\n   },\n")
	}
	out.WriteString("}\n")
	return out.Bytes(), nil
}

var luaRequirement = regexp.MustCompile(`\blua\s*(>=|<=|==|~=|>|<)\s*5\.([1-5])\b`)

// filterVersion applies simple Lua dependency constraints from the matching
// rockspec, as the upstream manifest writer does. Unrecognised expressions
// remain in the manifest so the native installer can make its final choice.
func filterVersion(
	ctx context.Context,
	env format.Env,
	c repo.Config,
	entries []manifestEntry,
	luaVersion string,
) ([]manifestEntry, error) {
	want := int(luaVersion[len(luaVersion)-1] - '0')
	allowed := map[string]bool{}
	out := make([]manifestEntry, 0, len(entries))
	for _, e := range entries {
		key := e.name + "\x00" + e.version
		ok, seen := allowed[key]
		if !seen {
			ok = true
			filename := e.name + "-" + e.version + ".rockspec"
			it, err := env.Store.Items().Get(ctx, c.Key, false, filename)
			switch {
			case errors.Is(err, store.ErrNotFound):
			case err != nil:
				return nil, fmt.Errorf("luarocks: rockspec %s: %w", filename, err)
			default:
				rc, info, err := env.Blobs.Open(ctx, it.Actual.SHA256)
				if err != nil {
					return nil, fmt.Errorf("luarocks: open %s: %w", filename, err)
				}
				if info.Size <= maxRockspec {
					body, readErr := io.ReadAll(io.LimitReader(rc, maxRockspec+1))
					if readErr != nil {
						_ = rc.Close()
						return nil, fmt.Errorf("luarocks: read %s: %w", filename, readErr)
					}
					for _, m := range luaRequirement.FindAllSubmatch(body, -1) {
						minor := int(m[2][0] - '0')
						switch string(m[1]) {
						case ">=":
							ok = ok && want >= minor
						case ">":
							ok = ok && want > minor
						case "<=":
							ok = ok && want <= minor
						case "<":
							ok = ok && want < minor
						case "==":
							ok = ok && want == minor
						case "~=":
							ok = ok && want != minor
						}
					}
				}
				_ = rc.Close()
			}
			allowed[key] = ok
		}
		if ok {
			out = append(out, e)
		}
	}
	return out, nil
}
