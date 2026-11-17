package condahttp

import (
	"bytes"
	"context"
	"errors"
	"fmt"
	"io"
	"net/http"
	"path"
	"strconv"
	"strings"
	"time"

	"github.com/go-chi/chi/v5"

	"github.com/weaveplatform/artifactweave/internal/access/api"
	"github.com/weaveplatform/artifactweave/internal/artifacts/adapters/http/itemkit"
	"github.com/weaveplatform/artifactweave/internal/artifacts/adapters/http/pkgroute"
	"github.com/weaveplatform/artifactweave/internal/artifacts/api/repo"
	"github.com/weaveplatform/artifactweave/internal/artifacts/core/remote/source"
	condafmt "github.com/weaveplatform/artifactweave/internal/artifacts/ecosystem/conda"
	"github.com/weaveplatform/artifactweave/internal/kernel"
	"github.com/weaveplatform/artifactweave/internal/platform/httpkit/apierror"
	"github.com/weaveplatform/artifactweave/internal/platform/httpkit/baseurl"
)

type virtualSource struct{}

func (virtualSource) Item(
	context.Context,
	pkgroute.Store,
	repo.Config,
	string,
) (pkgroute.Found, error) {
	return pkgroute.Found{}, source.ErrUnsupported
}

// Protocol returns the Conda channel protocol for the router kit: GET and
// HEAD of every path under /artifactory/api/conda/{repoKey}, for local and
// federated repositories from their own items, virtual ones from their
// members, and remote ones through remote when it is not nil; a class
// without a source answers 501. settings.ShardsEnabled turns on the CEP-16
// shard documents. The virtual class's source supports no single-item
// lookup, because the protocol reads a virtual repository's members itself.
func Protocol(remote *source.Service, settings condafmt.Settings) pkgroute.Protocol {
	sources := map[repo.Class]pkgroute.Source{
		repo.Local: pkgroute.Local{}, repo.Federated: pkgroute.Local{},
		repo.Virtual: virtualSource{},
	}
	if remote != nil {
		sources[repo.Remote] = pkgroute.Remote{Service: remote}
	}
	return pkgroute.Protocol{
		Name: "conda", Types: []repo.PackageType{repo.Conda},
		ErrorProfile: apierror.ProfileArtifactory, Sources: sources,
		Routes: func(r chi.Router, k *pkgroute.Kit) {
			h := handler{k: k, settings: settings}
			r.Get("/*", h.read)
			r.Head("/*", h.read)
		},
	}
}

type handler struct {
	k        *pkgroute.Kit
	settings condafmt.Settings
}

func (h handler) read(w http.ResponseWriter, r *http.Request) {
	p, err := pkgroute.ParsePath(pkgroute.RequestFrom(r).Rest)
	if err != nil || r.URL.RawQuery != "" {
		h.k.Error(w, r, apierror.New(http.StatusBadRequest, "invalid Conda channel path"))
		return
	}
	requested := p.String()
	if id, ok := condafmt.ParseMetadataPath(requested); ok {
		h.readMetadata(w, r, id)
		return
	}
	if id, ok := condafmt.ParsePackagePath(requested); ok {
		h.readPackage(w, r, id)
		return
	}
	h.k.Error(w, r, apierror.New(http.StatusNotFound, "Conda channel path not found"))
}

func (h handler) readMetadata(w http.ResponseWriter, r *http.Request, id condafmt.MetadataPath) {
	if !h.requireIndexRead(w, r, id) {
		return
	}
	sharded := id.Filename == condafmt.ShardIndexFilename || condafmt.IsShardFilename(id.Filename)
	if sharded && !h.settings.ShardsEnabled {
		h.k.Error(w, r, apierror.New(http.StatusNotFound, "Conda metadata not found"))
		return
	}
	if id.Filename == "notices.json" {
		h.serveBytes(w, r, []byte(`{"notices":[]}`), "application/json")
		return
	}
	if id.Filename == "repodata.json.bz2" {
		h.k.Error(w, r, apierror.New(http.StatusNotFound, "Conda metadata not found"))
		return
	}
	// 7.x calculates a local channel's index files and caches a remote's,
	// each at its own path, and reads them through the download path, so a
	// blackout or a pattern on the requested document refuses it, naming
	// it, and a virtual repository's own patterns refuse it too (R1, R5). A
	// pattern on one package's file leaves its record listed (RTFACT-24940).
	if err := h.k.CheckDocument(r, id.Path); err != nil {
		h.k.Error(w, r, err)
		return
	}
	canonical := condafmt.IndexPath(id.Channel, id.Subdir, "repodata.json")
	index, err := h.memberIndex(r, canonical, readPolicy{path: id.Path})
	if err != nil {
		h.k.Error(w, r, pkgroute.SourceError(err, false))
		return
	}
	var encoded map[string][]byte
	if sharded {
		encoded, err = condafmt.EncodeShards(
			index.records,
			id.Channel,
			id.Subdir,
			baseurl.From(r.Context())+path.Dir(r.URL.Path)+"/",
		)
	} else {
		encoded, err = condafmt.EncodeRepodata(index.records, id.Channel, id.Subdir)
	}
	if err != nil {
		h.k.Error(w, r, err)
		return
	}
	body := encoded[id.Filename]
	if body == nil {
		h.k.Error(w, r, apierror.New(http.StatusNotFound, "Conda metadata not found"))
		return
	}
	// The .zst forms keep application/zstd, which 7.x's MIME table, with no
	// zst row, would not give a stored file; no capture shows one [I].
	media := "application/json"
	if strings.HasSuffix(id.Filename, ".zst") {
		media = "application/zstd"
	}
	h.serveIndex(w, r, document{
		name: id.Filename, media: media, body: body, modified: index.modified,
	}, index.finish)
}

