package itemkit

import (
	"context"
	"errors"
	"fmt"
	"io"
	"net/http"
	"strings"
	"time"

	accessapi "github.com/weaveplatform/artifactweave/internal/access/api"
	"github.com/weaveplatform/artifactweave/internal/artifacts/adapters/http/pkgroute"
	"github.com/weaveplatform/artifactweave/internal/artifacts/api/repo"
	"github.com/weaveplatform/artifactweave/internal/artifacts/core/item"
	"github.com/weaveplatform/artifactweave/internal/artifacts/core/model"
	"github.com/weaveplatform/artifactweave/internal/artifacts/core/remote/source"
	"github.com/weaveplatform/artifactweave/internal/artifacts/ecosystem/generic"
	"github.com/weaveplatform/artifactweave/internal/kernel"
	"github.com/weaveplatform/artifactweave/internal/platform/httpkit"
	"github.com/weaveplatform/artifactweave/internal/platform/httpkit/apierror"
)

// traceTime is the time format of Artifactory's trace, Joda's
// ISODateTimeFormat.dateTime(): milliseconds and the zone offset, Z in UTC.
const traceTime = "2006-01-02T15:04:05.000Z07:00"

// resolutionTrace collects the steps of one download's resolution for
// GET {repo}/{path}?trace. A nil trace records nothing, so the download
// path reports its steps whether or not it is traced.
type resolutionTrace struct {
	steps []string
}

func (t *resolutionTrace) stepf(format string, args ...any) {
	if t == nil {
		return
	}
	t.steps = append(t.steps, time.Now().Format(traceTime)+" "+fmt.Sprintf(format, args...))
}

// stepf records a resolution step of a traced download. The storage API
// looks items up with no download at all, so d may be nil.
func (d *Download) stepf(format string, args ...any) {
	if d != nil {
		d.trace.stepf(format, args...)
	}
}

// traceRepoPath names a path as Artifactory's trace does, "<repo>:<path>",
// with a remote repository's cache under its "-cache" key.
func traceRepoPath(c repo.Config, cached bool, p string) string {
	key := c.Key
	if cached && c.Class == repo.Remote {
		key += model.CacheSuffix
	}
	return key + ":" + p
}

// traceItem answers GET or HEAD {repo}/{path}?trace. In place of the content
// it writes the request and the steps of its resolution as text, in the
// shape of Artifactory's TraceLoggingResponse: header lines, then "Steps:"
// and one timestamped line per step. It answers 200 whatever the download
// would answer, as that response does, and its last step is the download's
// own answer. Nothing is served, redirected or counted as a download.
// Artifactory's header also names the servlet thread; Go serves requests
// without named threads, so that line is left out.
func (s *Kit) traceItem(w http.ResponseWriter, r *http.Request) {
	ctx := r.Context()
	st, err := s.Content()
	if err != nil {
		httpkit.WriteErr(w, r, err)
		return
	}
	req, err := ParseItemPath(r)
	if err != nil {
		httpkit.WriteErr(w, r, err)
		return
	}
	p, acc, err := s.ItemCaller(ctx, st)
	if err != nil {
		httpkit.WriteErr(w, r, err)
		return
	}
	start := time.Now()
	t := &resolutionTrace{}
	t.stepf("Received request")
	if err := s.traceDownload(ctx, st, r, req, p, acc, t); err != nil {
		var e *apierror.Error
		if !errors.As(err, &e) {
			// A failure the download would answer with a bare 500: its
			// cause goes to the access log, as httpkit.WriteErr sends it.
			httpkit.NoteCause(ctx, err)
			e = apierror.New(http.StatusInternalServerError, "internal error")
		}
		if e.Status == http.StatusNotFound {
			t.stepf("Responding with unfound resource")
		}
		t.stepf("Sending a response with the status '%d' and the message '%s'", e.Status, e.Message)
	} else {
		t.stepf("Request succeeded")
	}
	var b strings.Builder
	fmt.Fprintf(&b, "Request ID: %s\n", httpkit.RequestIDFrom(ctx))
	fmt.Fprintf(&b, "Repo Path ID: %s:%s\n", req.Key, req.Path)
	fmt.Fprintf(&b, "Method Name: %s\n", r.Method)
	fmt.Fprintf(&b, "User: %s\n", p.Name)
	fmt.Fprintf(&b, "Time: %s\n", start.Format(traceTime))
	b.WriteString("Steps: \n")
	for _, line := range t.steps {
		b.WriteString(line)
		b.WriteByte('\n')
	}
	w.Header().Set("Content-Type", "text/plain")
	w.WriteHeader(http.StatusOK)
	if r.Method != http.MethodHead {
		_, _ = io.WriteString(w, b.String())
	}
}

