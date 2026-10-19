package server

import (
	"context"
	"errors"
	"fmt"
	"io"
	"net/http"
	"strings"
	"time"

	"github.com/weaveplatform/artifactweave/internal/auth"
	"github.com/weaveplatform/artifactweave/internal/format/generic"
	"github.com/weaveplatform/artifactweave/internal/server/apierror"
	"github.com/weaveplatform/artifactweave/internal/service/item"
	"github.com/weaveplatform/artifactweave/pkg/repo"
	"github.com/weaveplatform/artifactweave/pkg/store"
	"github.com/weaveplatform/artifactweave/pkg/types"
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

func (t *resolutionTrace) step(format string, args ...any) {
	if t == nil {
		return
	}
	t.steps = append(t.steps, time.Now().Format(traceTime)+" "+fmt.Sprintf(format, args...))
}

// step records a resolution step of a traced download. The storage API
// looks items up with no download at all, so d may be nil.
func (d *download) step(format string, args ...any) {
	if d != nil {
		d.trace.step(format, args...)
	}
}

// traceRepoPath names a path as Artifactory's trace does, "<repo>:<path>",
// with a remote repository's cache under its "-cache" key.
func traceRepoPath(c repo.Config, cached bool, p string) string {
	key := c.Key
	if cached && c.Class == repo.Remote {
		key += types.CacheSuffix
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
func (s artifactoryService) traceItem(w http.ResponseWriter, r *http.Request) {
	ctx := r.Context()
	st, err := s.content()
	if err != nil {
		writeErr(w, r, err)
		return
	}
	req, err := parseItemPath(r)
	if err != nil {
		writeErr(w, r, err)
		return
	}
	p, acc, err := s.itemCaller(ctx, st)
	if err != nil {
		writeErr(w, r, err)
		return
	}
	start := time.Now()
	t := &resolutionTrace{}
	t.step("Received request")
	if err := s.traceDownload(ctx, st, r.Method, req, p, acc, t); err != nil {
		var e *apierror.Error
		if !errors.As(err, &e) {
			// A failure the download would answer with a bare 500: its
			// cause goes to the access log, as writeErr sends it.
			noteCause(ctx, err)
			e = apierror.New(http.StatusInternalServerError, "internal error")
		}
		if e.Status == http.StatusNotFound {
			t.step("Responding with unfound resource")
		}
		t.step("Sending a response with the status '%d' and the message '%s'", e.Status, e.Message)
	} else {
		t.step("Request succeeded")
	}
	var b strings.Builder
	fmt.Fprintf(&b, "Request ID: %s\n", requestIDFrom(ctx))
	fmt.Fprintf(&b, "Repo Path ID: %s:%s\n", req.key, req.path)
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
func (s artifactoryService) traceDownload(
	ctx context.Context,
	st store.Store,
	method string,
	req itemRequest,
	p auth.Principal,
	acc access,
	t *resolutionTrace,
) error {
	c, cached, err := resolveRepo(ctx, st, req.key)
	if err != nil {
		return s.itemError(ctx, "resolve repository", err)
	}
	if !acc.folder(c, req.path, types.ActionRead) {
		return denied(p)
	}
	kind := string(c.Class)
	if cached {
		kind = "cache"
	}
	t.step("Retrieving info from %s repository '%s' type %s", kind, req.key, c.PackageType)
	if req.path == "" {
		if err := generic.CheckDownload(c, "", true); err != nil {
			return s.itemError(ctx, "download check", err)
		}
		t.step("Identified requested resource as a folder")
		return nil
	}
	dl := newDownload(req.path, req.folder, req.matrix(), s.items(st).Resolve)
	dl.trace = t
	f, err := lookup(ctx, st, c, cached, req.path, acc, dl, map[string]bool{})
	if errors.Is(err, store.ErrNotFound) {
		return s.traceContained(ctx, st, method, c, cached, req.path, p, acc, dl)
	}
	if err != nil {
		return s.itemError(ctx, "lookup item", err)
	}
	if !acc.item(f.repo, f.item.FullPath(), f.item.Folder, types.ActionRead) {
		return denied(p)
	}
	t.step(
		"Returning found resource from %s",
		traceRepoPath(f.repo, f.item.Cached, f.item.FullPath()),
	)
	ok, err := matchesMatrix(ctx, st, f.item, req)
	if err != nil {
		return s.internalError(ctx, "item properties", err)
	}
	if !ok {
		t.step("Request query properties don't match those that annotate the artifact")
		return itemNotFound()
	}
	if f.item.Folder {
		t.step("Identified requested resource as a folder")
		return nil
	}
	t.step("Identified requested resource as a file")
	traceResponding(method, t)
	return nil
}

// traceContained traces a path no item holds: an entry inside an archive
// ("<archive>!/<entry>") or a checksum file, which getItem serves from the
// file they name, or else a 404.
func (s artifactoryService) traceContained(
	ctx context.Context,
	st store.Store,
	method string,
	c repo.Config,
	cached bool,
	p string,
	caller auth.Principal,
	acc access,
	dl *download,
) error {
	if archive, entry, ok := strings.Cut(p, "!/"); ok {
		dl.step("Requested resource is contained within an archive")
		a, err := lookup(ctx, st, c, cached, archive, acc, dl, map[string]bool{})
		if err != nil {
			return itemNotFound()
		}
		if !acc.file(a.repo, a.item.FullPath(), types.ActionRead) {
			return denied(caller)
		}
		if a.item.Folder {
			return itemNotFound()
		}
		// The entry is looked for but not read: a trace serves nothing.
		err = s.items(st).ArchiveEntry(ctx, a.item, entry,
			func(int64, io.Reader) error { return nil })
		if errors.Is(err, item.ErrNotFound) {
			return itemNotFound()
		}
		if err != nil {
			return fmt.Errorf("archive entry: %w", err)
		}
		dl.step("Returning found resource from %s",
			traceRepoPath(a.repo, a.item.Cached, a.item.FullPath()))
		traceResponding(method, dl.trace)
		return nil
	}
	artifact, alg, ok := item.Sidecar(p)
	if !ok {
		return itemNotFound()
	}
	dl.step("Request is for a checksum")
	f, err := lookup(ctx, st, c, cached, artifact, acc, dl, map[string]bool{})
	// A file the caller may not read has no visible checksum either.
	if err != nil || f.item.Folder || alg.Of(f.item.Actual) == "" ||
		!acc.file(f.repo, f.item.FullPath(), types.ActionRead) {
		return itemNotFound()
	}
	dl.step(
		"Returning found resource from %s",
		traceRepoPath(f.repo, f.item.Cached, f.item.FullPath()),
	)
	dl.step("Responding to checksum request")
	return nil
}

// traceResponding records how a found file would be answered.
func traceResponding(method string, t *resolutionTrace) {
	if method == http.MethodHead {
		t.step("Request was of type HEAD - responding with no content")
		return
	}
	t.step("Responding with found resource")
}