// serveIndex answers d once finish, the fence of every source it was built
// from, passes, so no byte of it is written after a source's authority is
// withdrawn.
func (h handler) serveIndex(w http.ResponseWriter, r *http.Request, d document,
	finish func(context.Context) error,
) {
	if finish != nil {
		if err := finish(r.Context()); err != nil {
			h.k.Error(w, r, pkgroute.SourceError(err, false))
			return
		}
	}
	serveDocument(w, r, d)
}

// serveBytes answers notices.json, which 7.x does not store as a channel
// document, without a stored file's headers [I].
func (h handler) serveBytes(w http.ResponseWriter, r *http.Request, body []byte, media string) {
	w.Header().Set("Content-Type", media)
	w.Header().Set("Cache-Control", "private, no-store")
	w.Header().Set("Content-Length", strconv.Itoa(len(body)))
	itemkit.ServeContent(w, r, path.Base(r.URL.Path), time.Time{}, bytes.NewReader(body))
}

// channelIndex is the index a repository answers a channel's documents
// from: the records the caller may read, the fence to pass before any is
// disclosed, and when the stored or cached index they were read from last
// changed, the latest of its members' for a virtual repository, zero when
// there is none.
type channelIndex struct {
	records  condafmt.Repodata
	finish   func(context.Context) error
	modified time.Time
}

// readPolicy is how the download policy applies to the reads an index is
// built from. path is the one 7.x checks: the index document the request
// answers, already checked with CheckDocument, or the package whose record a
// download looks up, where download leaves the lookup to the blackout alone,
// since the download checks the package at its own path.
type readPolicy struct {
	path     string
	download bool
}

// memberIndex returns only package records the caller may read. A virtual
// repository merges its members' in the package handlers' walk
// (virtualIndex); it never inherits one member's authority or source
// network permit from another. The records of packages whose files the
// patterns reject are kept: only their downloads are refused (R3).
func (h handler) memberIndex(
	r *http.Request,
	indexPath string,
	policy readPolicy,
) (channelIndex, error) {
	c := pkgroute.RequestFrom(r).Repo
	id, ok := condafmt.ParseMetadataPath(indexPath)
	if !ok {
		return channelIndex{}, kernel.ErrNotFound
	}
	if c.Class == repo.Virtual {
		return h.virtualIndex(r, id, policy)
	}
	if !h.k.CanFolder(r, path.Dir(indexPath), api.ActionRead) ||
		!h.k.Can(r, indexPath, api.ActionRead) {
		return channelIndex{}, apierror.New(
			http.StatusForbidden,
			"Conda index is not readable",
		)
	}
	if c.Class == repo.Remote {
		if h.settings.ShardsEnabled {
			index, err := h.remoteShardedIndex(r, id, policy)
			if !errors.Is(err, errNoShardIndex) {
				return index, err
			}
		}
		return h.remoteIndex(r, id, policy)
	}
	return h.localIndex(r, id)
}

func (h handler) localIndex(r *http.Request, id condafmt.MetadataPath) (channelIndex, error) {
	f, err := h.k.ResolveStoredItem(r, id.Path)
	if errors.Is(err, kernel.ErrNotFound) {
		return channelIndex{records: condafmt.EmptyRepodata(id.Subdir)}, nil
	}
	if err != nil {
		return channelIndex{}, fmt.Errorf("conda: local index: %w", err)
	}
	if f.Item.Folder {
		return channelIndex{}, kernel.ErrNotFound
	}
	rc, _, err := h.k.Blobs().Open(r.Context(), f.Item.Actual.SHA256)
	if err != nil {
		return channelIndex{}, fmt.Errorf("conda: index blob: %w", err)
	}
	defer func() { _ = rc.Close() }()
	body, err := io.ReadAll(io.LimitReader(rc, 64<<20+1))
	if err != nil {
		return channelIndex{}, fmt.Errorf("conda: index body: %w", err)
	}
	index, err := condafmt.DecodeRepodata(body, id)
	if err != nil {
		return channelIndex{}, fmt.Errorf("conda: local repodata: %w", err)
	}
	filtered, err := h.filterIndex(r.Context(), r, index, id, nil)
	// The channel's documents are calculated together, so the stored
	// repodata.json's time is theirs.
	return channelIndex{records: filtered, modified: f.Item.Modified}, err
}

