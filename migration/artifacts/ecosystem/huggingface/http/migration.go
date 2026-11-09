package huggingfacehttp

import (
	"bufio"
	"bytes"
	"context"
	"errors"
	"fmt"
	"io"
	"net/http"
	"slices"
	"strings"

	"github.com/weaveplatform/artifactweave/internal/server/pkgroute"
	"github.com/weaveplatform/artifactweave/migration/artifacts/api/repo"
	format "github.com/weaveplatform/artifactweave/migration/artifacts/ecosystem/huggingface"
	"github.com/weaveplatform/artifactweave/migration/kernel"
	"github.com/weaveplatform/artifactweave/migration/platform/blob"
	"github.com/weaveplatform/artifactweave/migration/platform/httpkit/apierror"
	"github.com/weaveplatform/artifactweave/pkg/store"
	"github.com/weaveplatform/artifactweave/pkg/types"
)

const (
	legacyLayout                  = "jfrog-7.77-timestamp-v1"
	legacyRevisionProperty        = "huggingfaceml.generated.revision.sha1"
	migrationSourceProperty       = "artifactweave.huggingface.migration.source"
	maxMigrationItems             = 1024
	maxMigrationBytes       int64 = 16 << 30
)

// The public legacy layout specifies paths and the revision property, but no
// stable metadata JSON schema or upgrade request. This is an artifactweave
// operation, deliberately separate from the JFrog and Hub wire contracts.
type migrationRequest struct {
	Layout    string `json:"layout"`
	Kind      string `json:"kind"`
	Owner     string `json:"owner"`
	Name      string `json:"name"`
	SourceRef string `json:"source_ref"`
	Timestamp string `json:"timestamp"`
	Revision  string `json:"revision"`
	Ref       string `json:"ref,omitempty"`
	DryRun    bool   `json:"dry_run"`
}

type migrationStatus struct {
	State          string `json:"state"`
	Source         string `json:"source"`
	Destination    string `json:"destination"`
	Revision       string `json:"revision"`
	Files          int    `json:"files"`
	Bytes          int64  `json:"bytes"`
	VerifiedFiles  int    `json:"verified_files"`
	PublishedFiles int    `json:"published_files"`
	Ref            string `json:"ref,omitempty"`
}

type migrationFile struct {
	item  types.Item
	path  string
	props types.Properties
}

func (m migrationRequest) paths() (format.Identity, string, string, bool) {
	id, ok := format.ParseIdentity(m.Kind, m.Owner, m.Name)
	if !ok || m.Layout != legacyLayout || !format.Revision(m.Revision) ||
		!format.RefName(m.SourceRef) || !format.FilePath(m.Timestamp) ||
		strings.Contains(m.Timestamp, "/") || strings.HasPrefix(m.Timestamp, ".") {
		return id, "", "", false
	}
	ref := ""
	if m.Ref != "" {
		ref, ok = format.RefPath(id, m.Ref)
		if !ok {
			return id, "", "", false
		}
	}
	return id, m.Kind + "/" + id.RepoID() + "/" + m.SourceRef + "/" + m.Timestamp, ref, true
}

