package terraform

import (
	"context"
	"path"
	"strings"
)

const (
	moduleLinkBytes      = 64 << 20
	moduleLinkComponents = 4 << 20
	moduleLinkDepth      = 40
)

type moduleArchiveLink struct {
	target   string
	relative bool
}

type moduleLinkGraph struct {
	links     map[string]moduleArchiveLink
	bytes     int
	remaining int
}

func (g *moduleLinkGraph) add(name, target string, relative bool) error {
	if len(target) > 8192 || !moduleLink(name, target, relative) {
		return remoteModuleError()
	}
	g.bytes += len(name) + len(target)
	if g.bytes > moduleLinkBytes {
		return remoteModuleError()
	}
	if g.links == nil {
		g.links = map[string]moduleArchiveLink{}
	}
	g.links[name] = moduleArchiveLink{target: target, relative: relative}
	return nil
}

func (g *moduleLinkGraph) validate(ctx context.Context, names map[string]bool) error {
	// Entry parents cannot depend on extraction order. Target traversal is then
	// evaluated against the complete graph, before any lexical `..` cleanup.
	g.remaining = moduleLinkComponents
	for name := range names {
		if err := ctx.Err(); err != nil {
			return err //nolint:wrapcheck // preserve cancellation
		}
		for parent := path.Dir(name); parent != "." && parent != ""; parent = path.Dir(parent) {
			g.remaining--
			if g.remaining < 0 {
				return remoteModuleError()
			}
			if _, exists := g.links[parent]; exists {
				return remoteModuleError()
			}
		}
	}
	for name := range g.links {
		if _, err := g.resolve(ctx, name, map[string]bool{}, 0, true); err != nil {
			return err
		}
	}
	return nil
}

func (g *moduleLinkGraph) resolve(
	ctx context.Context,
	name string,
	active map[string]bool,
	depth int,
	symlinks bool,
) ([]string, error) {
	if err := ctx.Err(); err != nil {
		return nil, err //nolint:wrapcheck // preserve cancellation
	}
	link := g.links[name]
	if depth >= moduleLinkDepth || active[name] || link.relative && !symlinks {
		return nil, remoteModuleError()
	}
	active[name] = true
	defer delete(active, name)
	var resolved []string
	if link.relative {
		if parent := path.Dir(name); parent != "." {
			resolved = strings.Split(parent, "/")
		}
	}
	for _, part := range strings.Split(link.target, "/") {
		g.remaining--
		if g.remaining < 0 {
			return nil, remoteModuleError()
		}
		if err := ctx.Err(); err != nil {
			return nil, err //nolint:wrapcheck // preserve cancellation
		}
		switch part {
		case "", ".":
			continue
		case "..":
			if len(resolved) == 0 {
				return nil, remoteModuleError()
			}
			resolved = resolved[:len(resolved)-1]
		default:
			resolved = append(resolved, part)
			candidate := strings.Join(resolved, "/")
			if _, exists := g.links[candidate]; exists {
				var err error
				// A hardlink to a symlink can rebase that symlink's relative target at
				// the new name. Reject that extractor-dependent case instead of treating
				// it as an ordinary alias to the symlink's current destination.
				resolved, err = g.resolve(
					ctx,
					candidate,
					active,
					depth+1,
					symlinks && link.relative,
				)
				if err != nil {
					return nil, err
				}
			}
		}
	}
	return resolved, nil
}
