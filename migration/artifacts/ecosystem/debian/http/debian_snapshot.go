package debianhttp

import (
	"bytes"
	"context"
	"crypto/sha256"
	"encoding/json"
	"errors"
	"fmt"
	"net/http"
	"path"
	"regexp"
	"slices"
	"strings"
	"time"

	"github.com/go-chi/chi/v5"

	accessapi "github.com/weaveplatform/artifactweave/migration/access/api"
	"github.com/weaveplatform/artifactweave/migration/artifacts/adapters/http/itemkit"
	"github.com/weaveplatform/artifactweave/migration/artifacts/api/repo"
	"github.com/weaveplatform/artifactweave/migration/artifacts/core/item"
	"github.com/weaveplatform/artifactweave/migration/artifacts/ecosystem/debian"
	"github.com/weaveplatform/artifactweave/migration/artifacts/ecosystem/generic"
	"github.com/weaveplatform/artifactweave/migration/kernel"
	artgen "github.com/weaveplatform/artifactweave/migration/platform/gen/artifactory"
	"github.com/weaveplatform/artifactweave/migration/platform/httpkit"
	"github.com/weaveplatform/artifactweave/migration/platform/httpkit/apierror"
	"github.com/weaveplatform/artifactweave/migration/platform/work"
	"github.com/weaveplatform/artifactweave/pkg/store"
	"github.com/weaveplatform/artifactweave/pkg/types"
)

const debianSnapshotKind = "debian-snapshot"

var debianSnapshotSegment = regexp.MustCompile(`^[A-Za-z0-9][A-Za-z0-9._-]{0,127}$`)

type debianSnapshotRequest struct {
	Source       string                     `json:"source"`
	Target       string                     `json:"target"`
	Tag          string                     `json:"tag"`
	Distribution string                     `json:"distribution"`
	Credential   kernel.CredentialReference `json:"credential"`
}

// CreateDebianSnapshot captures metadata only. The persistent queue handles
// async=1 and gives the synchronous operation the same atomic work identity.
func (s DebianAPI) CreateDebianSnapshot(
	ctx context.Context, req artgen.CreateDebianSnapshotRequestObject,
) (artgen.CreateDebianSnapshotResponseObject, error) {
	if req.Body == nil || !debianSnapshotSegment.MatchString(req.Body.Tag) ||
		!debianSnapshotSegment.MatchString(req.Body.Distribution) ||
		req.Body.TargetRepo == "" ||
		req.Params.Async != nil && !req.Params.Async.Valid() {
		return nil, httpkit.BadRequestf("A safe tag, targetRepo and distribution are required")
	}
	st, err := s.Content()
	if err != nil {
		return nil, err
	}
	p, err := httpkit.Principal(ctx)
	if err != nil {
		return nil, err
	}
	d, _ := accessapi.DeciderFrom(ctx)
	acc, err := itemkit.AccessFor(ctx, s.Access.Authorizer, p, d)
	if err != nil {
		return nil, s.InternalError(ctx, "Debian snapshot access", err)
	}
	job := debianSnapshotRequest{
		Source: req.RepoKey, Target: req.Body.TargetRepo,
		Tag: req.Body.Tag, Distribution: req.Body.Distribution,
		Credential: p.CredentialReference(),
	}
	if _, _, err := snapshotRepos(ctx, st, job, p, acc); err != nil {
		return nil, err
	}
	if err := claimDebianSnapshot(ctx, st, job, p); err != nil {
		return nil, s.ItemError(ctx, "Debian snapshot claim", err)
	}
	if s.Work == nil {
		if err := s.createDebianSnapshot(ctx, st, job, p, acc); err != nil {
			return nil, s.ItemError(ctx, "Debian snapshot", err)
		}
	} else {
		payload, err := json.Marshal(job)
		if err != nil {
			return nil, s.InternalError(ctx, "Debian snapshot request", err)
		}
		key := job.Target + "/" + job.Tag
		if len(key) > store.MaxWorkKey {
			return nil, httpkit.BadRequestf("Debian snapshot key is too long")
		}
		if err := s.Work.Enqueue(ctx, debianSnapshotKind, key, payload); err != nil {
			return nil, s.InternalError(ctx, "Debian snapshot queue", err)
		}
		if req.Params.Async == nil ||
			*req.Params.Async == artgen.CreateDebianSnapshotParamsAsyncN0 {
			if err := s.Work.EnsureFresh(ctx, debianSnapshotKind, key); err != nil {
				return nil, s.ItemError(ctx, "Debian snapshot", err)
			}
		}
	}
	verb := "created"
	if req.Params.Async != nil && *req.Params.Async == artgen.CreateDebianSnapshotParamsAsyncN1 {
		verb = "scheduled in"
	}
	return artgen.CreateDebianSnapshot200TextResponse(
		fmt.Sprintf("Debian snapshot %s %s %s", job.Tag, verb, job.Target)), nil
}

