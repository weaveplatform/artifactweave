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

// CheckBlackedOut refuses any write to, or download from, a blacked-out
// repository. Status and wording are Artifactory's BlackedOutException,
// which keeps RepoRejectException's default 404 for both (Artifactory OSS
// 6.20, RealRepoBase.assertValidPath). A format overriding Admit checks this
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
// IncludeExcludeException, which a download answers 404 (CheckDownload).
func CheckPatterns(c repo.Config, path string) error {
	if authz.PatternsOf(c).Accepts(path, false) {
		return nil
	}
	return format.Reject(http.StatusConflict, rejectedByPatterns, c.Key, c.Key, path)
}

// rejectedByPatterns is IncludeExcludeException's message.
const rejectedByPatterns = "The repository '%s' rejected the artifact '%s:%s' due to its " +
	"include/exclude pattern settings."

// CheckDownload refuses what Artifactory refuses before it looks for the
// item a download names, whether or not it exists, and for an administrator
// too (DbLocalRepo.checkDownloadIsAllowed calls RealRepoBase.assertValidPath
// with downloadRequest set, ahead of the read permission check):
//   - any path of a blacked-out repository, 404;
//   - a path c's include and exclude patterns reject, 404 with
//     IncludeExcludeException's message.
//
// path is the one Artifactory checks: the archive of an archive entry and
// the file a checksum sidecar describes (RepoBase.accepts). folder is
// whether the request named a folder, which in Artifactory is a trailing
// slash (RepoPathImpl). The root is always accepted.
//
// A virtual repository has no blackedOut (only RealRepoDescriptor carries
// it), and its own patterns refusing the path leave it no member to search
// (VirtualRepoDownloadStrategy.getInfoFromSearchableRepositories), which it
// answers 404 with that message.
func CheckDownload(c repo.Config, path string, folder bool) error {
	if c.Class != repo.Virtual {
		if err := CheckBlackedOut(c, path); err != nil {
			return err
		}
	}
	if path == "" || authz.PatternsOf(c).Accepts(path, folder) {
		return nil
	}
	if c.Class == repo.Virtual {
		return format.Reject(http.StatusNotFound,
			"No repository found to serve the request for %s:%s", c.Key, path)
	}
	return format.Reject(http.StatusNotFound, rejectedByPatterns, c.Key, c.Key, path)
}

// Classify reports every generic file as unique content.
func (Handler) Classify(repo.Config, string) format.Class { return format.ClassUnique }
