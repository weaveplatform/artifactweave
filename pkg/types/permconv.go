package types

import (
	"errors"
	"fmt"
	"maps"
	"slices"
	"strings"

	"github.com/weaveplatform/artifactweave/pkg/types/wire"
)

// ErrLossyProjection reports a permission the requested legacy form cannot
// express. The legacy forms hold one include/exclude pattern pair per
// section, so a permission whose targets carry different patterns has no
// faithful legacy view; returning an error beats silently granting more or
// less than the stored permission.
var ErrLossyProjection = errors.New("permission cannot be expressed in this wire form")

// defaultInclude is what Artifactory applies when no include pattern is given.
var defaultInclude = []string{"**"}

// cleanPatterns drops empty patterns, which the legacy API emits as [""] to
// mean "none".
func cleanPatterns(p []string) []string {
	var out []string
	for _, s := range p {
		if s = strings.TrimSpace(s); s != "" {
			out = append(out, s)
		}
	}
	return out
}

func splitPatterns(s string) []string { return cleanPatterns(strings.Split(s, ",")) }

func grantsFrom(
	users, groups map[string][]string,
	parse func([]string) (Actions, error),
) (Grants, error) {
	var g Grants
	var err error
	if g.Users, err = actionMap(users, parse); err != nil {
		return Grants{}, err
	}
	if g.Groups, err = actionMap(groups, parse); err != nil {
		return Grants{}, err
	}
	return g, nil
}

// actionMap leaves the result nil for an empty input so a round trip does not
// turn an absent principal map into an empty one.
func actionMap(
	in map[string][]string,
	parse func([]string) (Actions, error),
) (map[string]Actions, error) {
	var out map[string]Actions
	for name, names := range in {
		if out == nil {
			out = make(map[string]Actions, len(in))
		}
		a, err := parse(names)
		if err != nil {
			return nil, fmt.Errorf("principal %q: %w", name, err)
		}
		out[name] = a
	}
	return out, nil
}

func namesMap(in map[string]Actions, render func(Actions) []string) map[string][]string {
	if len(in) == 0 {
		return nil
	}
	out := make(map[string][]string, len(in))
	for name, a := range in {
		out[name] = render(a)
	}
	return out
}

func targetsFor(repos, include, exclude []string) map[string]Target {
	include = cleanPatterns(include)
	if len(include) == 0 {
		include = slices.Clone(defaultInclude)
	}
	exclude = cleanPatterns(exclude)
	out := make(map[string]Target, len(repos))
	for _, r := range repos {
		out[r] = Target{Include: slices.Clone(include), Exclude: slices.Clone(exclude)}
	}
	return out
}

// sharedPatterns returns the one pattern pair every target of a resource
// uses, or ErrLossyProjection when they differ.
func sharedPatterns(r Resource) (repos, include, exclude []string, err error) {
	repos = slices.Sorted(maps.Keys(r.Targets))
	for i, name := range repos {
		t := r.Targets[name]
		if i == 0 {
			include, exclude = t.Include, t.Exclude
			continue
		}
		if !slices.Equal(t.Include, include) || !slices.Equal(t.Exclude, exclude) {
			return nil, nil, nil, fmt.Errorf("%w: targets %q and %q use different patterns",
				ErrLossyProjection, repos[0], name)
		}
	}
	if include == nil {
		include = slices.Clone(defaultInclude)
	}
	return repos, include, exclude, nil
}

// PermissionFromV1 reads a legacy v1 permission target. It covers
// repositories only, so the result has just the artifact resource.
func PermissionFromV1(w wire.PermissionTargetV1) (Permission, error) {
	g, err := grantsFrom(w.Principals.Users, w.Principals.Groups, ParseLetters)
	if err != nil {
		return Permission{}, err
	}
	return Permission{
		Name: w.Name,
		Resources: map[ResourceType]Resource{ResourceArtifact: {
			Actions: g,
			Targets: targetsFor(
				w.Repositories,
				splitPatterns(w.IncludesPattern),
				splitPatterns(w.ExcludesPattern),
			),
		}},
	}, nil
}

// PermissionToV1 renders the legacy v1 view. Sections other than artifact
// are left out, as Artifactory's v1 API does; patterns must be uniform.
func PermissionToV1(p Permission) (wire.PermissionTargetV1, error) {
	r := p.Resources[ResourceArtifact]
	repos, include, exclude, err := sharedPatterns(r)
	if err != nil {
		return wire.PermissionTargetV1{}, err
	}
	if repos == nil {
		repos = []string{}
	}
	return wire.PermissionTargetV1{
		Name:            p.Name,
		IncludesPattern: strings.Join(include, ","),
		ExcludesPattern: strings.Join(exclude, ","),
		Repositories:    repos,
		Principals: wire.PrincipalsV1{
			Users:  namesMap(r.Actions.Users, Actions.Letters),
			Groups: namesMap(r.Actions.Groups, Actions.Letters),
		},
	}, nil
}