func snapshotRoot(job debianSnapshotRequest) string { return "snapshots/" + job.Tag }

func claimDebianSnapshot(
	ctx context.Context, st store.Store, job debianSnapshotRequest, p kernel.Principal,
) error {
	if err := st.Tx(ctx, func(ctx context.Context) error {
		marker, err := st.Items().Get(ctx, job.Target, false, snapshotRoot(job))
		if err == nil {
			props, propErr := st.Items().Properties(ctx, marker.ID)
			if propErr != nil {
				return fmt.Errorf("snapshot claim properties: %w", propErr)
			}
			if len(props["deb.snapshot.pending.source"]) == 1 &&
				props["deb.snapshot.pending.source"][0] == job.Source &&
				len(props["deb.snapshot.pending.distribution"]) == 1 &&
				props["deb.snapshot.pending.distribution"][0] == job.Distribution {
				return nil // retry the same failed or still-running request
			}
			return apierror.New(http.StatusConflict, "Debian snapshot already exists")
		}
		if !errors.Is(err, store.ErrNotFound) {
			return fmt.Errorf("snapshot claim lookup: %w", err)
		}
		marker, err = st.Items().Put(ctx, types.Item{
			RepoKey: job.Target, Name: snapshotRoot(job), Folder: true,
			Created: time.Now().UTC(), CreatedBy: p.Name,
		})
		if err != nil {
			return fmt.Errorf("snapshot claim folder: %w", err)
		}
		if err := st.Items().SetProperties(ctx, marker.ID,
			types.Properties{
				"deb.snapshot.pending.source":       {job.Source},
				"deb.snapshot.pending.distribution": {job.Distribution},
			}, time.Now().UTC()); err != nil {
			return fmt.Errorf("snapshot claim source: %w", err)
		}
		return nil
	}); err != nil {
		return fmt.Errorf("snapshot claim: %w", err)
	}
	return nil
}

func snapshotRepos(
	ctx context.Context, st store.Store, job debianSnapshotRequest,
	p kernel.Principal, acc itemkit.Access,
) (repo.Config, repo.Config, error) {
	sourceRepo, err := st.Repos().Get(ctx, job.Source)
	if errors.Is(err, store.ErrNotFound) {
		return repo.Config{}, repo.Config{}, itemkit.NotFoundRepo(job.Source)
	}
	if err != nil {
		return repo.Config{}, repo.Config{}, fmt.Errorf("snapshot source repository: %w", err)
	}
	targetRepo, err := st.Repos().Get(ctx, job.Target)
	if errors.Is(err, store.ErrNotFound) {
		return repo.Config{}, repo.Config{}, itemkit.NotFoundRepo(job.Target)
	}
	if err != nil {
		return repo.Config{}, repo.Config{}, fmt.Errorf("snapshot target repository: %w", err)
	}
	if sourceRepo.PackageType != repo.Debian || targetRepo.PackageType != repo.Debian ||
		targetRepo.Class != repo.Local ||
		!snapshotSourceClass(sourceRepo.Class) {
		return repo.Config{}, repo.Config{}, httpkit.BadRequestf(
			"Debian snapshots require a Debian source and local Debian target",
		)
	}
	if !acc.File(sourceRepo, "dists/"+job.Distribution+"/Release", types.ActionRead) ||
		!acc.File(targetRepo, snapshotRoot(job), types.ActionWrite|types.ActionManage) {
		return repo.Config{}, repo.Config{}, httpkit.Denied(p)
	}
	return sourceRepo, targetRepo, nil
}

