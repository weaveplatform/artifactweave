// Package generic is the format of generic repositories: files without
// package metadata, so nothing is indexed. Every other format embeds its
// Handler for the deploy policy that applies to every package type
// (decision 0032).
package generic

import (
	"context"
	"net/http"

	"github.com/weaveplatform/artifactweave/internal/authz"
	"github.com/weaveplatform/artifactweave/internal/format"
	"github.com/weaveplatform/artifactweave/pkg/repo"
	"github.com/weaveplatform/artifactweave/pkg/types"
)

// Handler implements format.Handler for generic content.
type Handler struct{}

// Deployed does nothing: a generic file belongs to no package.
func (Handler) Deployed(context.Context, format.Env, repo.Config, types.Item) error { return nil }

// Deleting does nothing.
func (Handler) Deleting(context.Context, format.Env, repo.Config, types.Item) error { return nil }

// Deleted does nothing.
func (Handler) Deleted(context.Context, format.Env, repo.Config, []types.Item) error { return nil }

// Admit refuses writes to a blacked-out repository and to paths its
// include and exclude patterns reject, as Artifactory does for every
// package type.
func (Handler) Admit(
	_ context.Context,
	_ format.Env,
	c repo.Config,
	w format.Write,
) (format.Write, error) {
	if err := CheckBlackedOut(c, w.Path); err != nil {
		return w, err
	}
	if err := CheckPatterns(c, w.Path); err != nil {
		return w, err
	}
	return w, nil
}

// CheckBlackedOut refuses any write to a blacked-out repository. Status and
// wording are Artifactory's BlackedOutException, which keeps
// RepoRejectException's default 404 (Artifactory OSS 6.20,
// RealRepoBase.assertValidPath). A format overriding Admit checks this
// first, and its own policies (Maven's snapshot handling) before
// CheckPatterns, in Artifactory's order.
func CheckBlackedOut(c repo.Config, path string) error {
	if c.BlackedOut == nil || !*c.BlackedOut {
		return nil
	}
	return format.Reject(http.StatusNotFound,
		"The repository '%s' is blacked out and cannot serve artifact '%s:%s'.", c.Key, c.Key, path)
}

// CheckPatterns refuses a file path outside c's includesPattern or inside
// its excludesPattern: 409 for a write in Artifactory's
// IncludeExcludeException, which a download would answer 404.
func CheckPatterns(c repo.Config, path string) error {
	if authz.PatternsOf(c).Accepts(path, false) {
		return nil
	}
	return format.Reject(
		http.StatusConflict,
		"The repository '%s' rejected the artifact '%s:%s' due to its include/exclude pattern settings.",
		c.Key,
		c.Key,
		path,
	)
}

// Classify reports every generic file as unique content.
func (Handler) Classify(repo.Config, string) format.Class { return format.ClassUnique }