func (h handler) remoteIndex(
	r *http.Request,
	id condafmt.MetadataPath,
	policy readPolicy,
) (channelIndex, error) {
	resource := source.Resource{
		Kind: source.ResourceKindMetadata, Path: id.Path, Method: http.MethodGet,
		PermissionFolder: true, PermissionPath: path.Dir(id.Path),
		PolicyPath: policy.path, BlackoutOnly: policy.download,
		Variant: "conda-repodata-v1", ExpectedContentTypes: []string{
			"application/json", "text/plain", "application/octet-stream",
		},
		ValidateMetadata: func(_ context.Context, body io.ReadSeeker, size int64,
			_ source.MetadataResponse,
		) error {
			if size <= 0 || size > 64<<20 {
				return source.ErrInvalidResult
			}
			data, err := io.ReadAll(io.LimitReader(body, 64<<20+1))
			if err != nil || int64(len(data)) != size {
				return source.ErrInvalidResult
			}
			if _, err := condafmt.DecodeRepodata(data, id); err != nil {
				return source.ErrInvalidResult
			}
			return nil
		},
	}
	result, err := h.k.Read(r, resource)
	if err != nil {
		return channelIndex{}, fmt.Errorf("conda: upstream repodata: %w", err)
	}
	body, err := h.resultBytes(r.Context(), result)
	if err != nil {
		return channelIndex{}, err
	}
	index, err := condafmt.DecodeRepodata(body, id)
	if err != nil {
		return channelIndex{}, fmt.Errorf("conda: upstream repodata content: %w", err)
	}
	filtered, err := h.filterIndex(r.Context(), r, index, id, result.CheckPermission)
	if err != nil {
		return channelIndex{}, err
	}
	return channelIndex{
		records: filtered, finish: result.Revalidate, modified: resultModified(result),
	}, nil
}

// resultModified is when a remote's copy of a document last changed: the
// cached item's time, or the time the upstream's stream carries.
func resultModified(result source.Result) time.Time {
	if result.Item != nil {
		return result.Item.Modified
	}
	if result.Stream != nil {
		return result.Stream.Modified
	}
	return time.Time{}
}

func (h handler) resultBytes(ctx context.Context, result source.Result) ([]byte, error) {
	if result.Stream != nil {
		defer func() { _ = result.Stream.Body.Close() }()
		body, err := io.ReadAll(io.LimitReader(result.Stream.Body, 64<<20+1))
		if err != nil {
			return nil, fmt.Errorf(
				"conda: read upstream repodata stream: %w",
				pkgroute.ServedReadFault(ctx, err),
			)
		}
		return body, nil
	}
	if result.Item == nil {
		return nil, source.ErrInvalidResult
	}
	rc, _, err := h.k.Blobs().Open(ctx, result.Item.Actual.SHA256)
	if err != nil {
		return nil, fmt.Errorf("conda: open cached repodata: %w", err)
	}
	defer func() { _ = rc.Close() }()
	body, err := io.ReadAll(io.LimitReader(rc, 64<<20+1))
	if err != nil {
		return nil, fmt.Errorf(
			"conda: read cached repodata: %w",
			pkgroute.ServedReadFault(ctx, err),
		)
	}
	return body, nil
}

// filterIndex keeps the records of the packages the caller may read. check,
// when set, is a remote result's permission-only check of each package. The
// download policy drops no record: 7.x lists a package whose file the
// patterns reject and refuses only its download (RTFACT-24940).
func (h handler) filterIndex(ctx context.Context, r *http.Request,
	index condafmt.Repodata, id condafmt.MetadataPath,
	check func(context.Context, string) error,
) (condafmt.Repodata, error) {
	filtered := condafmt.EmptyRepodata(id.Subdir)
	keep := func(filename string) (bool, error) {
		p := condafmt.IndexPath(id.Channel, id.Subdir, filename)
		if !h.k.Can(r, p, api.ActionRead) {
			return false, nil
		}
		if check != nil {
			if err := check(ctx, p); err != nil {
				if errors.Is(err, source.ErrDenied) {
					return false, nil
				}
				return false, fmt.Errorf("conda: source package authority: %w", err)
			}
		}
		return true, nil
	}
	for filename, rec := range index.Packages {
		allowed, err := keep(filename)
		if err != nil {
			return condafmt.Repodata{}, err
		}
		if allowed {
			filtered.Packages[filename] = rec
		}
	}
	for filename, rec := range index.PackagesConda {
		allowed, err := keep(filename)
		if err != nil {
			return condafmt.Repodata{}, err
		}
		if allowed {
			filtered.PackagesConda[filename] = rec
		}
	}
	return filtered, nil
}