func snapshotSourceClass(c repo.Class) bool {
	return c == repo.Local || c == repo.Remote || c == repo.Virtual || c == repo.Federated
}

// RegisterDebianSnapshotWork registers on q the handler that runs a queued
// Debian snapshot (kind "debian-snapshot") with k. It must run before
// Queue.Run: only registered kinds are leased, and an item of a kind with
// no handler waits (decision 0033), so the snapshots would stay queued.
// Every attempt, a retry after a restart included, checks the queued
// credential reference against the caller's current credentials and
// permissions through k's access API; without an authenticator there,
// the job is refused for good.
func RegisterDebianSnapshotWork(q *work.Queue, k *itemkit.Kit) error {
	s := DebianAPI{k}
	err := q.Handle(debianSnapshotKind, func(ctx context.Context, it types.WorkItem) error {
		var job debianSnapshotRequest
		if err := json.Unmarshal(it.Payload, &job); err != nil ||
			!debianSnapshotSegment.MatchString(job.Tag) ||
			!debianSnapshotSegment.MatchString(job.Distribution) ||
			job.Source == "" || job.Target == "" || job.Credential.Name == "" {
			return fmt.Errorf("%w: invalid debian snapshot job", work.ErrPermanent)
		}
		if k.Access.Authenticator == nil {
			return fmt.Errorf("%w: debian snapshot authentication unavailable", work.ErrPermanent)
		}
		p, err := k.Access.Authenticator.Current(ctx, job.Credential.Principal())
		if err != nil {
			return fmt.Errorf("%w: debian snapshot caller expired: %w", work.ErrPermanent, err)
		}
		p, d, err := k.Access.Authorizer.Authorize(ctx, p, p.Anonymous)
		if err != nil {
			return fmt.Errorf("snapshot current permissions: %w", err)
		}
		acc, err := itemkit.AccessFor(ctx, k.Access.Authorizer, p, d)
		if err != nil {
			return fmt.Errorf("snapshot access: %w", err)
		}
		if err := s.createDebianSnapshot(ctx, k.Store, job, p, acc); err != nil {
			return fmt.Errorf("snapshot %s/%s: %w", job.Target, job.Tag, err)
		}
		return nil
	})
	if err != nil {
		return fmt.Errorf("register debian snapshot work: %w", err)
	}
	return nil
}

func (s DebianAPI) createDebianSnapshot(
	ctx context.Context, st store.Store, job debianSnapshotRequest,
	p kernel.Principal, acc itemkit.Access,
) error {
	sourceRepo, targetRepo, err := snapshotRepos(ctx, st, job, p, acc)
	if err != nil {
		return err
	}
	root := snapshotRoot(job)
	if existing, err := st.Items().Get(ctx, targetRepo.Key, false, root); err == nil {
		props, propErr := st.Items().Properties(ctx, existing.ID)
		if propErr != nil {
			return fmt.Errorf("snapshot marker properties: %w", propErr)
		}
		if len(props["deb.snapshot.source"]) == 1 &&
			props["deb.snapshot.source"][0] == job.Source &&
			len(props["deb.snapshot.distribution"]) == 1 &&
			props["deb.snapshot.distribution"][0] == job.Distribution {
			return nil // a prior queue attempt finished before acknowledgement
		}
		if len(props["deb.snapshot.source"]) != 0 {
			return apierror.New(http.StatusConflict, "Debian snapshot already exists")
		}
		if len(props["deb.snapshot.pending.source"]) != 1 ||
			props["deb.snapshot.pending.source"][0] != job.Source ||
			len(props["deb.snapshot.pending.distribution"]) != 1 ||
			props["deb.snapshot.pending.distribution"][0] != job.Distribution {
			return apierror.New(http.StatusConflict, "Debian snapshot claim changed")
		}
	} else if !errors.Is(err, store.ErrNotFound) {
		return fmt.Errorf("snapshot marker lookup: %w", err)
	}
	files, err := s.debianSnapshotFiles(ctx, st, sourceRepo, job.Distribution, p, acc)
	if err != nil {
		return err
	}
	paths := make([]string, 0, len(files))
	for name := range files {
		paths = append(paths, name)
	}
	slices.Sort(paths)
	for _, name := range paths {
		if !acc.File(targetRepo, root+"/"+name, types.ActionWrite) {
			return httpkit.Denied(p)
		}
		_, err := s.Items(st).Deploy(debian.WithSnapshotWrite(ctx), item.Deploy{
			Target: item.Target{Repo: targetRepo, Path: root + "/" + name},
			Body:   bytes.NewReader(files[name]), By: p.Name,
		})
		if err != nil {
			return fmt.Errorf("snapshot metadata %s: %w", name, err)
		}
	}
	marker, err := st.Items().Put(ctx, types.Item{
		RepoKey: targetRepo.Key, Name: root, Folder: true,
		Created: time.Now().UTC(), CreatedBy: p.Name,
	})
	if err != nil {
		return fmt.Errorf("snapshot marker: %w", err)
	}
	if err := st.Items().SetProperties(ctx, marker.ID,
		types.Properties{
			"deb.snapshot.source":       {sourceRepo.Key},
			"deb.snapshot.distribution": {job.Distribution},
		}, time.Now().UTC()); err != nil {
		return fmt.Errorf("snapshot source property: %w", err)
	}
	return nil
}

