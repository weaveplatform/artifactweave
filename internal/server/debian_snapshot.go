package server

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

	"github.com/weaveplatform/artifactweave/internal/auth"
	"github.com/weaveplatform/artifactweave/internal/authz"
	"github.com/weaveplatform/artifactweave/internal/format/debian"
	"github.com/weaveplatform/artifactweave/internal/format/generic"
	"github.com/weaveplatform/artifactweave/internal/server/apierror"
	artgen "github.com/weaveplatform/artifactweave/internal/server/gen/artifactory"
	"github.com/weaveplatform/artifactweave/internal/service/item"
	"github.com/weaveplatform/artifactweave/internal/service/work"
	"github.com/weaveplatform/artifactweave/pkg/repo"
	"github.com/weaveplatform/artifactweave/pkg/store"
	"github.com/weaveplatform/artifactweave/pkg/types"
)

const debianSnapshotKind = "debian-snapshot"

var debianSnapshotSegment = regexp.MustCompile(`^[A-Za-z0-9][A-Za-z0-9._-]{0,127}$`)

type debianSnapshotRequest struct {
	Source       string                   `json:"source"`
	Target       string                   `json:"target"`
	Tag          string                   `json:"tag"`
	Distribution string                   `json:"distribution"`
	Credential   auth.CredentialReference `json:"credential"`
}