func (h handler) migrateLegacy(w http.ResponseWriter, r *http.Request) {
	p, _ := kernel.FromContext(r.Context())
	if !p.Admin {
		h.k.Error(
			w,
			r,
			apierror.New(http.StatusForbidden, "legacy migration requires an administrator"),
		)
		return
	}
	c := pkgroute.RequestFrom(r).Repo
	if c.Class != repo.Local && c.Class != repo.Federated {
		h.k.Error(
			w,
			r,
			apierror.New(
				http.StatusConflict,
				"legacy migration requires a local or federated repository",
			),
		)
		return
	}
	var request migrationRequest
	if err := boundedJSON(r, 8192, &request); err != nil || r.URL.RawQuery != "" {
		h.k.Error(w, r, apierror.New(http.StatusBadRequest, "invalid migration JSON or query"))
		return
	}
	id, source, ref, ok := request.paths()
	if !ok {
		h.k.Error(w, r, apierror.New(http.StatusBadRequest, "invalid legacy migration mapping"))
		return
	}
	status := migrationStatus{
		Source: source, Destination: id.Prefix() + "/snapshots/" + request.Revision,
		Revision: request.Revision, Ref: request.Ref,
	}
	if err := h.migrationAccess(r, nil, status, ref); err != nil {
		h.k.Error(w, r, err)
		return
	}
	err := h.k.DB().Tx(r.Context(), func(ctx context.Context) error {
		r := r.WithContext(format.DeferReindex(ctx))
		files, err := h.legacyFiles(r, request, &status)
		if err != nil {
			return err
		}
		if err = h.migrationPolicy(r, id, files, status, ref); err != nil {
			return err
		}
		complete, err := h.migrationDestination(r, files, status)
		if err != nil {
			return err
		}
		for _, file := range files {
			if err = h.verifyLegacyFile(ctx, file.item); err != nil {
				return err
			}
			status.VerifiedFiles++
		}
		if err = h.recheckMigration(r, request, id, files, status, ref); err != nil {
			return err
		}
		if complete {
			status.State, status.PublishedFiles = "complete", len(files)
			return nil
		}
		if err = h.migrationRefAvailable(r, ref, request.Revision); err != nil {
			return err
		}
		status.State = "ready"
		if request.DryRun {
			return nil
		}
		if err = h.publishMigration(r, id, files, status, ref); err != nil {
			return err
		}
		status.State, status.PublishedFiles = "complete", len(files)
		return nil
	})
	if err != nil {
		h.k.Error(w, r, err)
		return
	}
	h.writeJSON(w, status)
}

func (h handler) migrationItems(
	r *http.Request,
	prefix string,
	actions types.Actions,
) ([]types.Item, error) {
	if !h.k.CanFolder(r, prefix, actions) {
		return nil, migrationAccessDenied()
	}
	var result []types.Item
	page := store.Page{Limit: maxMigrationItems}
	for {
		items, next, err := h.k.DB().Items().Descendants(r.Context(),
			pkgroute.RequestFrom(r).Repo.Key, false, prefix, page)
		if err != nil {
			return nil, fmt.Errorf("huggingface: list migration items: %w", err)
		}
		for _, it := range items {
			allowed := h.k.Can(r, it.FullPath(), actions)
			if it.Folder {
				allowed = h.k.CanFolder(r, it.FullPath(), actions)
			}
			if !allowed {
				return nil, migrationAccessDenied()
			}
		}
		result = append(result, items...)
		if len(result) > maxMigrationItems || len(result) == maxMigrationItems && next != "" {
			return nil, apierror.New(
				http.StatusRequestEntityTooLarge,
				"legacy revision exceeds 1024 items",
			)
		}
		if next == "" {
			return result, nil
		}
		page.After = next
	}
}