func (s DebianAPI) debianSnapshotFiles(
	ctx context.Context, st store.Store, c repo.Config, distribution string,
	p kernel.Principal, acc itemkit.Access,
) (map[string][]byte, error) {
	if c.Class == repo.Virtual {
		view, err := s.buildVirtualDebianView(ctx, st, c, distribution, p, acc)
		return view.files, err
	}
	base := "dists/" + distribution + "/"
	release, _, err := s.debianMemberBytes(ctx, st, c, base+"Release", p, acc)
	if err != nil {
		return nil, fmt.Errorf("snapshot Release: %w", err)
	}
	refs, err := debian.ReleaseIndexRefs(distribution, release)
	if err != nil {
		return nil, fmt.Errorf("snapshot Release declarations: %w", err)
	}
	files := map[string][]byte{base + "Release": release}
	total := int64(len(release))
	for _, ref := range refs {
		if ref.Size > debianIndexLimit || total+ref.Size > 512<<20 {
			return nil, httpkit.BadRequestf("Debian snapshot metadata exceeds limit")
		}
		body, checkRead, err := s.debianMemberBytes(ctx, st, c, ref.Path, p, acc)
		if err != nil {
			return nil, fmt.Errorf("snapshot index %s: %w", ref.Path, err)
		}
		if int64(len(body)) != ref.Size ||
			fmt.Sprintf("%x", sha256.Sum256(body)) != ref.SHA256 {
			return nil, errDebianReleaseDigestMismatch
		}
		if debian.PackagesIndexPath(ref.Path) || debian.SourcesIndexPath(ref.Path) {
			paths, err := debianIndexPaths(ref.Path, body)
			if err != nil {
				return nil, err
			}
			for _, artifact := range paths {
				if !acc.File(c, artifact, types.ActionRead) ||
					generic.CheckDownload(c, artifact, false) != nil {
					return nil, httpkit.Denied(p)
				}
				if checkRead != nil {
					if err := checkRead(ctx, artifact); err != nil {
						return nil, fmt.Errorf("snapshot artifact authority: %w", err)
					}
				}
			}
		}
		files[ref.Path] = body
		total += int64(len(body))
		if debian.PackagesIndexPath(ref.Path) || debian.SourcesIndexPath(ref.Path) {
			folder := path.Dir(ref.Path)
			files[folder+"/by-hash/SHA256/"+ref.SHA256] = body
		}
	}
	for _, suffix := range []string{"InRelease", "Release.gpg"} {
		body, _, err := s.debianMemberBytes(ctx, st, c, base+suffix, p, acc)
		if errors.Is(err, store.ErrNotFound) {
			continue
		}
		if err != nil {
			return nil, fmt.Errorf("snapshot %s: %w", suffix, err)
		}
		files[base+suffix] = body
	}
	return files, nil
}

// RegisterDebianSnapshotRoutes mounts the snapshot reads, GET and HEAD of
// /artifactory/api/deb/{repoKey}/snapshots/{tag}/*, on r. Debian snapshots
// have a separate client root: metadata lives in the target repository, and
// packages continue to resolve from the recorded source.
func (s DebianAPI) RegisterDebianSnapshotRoutes(r chi.Router) {
	for _, method := range []string{http.MethodGet, http.MethodHead} {
		r.MethodFunc(
			method,
			"/artifactory/api/deb/{repoKey}/snapshots/{tag}/*",
			s.getDebianSnapshot,
		)
	}
}

