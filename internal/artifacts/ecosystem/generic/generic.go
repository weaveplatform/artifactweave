package generic

import (
	"context"
	"net/http"
	"strings"

	"github.com/weaveplatform/artifactweave/internal/artifacts/api/repo"
	"github.com/weaveplatform/artifactweave/internal/artifacts/core/format"
	"github.com/weaveplatform/artifactweave/internal/artifacts/core/model"
)

// Handler implements format.Handler for generic content.
type Handler struct{}

// Deployed does nothing: a generic file belongs to no package.
func (Handler) Deployed(context.Context, format.Env, repo.Config, model.Item) error { return nil }

// Deleting does nothing.
func (Handler) Deleting(context.Context, format.Env, repo.Config, model.Item) error { return nil }

// Deleted does nothing.
func (Handler) Deleted(context.Context, format.Env, repo.Config, []model.Item) error { return nil }

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
	return format.Rejectf(http.StatusNotFound,
		"The repository '%s' is blacked out and cannot serve artifact '%s:%s'.", c.Key, c.Key, path)
}

// CheckPatterns refuses a file path outside c's includesPattern or inside
// its excludesPattern: 409 for a write in Artifactory's
// IncludeExcludeException, which a download answers 404 (CheckDownload).
func CheckPatterns(c repo.Config, path string) error {
	if repo.PatternsOf(c).Accepts(path, false) {
		return nil
	}
	return format.Rejectf(http.StatusConflict, rejectedByPatterns, c.Key, c.Key, path)
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
// slash (RepoPathImpl). The root is always accepted, and so is a system
// path (systemPath) by the patterns, though not by the blackout.
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
	if path == "" || systemPath(path) || repo.PatternsOf(c).Accepts(path, folder) {
		return nil
	}
	if c.Class == repo.Virtual {
		return format.Rejectf(http.StatusNotFound,
			"No repository found to serve the request for %s:%s", c.Key, path)
	}
	return format.Rejectf(http.StatusNotFound, rejectedByPatterns, c.Key, c.Key, path)
}

// systemPath reports a path Artifactory's include and exclude patterns do
// not apply to: a Maven index file, whose name starts
// nexus-maven-repository-index, and any path ending .index, such as P2's
// p2.index (NamingUtils.isSystem, which RepoBase.accepts passes before the
// patterns, in Artifactory OSS 6.20). 7.x documents nothing either way, so
// this follows 6.20 until a capture shows otherwise (#216).
func systemPath(p string) bool {
	name := p[strings.LastIndexByte(p, '/')+1:]
	return strings.HasPrefix(name, "nexus-maven-repository-index") || strings.HasSuffix(p, ".index")
}

// Classify reports every generic file as unique content.
func (Handler) Classify(repo.Config, string) format.Class { return format.ClassUnique }
