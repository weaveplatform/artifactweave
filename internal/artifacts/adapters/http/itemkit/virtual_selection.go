package itemkit

import (
	"context"
	"fmt"

	"github.com/weaveplatform/artifactweave/internal/artifacts/api/repo"
	"github.com/weaveplatform/artifactweave/internal/artifacts/core/format"
	"github.com/weaveplatform/artifactweave/internal/artifacts/core/model"
	"github.com/weaveplatform/artifactweave/internal/artifacts/core/virtual"
)

// lookupSelected retains ownership of just the best candidate. Losing transient
// bodies are closed immediately; no candidate is counted until delivery.
func lookupSelected(ctx context.Context, st Store, members []virtual.Member, path string, acc Access, dl *Download, selection *format.VirtualSelection) (out Found, err error) {
	var winner *Found
	var candidate *format.VirtualCandidate
	defer func() {
		if err != nil && winner != nil {
			closeCandidate(*winner)
		}
	}()
	res := virtual.Resolution{Missing: selection.Missing}
	for _, m := range members {
		if candidate != nil && candidate.Repo.PriorityResolution.Bool() && !m.Repo.PriorityResolution.Bool() {
			break
		}
		if m.Upstream && (dl.upstream == nil || dl.isFolder) {
			continue
		}
		if selection.DirectRemotes && m.Repo.Class == repo.Remote && !m.Upstream {
			continue
		}
		key := m.Repo.Key
		if m.Repo.Class == repo.Remote && !m.Upstream {
			key += model.CacheSuffix
		}
		dl.stepf("Searching for the resource within %s", key)
		if steps := selection.Skip(m.Repo); len(steps) > 0 {
			for _, step := range steps {
				dl.stepf("%s", step)
			}
			continue
		}
		found, served, err := lookupMember(ctx, st, m, path, acc, dl, &res)
		if err != nil {
			return Found{}, err
		}
		if served == passedOver {
			continue
		}
		if found.remote != nil && found.remote.err != nil {
			closeCandidate(found)
			return Found{}, found.remote.err
		}
		current := selectedCandidate(found, dl.path, served)
		choice, err := selection.Choose(ctx, current, candidate)
		if err != nil {
			closeCandidate(found)
			return Found{}, fmt.Errorf("compare virtual candidate: %w", err)
		}
		for _, step := range choice.Steps {
			dl.stepf("%s", step)
		}
		if choice.Take {
			if winner != nil {
				closeCandidate(*winner)
			}
			winner, candidate = &found, &current
		} else {
			closeCandidate(found)
		}
		if choice.Stop {
			break
		}
	}
	if winner != nil {
		dl.stepf("%s", selection.Returning(*candidate))
		return *winner, nil
	}
	res.Found = false
	return Found{}, &memberMiss{res: res}
}

func selectedCandidate(f Found, requested string, served memberAnswer) format.VirtualCandidate {
	if f.asked != "" {
		requested = f.asked
	}
	c := format.VirtualCandidate{Repo: f.Repo, Path: f.Item.FullPath(), Modified: f.Item.Modified}
	if f.remote != nil {
		c.Path = f.remote.resource.Path
		if f.remote.result.Item != nil {
			c.Modified = f.remote.result.Item.Modified
		}
		if f.remote.result.Stream != nil {
			c.Modified = f.remote.result.Stream.Modified
		}
	}
	c.Exact = c.Path == requested && served == exactMatch
	return c
}

func closeCandidate(f Found) {
	if f.remote != nil && f.remote.result.Stream != nil {
		_ = f.remote.result.Stream.Body.Close()
	}
}
