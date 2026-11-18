package itemkit

import (
	"context"
	"errors"
	"fmt"

	"github.com/weaveplatform/artifactweave/internal/artifacts/api/repo"
	"github.com/weaveplatform/artifactweave/internal/artifacts/core/format"
	"github.com/weaveplatform/artifactweave/internal/artifacts/core/item"
	"github.com/weaveplatform/artifactweave/internal/artifacts/core/layout"
	"github.com/weaveplatform/artifactweave/internal/artifacts/core/virtual"
)

// ReadTranslator projects item paths using the request's shared layout resolver.
// Only an unavailable layout passes through; configuration-storage faults must
// still fail the request.
func ReadTranslator(layouts format.Layouts, from repo.Config) func(context.Context, repo.Config, string) (string, error) {
	load := func(ctx context.Context, name string) (*layout.Layout, error) {
		l, err := layouts.Layout(ctx, name)
		if errors.Is(err, layout.ErrPattern) {
			l, err = nil, nil
		}
		return l, err
	}
	return func(ctx context.Context, to repo.Config, path string) (string, error) {
		if from.RepoLayoutRef == to.RepoLayoutRef || from.RepoLayoutRef == "" || to.RepoLayoutRef == "" {
			return path, nil
		}
		source, err := load(ctx, from.RepoLayoutRef)
		if err != nil {
			return "", fmt.Errorf("source layout: %w", err)
		}
		target, err := load(ctx, to.RepoLayoutRef)
		if err != nil {
			return "", fmt.Errorf("target layout: %w", err)
		}
		return layout.TranslateRead(source, target, path), nil
	}
}

func (d *Download) nestedPath(ctx context.Context, c repo.Config, path string) (string, error) {
	translated, err := d.translate(ctx, c, path)
	if err == nil && translated != path {
		d.stepf("Resource was translated to '%s' in order to search within nested virtual '%s'", translated, c.Key)
	}
	return translated, err
}

// lookupMember translates from the original virtual for each real member, never
// from a preceding nested virtual. 7.x retries every unfound translated result;
// each spelling is independently authorized. Infrastructure faults stay terminal.
func lookupMember(ctx context.Context, st Store, step virtual.Member, path string, acc Access, dl *Download, res *virtual.Resolution) (Found, memberAnswer, error) {
	attempt := *dl
	attempt.deferForbidden = true
	memberResult := virtual.Resolution{}
	translated := path
	if dl.translate != nil {
		var err error
		translated, err = dl.translate(ctx, step.Repo, path)
		if err != nil {
			return Found{}, passedOver, fmt.Errorf("member %s path: %w", step.Repo.Key, err)
		}
		attempt.path, err = dl.translate(ctx, step.Repo, dl.path)
		if err != nil {
			return Found{}, passedOver, fmt.Errorf("member %s resource: %w", step.Repo.Key, err)
		}
	}
	if translated != path {
		attempt.layoutTranslated = true
		dl.stepf("Request path was translated to %s due to repository layout differences", translated)
	}
	found, served, err := lookupMemberPath(ctx, st, step, translated, acc, &attempt, &memberResult)
	if err == nil && served == passedOver && translated != path && !dl.noOriginalFallback {
		dl.stepf("Unable to find the resource in the translated path - retrying with the original")
		attempt.path = dl.path
		attempt.layoutTranslated = false
		memberResult = virtual.Resolution{}
		found, served, err = lookupMemberPath(ctx, st, step, path, acc, &attempt, &memberResult)
	}
	if memberResult.Forbidden != nil && err == nil && served == passedOver {
		if res.Deny(*memberResult.Forbidden) {
			dl.stepf("Request is forbidden by %s", step.Repo.Key)
		}
	}
	if served != passedOver {
		res.Found = true
	}
	found.asked = resourcePath(attempt.path)
	return found, served, err
}

func foundRequest(req ItemRequest, found Found) ItemRequest {
	req = memberRequest(req, found.Repo)
	if found.asked == "" || found.asked == resourcePath(req.Path) {
		return req
	}
	_, checksum, sidecar := item.Sidecar(req.Path)
	req.Path = found.asked
	if sidecar {
		req.Path += "." + string(checksum)
	}
	return req
}