func (s DebianAPI) getDebianSnapshot(w http.ResponseWriter, r *http.Request) {
	st, err := s.Content()
	if err != nil {
		httpkit.WriteErr(w, r, err)
		return
	}
	p, acc, err := s.ItemCaller(r.Context(), st)
	if err != nil {
		httpkit.WriteErr(w, r, err)
		return
	}
	key, tag := chi.URLParam(r, "repoKey"), chi.URLParam(r, "tag")
	if !debianSnapshotSegment.MatchString(tag) {
		httpkit.WriteErr(w, r, itemkit.ItemNotFound())
		return
	}
	c, err := st.Repos().Get(r.Context(), key)
	if err != nil || c.PackageType != repo.Debian || c.Class != repo.Local {
		httpkit.WriteErr(w, r, itemkit.ItemNotFound())
		return
	}
	root := "snapshots/" + tag
	marker, err := st.Items().Get(r.Context(), key, false, root)
	if err != nil || !marker.Folder {
		httpkit.WriteErr(w, r, itemkit.ItemNotFound())
		return
	}
	props, err := st.Items().Properties(r.Context(), marker.ID)
	if err != nil || len(props["deb.snapshot.source"]) != 1 {
		httpkit.WriteErr(w, r, itemkit.ItemNotFound())
		return
	}
	sourceKey := props["deb.snapshot.source"][0]
	sourceRepo, err := st.Repos().Get(r.Context(), sourceKey)
	if err != nil || sourceRepo.PackageType != repo.Debian {
		httpkit.WriteErr(w, r, itemkit.ItemNotFound())
		return
	}
	rel := strings.TrimPrefix(chi.URLParam(r, "*"), "/")
	if rel == "" || path.Clean(rel) != rel || strings.HasPrefix(rel, "../") {
		httpkit.WriteErr(w, r, itemkit.ItemNotFound())
		return
	}
	if !acc.File(c, root+"/"+rel, types.ActionRead) ||
		!acc.File(sourceRepo, rel, types.ActionRead) {
		httpkit.WriteErr(w, r, httpkit.Denied(p))
		return
	}
	if strings.HasPrefix(rel, "dists/") {
		it, err := st.Items().Get(r.Context(), key, false, root+"/"+rel)
		if err != nil || it.Folder {
			httpkit.WriteErr(w, r, itemkit.ItemNotFound())
			return
		}
		if err := s.checkDebianSnapshotIndex(r.Context(), rel, it, sourceRepo, p, acc); err != nil {
			httpkit.WriteErr(w, r, s.ItemError(r.Context(), "Debian snapshot index", err))
			return
		}
		s.ServeFile(w, r, st, itemkit.Found{Repo: c, Item: it}, p)
		return
	}
	if !debian.ValidBinaryPath(rel) && !debian.ValidSourcePath(rel) &&
		!strings.HasPrefix(rel, "pool/") {
		httpkit.WriteErr(w, r, itemkit.ItemNotFound())
		return
	}
	w.Header().Set("Location", "/artifactory/"+sourceKey+"/"+httpkit.EscapePath(rel))
	w.WriteHeader(http.StatusFound)
}

func (s DebianAPI) checkDebianSnapshotIndex(
	ctx context.Context, rel string, it types.Item, sourceRepo repo.Config,
	p kernel.Principal, acc itemkit.Access,
) error {
	_, byHash := debian.ByHashMetadataPath(rel)
	if !debian.PackagesIndexPath(rel) && !debian.SourcesIndexPath(rel) && !byHash {
		return nil
	}
	body, err := s.readDebianBlob(ctx, it.Actual.SHA256)
	if err != nil {
		return err
	}
	paths, err := debianIndexPaths(rel, body)
	if err != nil {
		return err
	}
	for _, artifact := range paths {
		if !acc.File(sourceRepo, artifact, types.ActionRead) ||
			generic.CheckDownload(sourceRepo, artifact, false) != nil {
			return httpkit.Denied(p)
		}
	}
	return nil
}