func (h handler) legacyFiles(
	r *http.Request,
	request migrationRequest,
	status *migrationStatus,
) ([]migrationFile, error) {
	items, err := h.migrationItems(r, status.Source, types.ActionRead)
	if err != nil {
		return nil, err
	}
	metadata := ".jfrog_huggingface_model_info.json"
	if request.Kind == "datasets" {
		metadata = ".jfrog_huggingface_dataset_info.json"
	}
	var files []migrationFile
	var identityVerified bool
	for _, it := range items {
		if it.Folder {
			continue
		}
		locked, err := h.k.DB().Items().GetByID(r.Context(), it.ID)
		if err != nil {
			return nil, fmt.Errorf("huggingface: lock legacy source: %w", err)
		}
		if locked != it {
			return nil, apierror.New(http.StatusConflict, "legacy source changed while planning")
		}
		name := strings.TrimPrefix(it.FullPath(), status.Source+"/")
		props, err := h.k.DB().Items().Properties(r.Context(), it.ID)
		if err != nil {
			return nil, fmt.Errorf("huggingface: migration properties: %w", err)
		}
		if name == metadata {
			identityVerified = slices.Equal(
				props[legacyRevisionProperty],
				[]string{request.Revision},
			)
			continue
		}
		if _, reserved := props[migrationSourceProperty]; reserved {
			return nil, apierror.New(
				http.StatusConflict,
				"legacy source already contains migration provenance",
			)
		}
		if !h.k.Can(r, status.Destination+"/"+name, types.ActionRead|types.ActionWrite) {
			return nil, migrationAccessDenied()
		}
		if !format.FilePath(name) || strings.HasPrefix(name, ".jfrog") ||
			!formatSHA256(
				it.Actual.SHA256,
			) || it.Size < 0 || it.Size > maxMigrationBytes-status.Bytes {
			return nil, apierror.New(
				http.StatusConflict,
				"unsupported legacy file or revision larger than 16 GiB",
			)
		}
		status.Bytes += it.Size
		files = append(
			files,
			migrationFile{item: it, path: status.Destination + "/" + name, props: props},
		)
	}
	if !identityVerified || len(files) == 0 {
		return nil, apierror.New(
			http.StatusConflict,
			"legacy revision property is missing, differs, or revision has no files",
		)
	}
	status.Files = len(files)
	return files, nil
}

func (h handler) verifyLegacyFile(ctx context.Context, it types.Item) error {
	reader, info, err := h.k.Blobs().Open(ctx, it.Actual.SHA256)
	if err != nil {
		return fmt.Errorf("huggingface: open legacy content: %w", err)
	}
	defer reader.Close()
	checked := bufio.NewReader(&migrationReader{ctx: ctx, reader: reader})
	const lfsPointer = "version https://git-lfs.github.com/spec/v1\n"
	prefix, err := checked.Peek(len(lfsPointer))
	if err != nil && !errors.Is(err, io.EOF) {
		return fmt.Errorf("huggingface: inspect legacy content: %w", err)
	}
	if bytes.Equal(prefix, []byte(lfsPointer)) {
		return apierror.New(http.StatusConflict, "legacy LFS pointer requires materialized content")
	}
	hasher := blob.NewHasher()
	n, err := io.Copy(hasher, io.LimitReader(checked, it.Size+1))
	want := blob.Digests{
		SHA256: it.Actual.SHA256,
		SHA1:   it.Actual.SHA1,
		SHA512: it.Actual.SHA512,
		MD5:    it.Actual.MD5,
	}
	if err != nil {
		return fmt.Errorf("huggingface: verify legacy content: %w", err)
	}
	if n != it.Size || info.Size != it.Size || blob.Verify(want, hasher.Sum()) != nil {
		return apierror.New(http.StatusConflict, "legacy content size or checksum differs")
	}
	return nil
}

type migrationReader struct {
	ctx    context.Context
	reader io.Reader
}

func (r *migrationReader) Read(p []byte) (int, error) {
	if err := r.ctx.Err(); err != nil {
		return 0, fmt.Errorf("huggingface: migration cancelled: %w", err)
	}
	n, err := r.reader.Read(p)
	if err != nil && !errors.Is(err, io.EOF) {
		return n, fmt.Errorf("huggingface: migration read: %w", err)
	}
	if errors.Is(err, io.EOF) {
		return n, io.EOF
	}
	return n, nil
}

func (h handler) recheckMigration(
	r *http.Request,
	request migrationRequest,
	id format.Identity,
	files []migrationFile,
	status migrationStatus,
	ref string,
) error {
	fresh := migrationStatus{Source: status.Source, Destination: status.Destination}
	current, err := h.legacyFiles(r, request, &fresh)
	if err != nil {
		return err
	}
	if !slices.EqualFunc(files, current, func(a, b migrationFile) bool {
		return a.item == b.item && a.path == b.path && a.props.Equal(b.props)
	}) {
		return apierror.New(http.StatusConflict, "legacy source changed while verifying")
	}
	return h.migrationPolicy(r, id, files, status, ref)
}