// traceDownload resolves req as getItem does, recording each step in t, and
// returns the error getItem would answer, or nil where it would serve the
// item. It follows getItem's checks in getItem's order, so a caller learns
// from the trace only what the download itself would tell it: a repository
// is described only once the caller may read it.
func (s *Kit) traceDownload(
	ctx context.Context,
	st Store,
	r *http.Request,
	req ItemRequest,
	p kernel.Principal,
	acc Access,
	t *resolutionTrace,
) error {
	method := r.Method
	c, cached, err := ResolveRepo(ctx, st, req.Key)
	if err != nil {
		return s.ItemError(ctx, "resolve repository", err)
	}
	if !acc.Folder(c, req.Path, accessapi.ActionRead) {
		return httpkit.Denied(p)
	}
	kind := string(c.Class)
	if cached {
		kind = "cache"
	}
	t.stepf("Retrieving info from %s repository '%s' type %s", kind, req.Key, c.PackageType)
	if req.Path == "" {
		if err := generic.CheckDownload(c, "", true); err != nil {
			return s.ItemError(ctx, "download check", err)
		}
		t.stepf("Identified requested resource as a folder")
		return nil
	}
	if c.Class == repo.Remote && c.PackageType == repo.Generic && !cached && !req.IsFolder &&
		s.RemoteSources != nil &&
		!strings.Contains(req.Path, "!/") {
		_, _, sidecar := item.Sidecar(req.Path)
		cachedItem, lookupErr := st.Items().Get(ctx, c.Key, true, req.Path)
		if !sidecar && (lookupErr != nil || !cachedItem.Folder) {
			t.stepf("Resolving remote source with current read authorization")
			result, readErr := s.RemoteSources.Read(ctx, source.Request{
				Repo:     c,
				Subject:  accessapi.SubjectOf(p),
				Access:   acc.Decider,
				Resource: RemoteResource(r, req.Path),
				Matrix:   req.matrix(),
				Project:  c.ProjectKey,
			})
			if readErr != nil {
				return s.ItemError(ctx, "trace source", pkgroute.SourceError(readErr, p.Anonymous))
			}
			if result.Stream != nil {
				_ = result.Stream.Body.Close()
				if !req.matrix().Matches(nil) {
					return ItemNotFound()
				}
				t.stepf("Returning validated transient resource")
			} else {
				matches, matchErr := matchesMatrix(ctx, st, *result.Item, req)
				if matchErr != nil {
					return s.InternalError(ctx, "trace properties", matchErr)
				}
				if !matches {
					return ItemNotFound()
				}
				t.stepf("Returning found resource from %s", traceRepoPath(c, true, req.Path))
			}
			traceResponding(method, t)
			return nil
		}
	}
	dl := newDownload(req.Path, req.IsFolder, req.matrix(), s.Items(st).Resolve)
	dl.trace = t
	f, err := Lookup(ctx, st, c, cached, req.Path, acc, dl, map[string]bool{})
	if errors.Is(err, kernel.ErrNotFound) {
		return s.traceContained(ctx, st, method, c, cached, req.Path, p, acc, dl)
	}
	if err != nil {
		return s.ItemError(ctx, "lookup item", err)
	}
	if !acc.Item(f.Repo, f.Item.FullPath(), f.Item.Folder, accessapi.ActionRead) {
		return httpkit.Denied(p)
	}
	t.stepf(
		"Returning found resource from %s",
		traceRepoPath(f.Repo, f.Item.Cached, f.Item.FullPath()),
	)
	ok, err := matchesMatrix(ctx, st, f.Item, req)
	if err != nil {
		return s.InternalError(ctx, "item properties", err)
	}
	if !ok {
		t.stepf("Request query properties don't match those that annotate the artifact")
		return ItemNotFound()
	}
	if f.Item.Folder {
		t.stepf("Identified requested resource as a folder")
		return nil
	}
	t.stepf("Identified requested resource as a file")
	traceResponding(method, t)
	return nil
}

// traceContained traces a path no item holds: an entry inside an archive
// ("<archive>!/<entry>") or a checksum file, which getItem serves from the
// file they name, or else a 404.
func (s *Kit) traceContained(
	ctx context.Context,
	st Store,
	method string,
	c repo.Config,
	cached bool,
	p string,
	caller kernel.Principal,
	acc Access,
	dl *Download,
) error {
	if archive, entry, ok := strings.Cut(p, "!/"); ok {
		dl.stepf("Requested resource is contained within an archive")
		a, err := Lookup(ctx, st, c, cached, archive, acc, dl, map[string]bool{})
		if err != nil {
			return ItemNotFound()
		}
		if !acc.File(a.Repo, a.Item.FullPath(), accessapi.ActionRead) {
			return httpkit.Denied(caller)
		}
		if a.Item.Folder {
			return ItemNotFound()
		}
		// The entry is looked for but not read: a trace serves nothing.
		err = s.Items(st).ArchiveEntry(ctx, a.Item, entry,
			func(int64, io.Reader) error { return nil })
		if errors.Is(err, item.ErrNotFound) {
			return ItemNotFound()
		}
		if err != nil {
			return fmt.Errorf("archive entry: %w", err)
		}
		dl.stepf("Returning found resource from %s",
			traceRepoPath(a.Repo, a.Item.Cached, a.Item.FullPath()))
		traceResponding(method, dl.trace)
		return nil
	}
	artifact, alg, ok := item.Sidecar(p)
	if !ok {
		return ItemNotFound()
	}
	dl.stepf("Request is for a checksum")
	f, err := Lookup(ctx, st, c, cached, artifact, acc, dl, map[string]bool{})
	// A file the caller may not read has no visible checksum either.
	if err != nil || f.Item.Folder || alg.Of(f.Item.Actual) == "" ||
		!acc.File(f.Repo, f.Item.FullPath(), accessapi.ActionRead) {
		return ItemNotFound()
	}
	dl.stepf(
		"Returning found resource from %s",
		traceRepoPath(f.Repo, f.Item.Cached, f.Item.FullPath()),
	)
	dl.stepf("Responding to checksum request")
	return nil
}

// traceResponding records how a found file would be answered.
func traceResponding(method string, t *resolutionTrace) {
	if method == http.MethodHead {
		t.stepf("Request was of type HEAD - responding with no content")
		return
	}
	t.stepf("Responding with found resource")
}