// CreateDebianSnapshot captures metadata only. The persistent queue handles
// async=1 and gives the synchronous operation the same atomic work identity.
func (s artifactoryService) CreateDebianSnapshot(
	ctx context.Context, req artgen.CreateDebianSnapshotRequestObject,
) (artgen.CreateDebianSnapshotResponseObject, error) {
	if req.Body == nil || !debianSnapshotSegment.MatchString(req.Body.Tag) ||
		!debianSnapshotSegment.MatchString(req.Body.Distribution) ||
		req.Body.TargetRepo == "" ||
		req.Params.Async != nil && !req.Params.Async.Valid() {
		return nil, badRequest("A safe tag, targetRepo and distribution are required")
	}
	st, err := s.content()
	if err != nil {
		return nil, err
	}
	p, err := principal(ctx)
	if err != nil {
		return nil, err
	}
	acc, err := accessFor(ctx, st, p)
	if err != nil {
		return nil, s.internalError(ctx, "Debian snapshot access", err)
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
		return nil, s.itemError(ctx, "Debian snapshot claim", err)
	}
	if s.cfg.Work == nil {
		if err := s.createDebianSnapshot(ctx, st, job, p, acc); err != nil {
			return nil, s.itemError(ctx, "Debian snapshot", err)
		}
	} else {
		payload, err := json.Marshal(job)
		if err != nil {
			return nil, s.internalError(ctx, "Debian snapshot request", err)
		}
		key := job.Target + "/" + job.Tag
		if len(key) > store.MaxWorkKey {
			return nil, badRequest("Debian snapshot key is too long")
		}
		if err := s.cfg.Work.Enqueue(ctx, debianSnapshotKind, key, payload); err != nil {
			return nil, s.internalError(ctx, "Debian snapshot queue", err)
		}
		if req.Params.Async == nil ||
			*req.Params.Async == artgen.CreateDebianSnapshotParamsAsyncN0 {
			if err := s.cfg.Work.EnsureFresh(ctx, debianSnapshotKind, key); err != nil {
				return nil, s.itemError(ctx, "Debian snapshot", err)
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
	ctx context.Context, st store.Store, job debianSnapshotRequest, p auth.Principal,
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
	p auth.Principal, acc access,
) (repo.Config, repo.Config, error) {
	sourceRepo, err := st.Repos().Get(ctx, job.Source)
	if errors.Is(err, store.ErrNotFound) {
		return repo.Config{}, repo.Config{}, notFoundRepo(job.Source)
	}
	if err != nil {
		return repo.Config{}, repo.Config{}, fmt.Errorf("snapshot source repository: %w", err)
	}
	targetRepo, err := st.Repos().Get(ctx, job.Target)
	if errors.Is(err, store.ErrNotFound) {
		return repo.Config{}, repo.Config{}, notFoundRepo(job.Target)
	}
	if err != nil {
		return repo.Config{}, repo.Config{}, fmt.Errorf("snapshot target repository: %w", err)
	}
	if sourceRepo.PackageType != repo.Debian || targetRepo.PackageType != repo.Debian ||
		targetRepo.Class != repo.Local ||
		!snapshotSourceClass(sourceRepo.Class) {
		return repo.Config{}, repo.Config{}, badRequest(
			"Debian snapshots require a Debian source and local Debian target",
		)
	}
	if !acc.file(sourceRepo, "dists/"+job.Distribution+"/Release", types.ActionRead) ||
		!acc.file(targetRepo, snapshotRoot(job), types.ActionWrite|types.ActionManage) {
		return repo.Config{}, repo.Config{}, denied(p)
	}
	return sourceRepo, targetRepo, nil
}

func snapshotSourceClass(c repo.Class) bool {
	return c == repo.Local || c == repo.Remote || c == repo.Virtual || c == repo.Federated
}

// RegisterDebianSnapshotWork must run before Queue.Run. The queued credential
// reference is checked against current credentials and permissions on every
// attempt, including retries after a restart.
func RegisterDebianSnapshotWork(q *work.Queue, cfg Config) error {
	s := artifactoryService{cfg: cfg}
	err := q.Handle(debianSnapshotKind, func(ctx context.Context, it types.WorkItem) error {
		var job debianSnapshotRequest
		if err := json.Unmarshal(it.Payload, &job); err != nil ||
			!debianSnapshotSegment.MatchString(job.Tag) ||
			!debianSnapshotSegment.MatchString(job.Distribution) ||
			job.Source == "" || job.Target == "" || job.Credential.Name == "" {
			return fmt.Errorf("%w: invalid Debian snapshot job", work.ErrPermanent)
		}
		if cfg.Auth == nil {
			return fmt.Errorf("%w: Debian snapshot authentication unavailable", work.ErrPermanent)
		}
		p, err := cfg.Auth.Current(ctx, job.Credential.Principal(), cfg.Store.Tokens())
		if err != nil {
			return fmt.Errorf("%w: Debian snapshot caller expired: %w", work.ErrPermanent, err)
		}
		p, err = auth.Authorize(ctx, authz.Sources{
			Permissions: cfg.Store.Permissions(), Groups: cfg.Store.Groups(),
			AnonymousEnabled: p.Anonymous,
		}, p)
		if err != nil {
			return fmt.Errorf("snapshot current permissions: %w", err)
		}
		acc, err := accessFor(ctx, cfg.Store, p)
		if err != nil {
			return fmt.Errorf("snapshot access: %w", err)
		}
		if err := s.createDebianSnapshot(ctx, cfg.Store, job, p, acc); err != nil {
			return fmt.Errorf("snapshot %s/%s: %w", job.Target, job.Tag, err)
		}
		return nil
	})
	if err != nil {
		return fmt.Errorf("register Debian snapshot work: %w", err)
	}
	return nil
}

func (s artifactoryService) createDebianSnapshot(
	ctx context.Context, st store.Store, job debianSnapshotRequest,
	p auth.Principal, acc access,
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
		if !acc.file(targetRepo, root+"/"+name, types.ActionWrite) {
			return denied(p)
		}
		_, err := s.items(st).Deploy(debian.WithSnapshotWrite(ctx), item.Deploy{
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

func (s artifactoryService) debianSnapshotFiles(
	ctx context.Context, st store.Store, c repo.Config, distribution string,
	p auth.Principal, acc access,
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
			return nil, badRequest("Debian snapshot metadata exceeds limit")
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
				if !acc.file(c, artifact, types.ActionRead) ||
					generic.CheckDownload(c, artifact, false) != nil {
					return nil, denied(p)
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

// Debian snapshots have a separate client root. Metadata lives in the target
// repository; packages continue to resolve from the recorded source.
func (s artifactoryService) registerDebianSnapshotRoutes(r chi.Router) {
	for _, method := range []string{http.MethodGet, http.MethodHead} {
		r.MethodFunc(
			method,
			"/artifactory/api/deb/{repoKey}/snapshots/{tag}/*",
			s.getDebianSnapshot,
		)
	}
}

func (s artifactoryService) getDebianSnapshot(w http.ResponseWriter, r *http.Request) {
	st, err := s.content()
	if err != nil {
		writeErr(w, r, err)
		return
	}
	p, acc, err := s.itemCaller(r.Context(), st)
	if err != nil {
		writeErr(w, r, err)
		return
	}
	key, tag := chi.URLParam(r, "repoKey"), chi.URLParam(r, "tag")
	if !debianSnapshotSegment.MatchString(tag) {
		writeErr(w, r, itemNotFound())
		return
	}
	c, err := st.Repos().Get(r.Context(), key)
	if err != nil || c.PackageType != repo.Debian || c.Class != repo.Local {
		writeErr(w, r, itemNotFound())
		return
	}
	root := "snapshots/" + tag
	marker, err := st.Items().Get(r.Context(), key, false, root)
	if err != nil || !marker.Folder {
		writeErr(w, r, itemNotFound())
		return
	}
	props, err := st.Items().Properties(r.Context(), marker.ID)
	if err != nil || len(props["deb.snapshot.source"]) != 1 {
		writeErr(w, r, itemNotFound())
		return
	}
	sourceKey := props["deb.snapshot.source"][0]
	sourceRepo, err := st.Repos().Get(r.Context(), sourceKey)
	if err != nil || sourceRepo.PackageType != repo.Debian {
		writeErr(w, r, itemNotFound())
		return
	}
	rel := strings.TrimPrefix(chi.URLParam(r, "*"), "/")
	if rel == "" || path.Clean(rel) != rel || strings.HasPrefix(rel, "../") {
		writeErr(w, r, itemNotFound())
		return
	}
	if !acc.file(c, root+"/"+rel, types.ActionRead) ||
		!acc.file(sourceRepo, rel, types.ActionRead) {
		writeErr(w, r, denied(p))
		return
	}
	if strings.HasPrefix(rel, "dists/") {
		it, err := st.Items().Get(r.Context(), key, false, root+"/"+rel)
		if err != nil || it.Folder {
			writeErr(w, r, itemNotFound())
			return
		}
		if err := s.checkDebianSnapshotIndex(r.Context(), rel, it, sourceRepo, p, acc); err != nil {
			writeErr(w, r, s.itemError(r.Context(), "Debian snapshot index", err))
			return
		}
		s.serveFile(w, r, st, found{repo: c, item: it}, p)
		return
	}
	if !debian.ValidBinaryPath(rel) && !debian.ValidSourcePath(rel) &&
		!strings.HasPrefix(rel, "pool/") {
		writeErr(w, r, itemNotFound())
		return
	}
	w.Header().Set("Location", "/artifactory/"+sourceKey+"/"+escapePath(rel))
	w.WriteHeader(http.StatusFound)
}

func (s artifactoryService) checkDebianSnapshotIndex(
	ctx context.Context, rel string, it types.Item, sourceRepo repo.Config,
	p auth.Principal, acc access,
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
		if !acc.file(sourceRepo, artifact, types.ActionRead) ||
			generic.CheckDownload(sourceRepo, artifact, false) != nil {
			return denied(p)
		}
	}
	return nil
}
