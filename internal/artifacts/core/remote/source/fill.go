package source

import (
	"fmt"
	"path"
	"strings"

	accessapi "github.com/weaveplatform/artifactweave/internal/access/api"
	"github.com/weaveplatform/artifactweave/internal/artifacts/core/model"
	"github.com/weaveplatform/artifactweave/internal/kernel"
)

// ErrAssumedOffline is a remote's answer for a path it cannot serve while
// its upstream is assumed offline: nothing complete cached, and no upstream
// asked. 7.161.26's RemoteRepoBase.internalGetInfo asks isOffline, which is
// true while the repository is assumed offline, before it asks the upstream
// anything, and answers such a path with an UnfoundRepoResource, which the
// download answers 404 (DownloadServiceImpl.respondResourceNotFound). It is
// kernel.ErrNotFound, so a route that answers its own miss answers this
// one too, and a virtual repository passes over the member, as over an
// offline one (decision 0008, R3); the item route names the repository's
// state in its reason.
var ErrAssumedOffline = fmt.Errorf("source: remote assumed offline: %w", kernel.ErrNotFound)

// CacheFillDenied refuses a read that would fill a remote repository's
// cache from its upstream for a caller who may read the remote but not
// fill its cache: who lacks Deploy/Cache on the cache path, or Annotate on
// a metadata path. JFrog's Permissions page: on a remote, Read "only allows
// downloading from the remote cache", and new files need "the Deploy/Cache
// permission". 7.161.26's RemoteRepoBase.downloadAndSave calls
// RepositoryServiceImpl.assertValidDeployPathAndPermissions on the cache
// path before it downloads.
//
// It is kernel.ErrNotFound for every route but the item route, which
// answers Message as 7.x's download does (403 "Rejected artifact download
// request: ..."). JFrog's KB article "Why Am I Getting Random 404 Errors
// When Downloading From a Remote Repository" says installing packages
// without Deploy "will result in a 404 error - even if the artifact exists
// in the upstream repository", and RTFACT-30824 shows a PyPI page answered
// 404 for a read-only user; which body each native route sends is closed
// code, inferred to be its own miss [I].
type CacheFillDenied struct {
	// User is the caller's name, "anonymous" for an anonymous one.
	User string
	// Repo is the remote repository's key.
	Repo string
	// Path is the cache path checked: where 7.x keeps the file.
	Path string
	// Annotate is set for a metadata path, which needs Annotate.
	Annotate bool
}

// Error returns Message.
func (e *CacheFillDenied) Error() string { return e.Message() }

// Message is assertValidDeployPathAndPermissions' reason, for the cache
// path: "User <user> is not permitted to deploy '<path>' into
// '<remote>-cache:<path>'.", or "... to annotate '<path>' on ..." for a
// metadata path (RepositoryServiceImpl 2817-2870, 7.161.26).
func (e *CacheFillDenied) Message() string {
	verb, preposition := "deploy", "into"
	if e.Annotate {
		verb, preposition = "annotate", "on"
	}
	return fmt.Sprintf("User %s is not permitted to %s '%s' %s '%s%s:%s'.",
		e.User, verb, e.Path, preposition, e.Repo, model.CacheSuffix, e.Path)
}

// Unwrap is kernel.ErrNotFound, the answer of every route but the item
// route.
func (e *CacheFillDenied) Unwrap() error { return kernel.ErrNotFound }

// AuthorizeFill checks that q's caller may fill the remote's cache with
// q's resource: Deploy/Cache (accessapi.ActionWrite) on the path 7.x keeps
// it at, the resource's PolicyPath when it names one and its Path
// otherwise, or Annotate on a metadata path. Permissions on a remote are
// always those of its cache ("permissions are always on the cache repo
// key", RemoteRepoBase.checkDownloadIsAllowed), which the decider judges as
// the remote. It returns a *CacheFillDenied, or nil.
//
// Only the checks of assertValidDeployPathAndPermissions that a fill can
// meet are made. Delete/Overwrite is checked there only to replace a file
// the cache repository does not treat as expirable, without a forced
// expiry check (DbStoringRepoMixin.assertOverride), and 7.x refreshes only
// expirable files (ZapServiceImpl.zapFile zaps nothing else), as the remote
// policy refreshes only metadata and mutable files; Maven metadata and
// checksums are never protected. The storage quota has no counterpart.
func AuthorizeFill(q Request) error {
	at := q.Resource.PolicyPath
	if at == "" {
		at = q.Resource.Path
	}
	want := accessapi.ActionWrite
	annotate := MetadataPath(at)
	if annotate {
		want = accessapi.ActionAnnotate
	}
	if q.Access != nil && q.Access.Allows(accessapi.ResourceArtifact,
		accessapi.Repo{Key: q.Repo.Key, Class: string(q.Repo.Class)}, at, want) {
		return nil
	}
	return &CacheFillDenied{User: q.Subject.Name, Repo: q.Repo.Key, Path: at, Annotate: annotate}
}

// MetadataPath reports whether p is what 7.161.26's NamingUtils.isMetadata
// calls metadata, which assertValidDeployPathAndPermissions asks Annotate
// for: a file name holding ":" that ends in ":properties", ":statistics"
// or ":maven-metadata.xml", the "<resource>:<metadata>" form. A plain
// maven-metadata.xml is a file like any other, needing Deploy ("Maven
// metadata is treated as regular file").
func MetadataPath(p string) bool {
	name := path.Base(p)
	if !strings.Contains(name, ":") {
		return false
	}
	for _, suffix := range []string{":properties", ":statistics", ":maven-metadata.xml"} {
		if strings.HasSuffix(name, suffix) {
			return true
		}
	}
	return false
}
