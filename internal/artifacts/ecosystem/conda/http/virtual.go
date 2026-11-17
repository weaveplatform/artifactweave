package condahttp

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"net/http"
	"path"
	"time"

	"github.com/weaveplatform/artifactweave/internal/access/api"
	"github.com/weaveplatform/artifactweave/internal/artifacts/adapters/http/pkgroute"
	"github.com/weaveplatform/artifactweave/internal/artifacts/api/repo"
	"github.com/weaveplatform/artifactweave/internal/artifacts/core/remote/source"
	"github.com/weaveplatform/artifactweave/internal/artifacts/core/virtual"
	condafmt "github.com/weaveplatform/artifactweave/internal/artifacts/ecosystem/conda"
	"github.com/weaveplatform/artifactweave/internal/kernel"
	"github.com/weaveplatform/artifactweave/internal/platform/httpkit/apierror"
)

// errShardIndexMissing is a member that answers 404 for the shard index a
// virtual repository merges.
var errShardIndexMissing = errors.New("conda: a member has no shard index")

// virtualIndex merges the channel index of id's subdir that the members of
// a virtual repository serve (condafmt.Merger), in the package handlers'
// walk (pkgroute.Collect): nested virtual repositories flattened with a
// cycle or a duplicate skipped (decision 0008, R2), and a member that is
// blacked out, whose patterns reject policy.path, that the caller may read
// nothing in, or whose read misses or is refused passed over (R3, R5). A
// member whose patterns reject only a package's file still lists it. A
// member's fault, an upstream's 5xx, one it cannot reach or an answer that
// fails its checks, is passed over too, with a warning in the log
// (pkgroute.Kit.WarnPassedOver) [I]. Each record is filtered by Read in its
// member alone: no permission target can name the virtual repository
// (decision 0006), so a caller who may read only the members is served.
//
// The one exception to R3 is the CEP-16 shard index: JFrog's Conda page
// says "If any member in a virtual repository returns 404 for a shard
// index, the virtual repository also returns 404 for that shard index and
// falls back to repodata.json", so a member the walk reads whose index
// misses answers the virtual repository's 404 for it (errShardIndexMissing).
// A member the walk passes over before it reads, such as a blacked-out one,
// returns no 404 of its own and is passed over still [I].
//
// 7.x last changed a virtual repository's merged index when its latest
// member did [I], so modified is the latest of the parts'. The fence of
// each part read from its source in this request is passed again before a
// byte of the answer is written (serveIndex), so a source whose authority
// is withdrawn while the answer is built discloses nothing.
func (h handler) virtualIndex(
	r *http.Request,
	id condafmt.MetadataPath,
	policy readPolicy,
) (channelIndex, error) {
	shardIndex := !policy.download && path.Base(policy.path) == condafmt.ShardIndexFilename
	var modified time.Time
	var fences []func(context.Context) error
	parts, err := pkgroute.Collect(h.k, r, policy.path, func(mr *http.Request) (condafmt.Repodata, error) {
		part, err := h.part(mr, id, policy)
		h.k.WarnPassedOver(mr, err)
		if shardIndex && pkgroute.PassedOver(err) && shardNotFound(err) {
			return condafmt.Repodata{}, errShardIndexMissing
		}
		if part.modified.After(modified) {
			modified = part.modified
		}
		if err == nil && part.finish != nil {
			fences = append(fences, part.finish)
		}
		return part.records, err
	})
	if errors.Is(err, errShardIndexMissing) {
		return channelIndex{}, apierror.New(http.StatusNotFound, "Conda metadata not found")
	}
	if err != nil {
		return channelIndex{}, err //nolint:wrapcheck // the kit's answer or a member's fault, as it is
	}
	merged, _ := condafmt.Merger{Subdir: id.Subdir}.Merge(r.Context(), parts)
	return channelIndex{records: merged, modified: modified, finish: func(ctx context.Context) error {
		for _, fence := range fences {
			if err := fence(ctx); err != nil {
				return fmt.Errorf("conda: virtual source authority: %w", err)
			}
		}
		return nil
	}}, nil
}