var v2Sections = []struct {
	resource ResourceType
	get      func(*wire.PermissionTargetV2) **wire.SectionV2
}{
	{ResourceArtifact, func(w *wire.PermissionTargetV2) **wire.SectionV2 { return &w.Repo }},
	{ResourceBuild, func(w *wire.PermissionTargetV2) **wire.SectionV2 { return &w.Build }},
	{
		ResourceReleaseBundle,
		func(w *wire.PermissionTargetV2) **wire.SectionV2 { return &w.ReleaseBundle },
	},
}

// PermissionFromV2 reads a legacy v2 permission target.
func PermissionFromV2(w wire.PermissionTargetV2) (Permission, error) {
	p := Permission{Name: w.Name, Resources: map[ResourceType]Resource{}}
	for _, s := range v2Sections {
		sec := *s.get(&w)
		if sec == nil {
			continue
		}
		g, err := grantsFrom(sec.Actions.Users, sec.Actions.Groups, ParseLegacyActions)
		if err != nil {
			return Permission{}, fmt.Errorf("section %s: %w", s.resource, err)
		}
		p.Resources[s.resource] = Resource{
			Actions: g,
			Targets: targetsFor(sec.Repositories, sec.IncludePatterns, sec.ExcludePatterns),
		}
	}
	return p, nil
}

// PermissionToV2 renders the legacy v2 view of the artifact, build and
// release bundle sections; other resource types have no legacy section.
func PermissionToV2(p Permission) (wire.PermissionTargetV2, error) {
	w := wire.PermissionTargetV2{Name: p.Name}
	for _, s := range v2Sections {
		r, ok := p.Resources[s.resource]
		if !ok {
			continue
		}
		repos, include, exclude, err := sharedPatterns(r)
		if err != nil {
			return wire.PermissionTargetV2{}, fmt.Errorf("section %s: %w", s.resource, err)
		}
		if repos == nil {
			repos = []string{}
		}
		if exclude == nil {
			exclude = []string{}
		}
		*s.get(&w) = &wire.SectionV2{
			IncludePatterns: include,
			ExcludePatterns: exclude,
			Repositories:    repos,
			Actions: wire.ActionsV2{
				Users:  namesMap(r.Actions.Users, Actions.LegacyNames),
				Groups: namesMap(r.Actions.Groups, Actions.LegacyNames),
			},
		}
	}
	return w, nil
}

// PermissionFromAccess reads an Access v2 permission. It is the internal
// shape already, so every resource type round-trips.
func PermissionFromAccess(w wire.AccessPermission) (Permission, error) {
	p := Permission{
		Name:        w.Name,
		Description: w.Description,
		Resources:   map[ResourceType]Resource{},
	}
	for name, r := range w.Resources {
		g, err := grantsFrom(r.Actions.Users, r.Actions.Groups, ParseAccessActions)
		if err != nil {
			return Permission{}, fmt.Errorf("resource %s: %w", name, err)
		}
		targets := make(map[string]Target, len(r.Targets))
		for t, pat := range r.Targets {
			targets[t] = Target{
				Include: cleanPatterns(pat.IncludePatterns),
				Exclude: cleanPatterns(pat.ExcludePatterns),
			}
		}
		p.Resources[ResourceType(name)] = Resource{Actions: g, Targets: targets}
	}
	return p, nil
}

// PermissionToAccess renders the Access v2 form.
func PermissionToAccess(p Permission) wire.AccessPermission {
	w := wire.AccessPermission{
		Name:        p.Name,
		Description: p.Description,
		Resources:   make(map[string]wire.AccessResource, len(p.Resources)),
	}
	for t, r := range p.Resources {
		targets := make(map[string]wire.AccessTarget, len(r.Targets))
		for name, pat := range r.Targets {
			targets[name] = wire.AccessTarget{
				IncludePatterns: orEmpty(pat.Include),
				ExcludePatterns: orEmpty(pat.Exclude),
			}
		}
		w.Resources[string(t)] = wire.AccessResource{
			Actions: wire.AccessActions{
				Users:  namesMap(r.Actions.Users, Actions.AccessNames),
				Groups: namesMap(r.Actions.Groups, Actions.AccessNames),
			},
			Targets: targets,
		}
	}
	return w
}

func orEmpty(s []string) []string {
	if s == nil {
		return []string{}
	}
	return slices.Clone(s)
}