// part is the index of id's subdir that the member request mr serves the
// caller, for a virtual repository's merge, once its authority holds. A
// member whose index the caller may not read is passed over as a Read
// refusal (source.ErrDenied), not as a miss, so it never answers the shard
// index's 404 for a caller who may not see it.
//
// A remote member's part is kept for the virtual repository's
// virtualRetrievalCachePeriodSecs (pkgroute.Kit.MergedPart), while a local
// or federated member's is read on every request: JFrog's Conda page says a
// virtual repository's metadata reflects "changes in the aggregated local
// repositories in real-time", while "for aggregated remote repositories,
// the virtual repository metadata is renewed on demand" by the "Retrieval
// Cache Period parameter of your virtual repository", and 7.161.26 takes
// that period for a Conda virtual repository
// (VirtualRepositoryConfigurationImpl 91-93,
// RepoConfigDescriptorBuilder.populateVirtualCondaDescriptor 953-959). The
// remote's authority is checked when its part is built, and its fence is
// returned to be passed again before delivery, but not on a hit, as 7.x
// serves a kept document without asking its members (Composer's virtualP2
// keeps the same rule). A part that misses is not kept, so the next request
// reads the member again.
func (h handler) part(mr *http.Request, id condafmt.MetadataPath, policy readPolicy) (channelIndex, error) {
	canonical := condafmt.IndexPath(id.Channel, id.Subdir, "repodata.json")
	if !h.k.CanFolder(mr, path.Dir(canonical), api.ActionRead) || !h.k.Can(mr, canonical, api.ActionRead) {
		return channelIndex{}, source.ErrDenied
	}
	if pkgroute.RequestFrom(mr).Repo.Class != repo.Remote {
		index, err := h.memberIndex(mr, canonical, policy)
		return index, pkgroute.SourceError(err, false) //nolint:wrapcheck // the member's answer, as the protocol gives it
	}
	// fence is set only when this request built the part: a request that
	// waited on another's build, or was served a kept part, has none.
	var fence func(context.Context) error
	e, err := h.k.MergedPart(mr, canonical, func(mr *http.Request) (virtual.Entry, error) {
		index, err := h.memberIndex(mr, canonical, policy)
		if err == nil && index.finish != nil {
			err = index.finish(mr.Context())
			fence = index.finish
		}
		err = pkgroute.SourceError(err, false)
		switch {
		case errors.Is(err, kernel.ErrForbidden), errors.Is(err, kernel.ErrUnauthenticated):
			// A source that refuses the member no longer serves its part,
			// so the kept one is dropped (virtual.ErrPassOver), as 7.x drops
			// a resource no member serves; only a fault keeps it as the
			// stale fallback (virtual.Cache.Get).
			return virtual.Entry{}, fmt.Errorf("%w: %w", virtual.ErrPassOver, err)
		case err != nil:
			return virtual.Entry{}, err //nolint:wrapcheck // the member's answer, as the protocol gives it
		}
		body, err := json.Marshal(keptPart{Records: index.records, Modified: index.modified})
		return virtual.Entry{Body: body}, err //nolint:wrapcheck // records of strings and numbers always marshal
	})
	if err != nil {
		return channelIndex{}, err //nolint:wrapcheck // the build's answer, as it is
	}
	var kept keptPart
	if err := json.Unmarshal(e.Body, &kept); err != nil {
		return channelIndex{}, fmt.Errorf("conda: kept part of %s: %w", pkgroute.RequestFrom(mr).Repo.Key, err)
	}
	return channelIndex{records: kept.Records, modified: kept.Modified, finish: fence}, nil
}

// keptPart is a remote member's part as the cache keeps it.
type keptPart struct {
	Records  condafmt.Repodata `json:"records"`
	Modified time.Time         `json:"modified"`
}

// virtualPackage serves the package id through a virtual repository from
// the first member, in the package handlers' walk (pkgroute.Kit.
// MergeMembers, which checks the virtual repository's own patterns and
// each member's policy at the package), whose index lists it and which the
// caller may read it in: a member's policy refusing the package, a
// blacked-out one included, passes over it as one without the package does
// (R5, decision 0032), since its index lists a package whose file its
// patterns reject. A member whose index read misses, is refused or fails is
// passed over too, with a warning for a fault [I]. The member's index is
// its part of the virtual repository's index (part), so a remote member
// serves the package its kept part lists.
func (h handler) virtualPackage(w http.ResponseWriter, r *http.Request, id condafmt.PackagePath) error {
	members, err := h.k.MergeMembers(r, id.Path)
	if err != nil {
		return fmt.Errorf("conda: members: %w", err)
	}
	indexID, _ := condafmt.ParseMetadataPath(condafmt.IndexPath(id.Channel, id.Subdir, "repodata.json"))
	for _, mr := range members {
		if !h.k.Can(mr, id.Path, api.ActionRead) ||
			!h.k.CanFolder(mr, path.Dir(id.Path), api.ActionRead) {
			continue
		}
		index, err := h.part(mr, indexID, packageLookup(id))
		h.k.WarnPassedOver(mr, err)
		if pkgroute.PassedOver(err) {
			continue
		}
		if err != nil {
			return err
		}
		rec, ok := recordOf(index.records, id)
		if !ok {
			continue
		}
		if index.finish != nil {
			if err := index.finish(r.Context()); err != nil {
				return fmt.Errorf("conda: virtual member authority: %w", err)
			}
		}
		return h.serveRecord(w, mr, id, rec)
	}
	return kernel.ErrNotFound
}

// requireIndexRead refuses a caller who may not read a channel document of
// the request's repository before it is built: Read on the document and its
// folder, or, for a virtual repository, whose documents cover the whole
// channel, Read on the root of some member (pkgroute.Kit.RequireRoot), as
// 7.x's gate for a virtual repository's root asks it
// (AuthorizationServiceBase.computeUserHasPermissionsOnRepositoryRoot
// 1047-1078, decision 0008 R3). A caller who passes still gets only the
// records of the members it may read.
func (h handler) requireIndexRead(w http.ResponseWriter, r *http.Request, id condafmt.MetadataPath) bool {
	if pkgroute.RequestFrom(r).Repo.Class == repo.Virtual {
		return h.k.RequireRoot(w, r)
	}
	return h.k.RequireFolder(w, r, path.Dir(id.Path), api.ActionRead) &&
		h.k.Require(w, r, id.Path, api.ActionRead)
}
