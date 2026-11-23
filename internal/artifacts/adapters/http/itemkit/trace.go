package itemkit

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/http"
	"regexp"
	"strings"
	"time"

	accessapi "github.com/weaveplatform/artifactweave/internal/access/api"
	"github.com/weaveplatform/artifactweave/internal/artifacts/api/repo"
	"github.com/weaveplatform/artifactweave/internal/artifacts/core/item"
	"github.com/weaveplatform/artifactweave/internal/artifacts/core/model"
	"github.com/weaveplatform/artifactweave/internal/artifacts/core/remote/source"
	"github.com/weaveplatform/artifactweave/internal/artifacts/core/virtual"
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
	// unfound is whether a direct request was refused by the download
	// policy (denied), which 7.161.26 answers as an unfound resource
	// whatever its status, Maven's 409 included.
	unfound bool
}

func (t *resolutionTrace) stepf(format string, args ...any) {
	if t == nil {
		return
	}
	t.steps = append(t.steps, time.Now().Format(traceTime)+" "+fmt.Sprintf(format, args...))
}

// denied records the download policy's refusal err holds, if any, of a
// direct request, as RealRepoBase.createUnfoundRepoResource (224-227) logs
// it when checkDownloadIsAllowed refuses.
func (t *resolutionTrace) denied(err error) {
	if refusal := policyRefusal(err); refusal != nil && t != nil {
		t.stepf("Download denied (%s) - returning un-found resource", refusal.Message)
		t.unfound = true
	}
}

// stepf records a resolution step of a traced download. The storage API
// looks items up with no download at all, so d may be nil.
func (d *Download) stepf(format string, args ...any) {
	if d != nil {
		d.trace.stepf(format, args...)
	}
}

// denied records c's refusal err of a direct request. A virtual
// repository's own patterns leave it no member to search, which
// getInfoFromSearchableRepositories logs (7.161.26, 166-167), as
// lookupMembers does when no member is left; any other repository's
// refusal is its download policy's (resolutionTrace.denied). 7.x traces
// no folder request: RepoFilter.doRepoRequest lists the folder before it
// reads ?trace, so a folder's refusal keeps this server's own step.
func (d *Download) denied(c repo.Config, err error) {
	switch {
	case d == nil:
	case d.isFolder:
		d.stepf("Request is forbidden by %s", c.Key)
	case c.Class == repo.Virtual:
		d.stepf("Unable to find aggregated repositories to search within - returning " +
			"unfound resource")
	default:
		d.trace.denied(err)
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

// traceHeaders are the request headers 7.161.26's trace prints, in its
// order (RepoFilter.TRACE_WHITELISTED_HEADERS): what a proxy in front of
// the server says of the request, which sets the URLs a response names.
var traceHeaders = []string{
	"X-Orig-Client-Uri", "X-JFrog-Override-Base-Url", "X-Forwarded-Host", "X-Forwarded-Proto",
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
		// The download's answer, hidden as the download would hide it.
		err = httpkit.Hidden(ctx, err)
		var e *apierror.Error
		if !errors.As(err, &e) {
			// A failure the download would answer with a bare 500: its
			// cause goes to the access log, as httpkit.WriteErr sends it.
			httpkit.NoteCause(ctx, err)
			e = apierror.New(http.StatusInternalServerError, "internal error")
		}
		// DownloadServiceImpl.respond logs this for every resource that is
		// not found (7.161.26, 748-749): a miss, and a refusal by the
		// download policy whatever its status.
		if e.Status == http.StatusNotFound || t.unfound {
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
	writeTraceHeaders(&b, r)
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

// traceLineBreaks is TraceLoggingResponse.sanitizeHeaderValue's pattern.
var traceLineBreaks = regexp.MustCompile(`[\r\n]+`)

// writeTraceHeaders writes the "Headers:" block of 7.161.26's trace
// (TraceLoggingResponse.sendResponse): each of traceHeaders the request
// carries with a value that is not blank, its first value only, as
// RepoFilter.collectWhitelistedHeaders reads it, with every run of CR and LF
// made one space so a value cannot add a line. With none, the block is left
// out.
func writeTraceHeaders(b *strings.Builder, r *http.Request) {
	head := "Headers:\n"
	for _, name := range traceHeaders {
		value := r.Header.Get(name)
		if strings.TrimSpace(value) == "" {
			continue
		}
		b.WriteString(head)
		head = ""
		fmt.Fprintf(b, "  %s: %s\n", name, traceLineBreaks.ReplaceAllString(value, " "))
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
	// The format overlays decide first, as in GetItem (#1595).
	if handled, err := s.traceOverlays(r, st, c, cached, req, p, acc, t); handled {
		return err
	}
	if ok, err := acc.Admits(ctx, st.Repos(), c, req.Path); err != nil {
		return s.InternalError(ctx, "read gate", err)
	} else if !ok {
		return refused(c, req, p)
	}
	traceRetrieving(t, c, cached, req)
	if req.Path == "" {
		if err := generic.CheckDownload(c, "", true); err != nil {
			return s.ItemError(ctx, "download check", err)
		}
		t.stepf("Identified requested resource as a folder")
		return nil
	}
	// A remote file is read from the upstream where the download reads it,
	// before the cache lookup (GetItem's remote read) or once it has not
	// found the file (getColdRemoteItem), on a remote of any type: 7.161.26's
	// RepoFilter answers ?trace by running the download itself
	// (doGetOrHeadRequest wraps its response in a TraceLoggingResponse), so
	// the trace finds what the download finds.
	alias, err := aliasRemoteMetadata(ctx, st, c, cached, req.Path)
	if err != nil {
		return s.InternalError(ctx, "cached document", err)
	}
	if (remoteDownloadType(c, cached) || alias || fileChecksumRoute(c, cached, req.Path)) &&
		!req.IsFolder && s.RemoteSources != nil {
		if handled, err := s.traceRemoteItem(r, c, req, p, acc, t); handled {
			return err
		}
	}
	items := s.Items(st).ReadSession()
	dl := newDownload(req.Path, req.IsFolder, req.matrix(), items.Resolve)
	dl.selectVirtual = items.SelectVirtual
	dl.translate = ReadTranslator(items, c)
	dl.trace = t
	dl.upstream = s.walkStep(r, req, p, acc)
	f, err := Lookup(ctx, st, c, cached, req.Path, acc, dl, virtual.Resolve)
	if errors.Is(err, kernel.ErrNotFound) {
		// getUnfoundItem's check of Read on the file, before anything else.
		if fileRequest(c, req) && !acc.File(c, resourcePath(req.Path), accessapi.ActionRead) {
			return refused(c, req, p)
		}
		if s.coldRemoteRead(ctx, st, c, cached, req, acc, dl) {
			if handled, err := s.traceRemoteItem(r, c, req, p, acc, t); handled {
				return err
			}
		}
		return s.traceContained(r, st, c, cached, req, p, acc, dl, err)
	}
	if err != nil {
		return s.ItemError(ctx, "lookup item", lookupRefusal(c, req, err))
	}
	if f.remote != nil {
		return s.traceRemoteRead(r, f.Repo, memberRequest(req, f.Repo), p, *f.remote, t)
	}
	if !acc.Item(f.Repo, f.Item.FullPath(), f.Item.Folder, accessapi.ActionRead) {
		return refused(c, req, p)
	}
	ok, err := traceMatrix(ctx, st, c, f.Item, req, t)
	if err != nil {
		return s.InternalError(ctx, "item properties", err)
	}
	if !ok {
		return mismatched(c, req)
	}
	t.stepf(
		"Returning found resource from %s",
		traceRepoPath(f.Repo, f.Item.Cached, f.Item.FullPath()),
	)
	if f.Item.Folder {
		t.stepf("Identified requested resource as a folder")
		return nil
	}
	t.stepf("Identified requested resource as a file")
	traceResponding(method, t)
	return nil
}

// traceMatrix is matchesMatrix for a traced download from c, which records
// the match as DbStoringRepoMixin.getInfo logs it (7.161.26, 516-530), and
// as the walk through a virtual repository does (lookupMember): a file
// whose properties conflict with the request's, which the download answers
// unfound, and one that lacks a key the request asks a value of, which it
// still serves (PropertiesImpl.matchQuery's NO_MATCH). Through a virtual
// repository the walk has recorded its members' matches already.
func traceMatrix(
	ctx context.Context,
	st Store,
	c repo.Config,
	it model.Item,
	req ItemRequest,
	t *resolutionTrace,
) (bool, error) {
	if c.Class == repo.Virtual {
		return matchesMatrix(ctx, st, it, req)
	}
	if len(req.Props) == 0 {
		return true, nil
	}
	props, err := st.Items().Properties(ctx, it.ID)
	if err != nil {
		return false, fmt.Errorf("properties of %s: %w", it.FullPath(), err)
	}
	if !it.Folder {
		t.stepf("Request includes query properties")
	}
	m := req.matrix()
	switch {
	case !m.Matches(props):
		t.stepf("Request query properties conflict with those that annotate the artifact - " +
			"returning unfound resource")
		return false, nil
	case !carriesEveryKey(m, props):
		t.stepf("Request query properties don't match those that annotate the artifact")
	}
	return true, nil
}

// traceRemoteItem traces the read getRemoteItem makes of req
// (readRemoteItem) and returns the error getRemoteItem would answer from
// its result, or nil where it would serve a file or a checksum, without
// serving or counting anything. It reports false where getRemoteItem
// leaves the path to the cache lookup.
func (s *Kit) traceRemoteItem(
	r *http.Request,
	c repo.Config,
	req ItemRequest,
	p kernel.Principal,
	acc Access,
	t *resolutionTrace,
) (bool, error) {
	read, ok := s.readRemoteItem(r, c, req, p, acc, t)
	if !ok {
		return false, nil
	}
	return true, s.traceRemoteRead(r, c, req, p, read, t)
}

// traceRemoteRead traces read, the remote repository c's read of req, as
// serveRemoteRead would answer it, and returns the error it would answer,
// or nil where it would serve a file or a checksum, without serving or
// counting anything. A walk through a virtual repository that found the
// file in c traces it so too.
func (s *Kit) traceRemoteRead(
	r *http.Request,
	c repo.Config,
	req ItemRequest,
	p kernel.Principal,
	read remoteRead,
	t *resolutionTrace,
) error {
	if read.err != nil {
		return read.err
	}
	ctx := r.Context()
	result, alg := read.result, item.Algorithm(read.sidecar)
	var err error
	switch {
	case result.Stream != nil && read.infoSum != nil:
		_, err = s.infoChecksum(ctx, result, c, *read.infoSum, read.resource, read.sidecar,
			p.Anonymous)
	case result.Stream != nil:
		if err = remoteDeliveryErr(ctx, result, p.Anonymous); err == nil {
			_ = result.Stream.Body.Close()
			// ServeRemoteStream answers a HEAD for a checksum it cannot
			// compute without a body.
			if read.sidecar != "" && r.Method != http.MethodHead &&
				remoteStreamChecksum(c, result.Stream, alg) == "" {
				err = ItemNotFound()
			}
		}
	default:
		err = remoteDeliveryErr(ctx, result, p.Anonymous)
		if err == nil && read.sidecar != "" && item.ServedChecksum(c, *result.Item, alg) == "" {
			err = ItemNotFound()
		}
	}
	if err != nil {
		return err
	}
	if result.Stream != nil {
		t.stepf("Returning validated transient resource")
	} else {
		t.stepf("Returning found resource from %s", traceRepoPath(c, true, result.Item.FullPath()))
	}
	if read.sidecar != "" {
		t.stepf("Responding to checksum request")
		return nil
	}
	traceResponding(r.Method, t)
	return nil
}

// traceContained traces a path no item holds: an entry inside an archive
// ("<archive>!/<entry>") or a checksum file, which getItem serves from the
// file they name, or else the miss, which is miss, the lookup's error. It
// answers each as getUnfoundItem and getSidecar do.
func (s *Kit) traceContained(
	r *http.Request,
	st Store,
	c repo.Config,
	cached bool,
	req ItemRequest,
	caller kernel.Principal,
	acc Access,
	dl *Download,
	miss error,
) error {
	ctx, method, p := r.Context(), r.Method, req.Path
	if archive, entry, ok := strings.Cut(p, "!/"); ok {
		dl.stepf("Requested resource is contained within an archive")
		dl.upstream = s.archiveStep(r, req, archive, caller, acc)
		a, err := Lookup(ctx, st, c, cached, archive, acc, dl, virtual.Resolve)
		if errors.Is(err, kernel.ErrNotFound) {
			fetched, read, answer := s.remoteArchive(r, c, cached, req, archive, entry, caller, acc,
				dl.trace)
			switch {
			case !read:
				return unfound(c, cached, req, caller, err)
			case answer != nil:
				return answer
			}
			a, err = fetched, nil
		}
		if err != nil {
			return containedRefusal(c, req, err)
		}
		if !acc.File(a.Repo, a.Item.FullPath(), accessapi.ActionRead) {
			return refused(c, req, caller)
		}
		if a.Item.Folder {
			return ItemNotFound()
		}
		// The entry is read through and its bytes dropped, as the download
		// reads it for its checksums before it answers (getArchiveEntry),
		// so an entry too corrupt to read ends on the download's 404.
		err = s.Items(st).ArchiveEntry(ctx, a.Item, entry,
			func(_ int64, r io.Reader) error {
				_, err := io.Copy(io.Discard, r)
				return err //nolint:wrapcheck // ArchiveEntry classifies the entry's own error
			})
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
		return unfound(c, cached, req, caller, miss)
	}
	dl.stepf("Request is for a checksum")
	dl.upstream = s.fileStep(r, req, caller, acc)
	f, err := Lookup(ctx, st, c, cached, artifact, acc, dl, virtual.Resolve)
	if errors.Is(err, kernel.ErrNotFound) {
		return unfound(c, cached, req, caller, err)
	}
	if err != nil {
		return containedRefusal(c, req, err)
	}
	if f.remote != nil {
		return s.traceRemoteRead(r, f.Repo, memberRequest(req, f.Repo), caller, *f.remote, dl.trace)
	}
	if f.Item.Folder {
		return ItemNotFound()
	}
	// A file the caller may not read has no visible checksum either.
	if !acc.File(f.Repo, f.Item.FullPath(), accessapi.ActionRead) {
		return checksumRefused(c, req, caller)
	}
	if item.ServedChecksum(f.Repo, f.Item, alg) == "" {
		return apierror.New(http.StatusNotFound, "Checksum not found for "+f.Item.FullPath())
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

// traceRetrieving records the step that names the repository c, which a
// trace writes only once the caller may read the path.
func traceRetrieving(t *resolutionTrace, c repo.Config, cached bool, req ItemRequest) {
	kind := string(c.Class)
	if cached {
		kind = "cache"
	}
	t.stepf("Retrieving info from %s repository '%s' type %s", kind, req.Key, c.PackageType)
}

// overlayTraceKey marks the context of a request that the overlays decide
// under a trace (traceOverlays).
type overlayTraceKey struct{}

// overlayTrace is what an overlay records while it decides a traced
// request: the steps of what it would serve.
type overlayTrace struct{ steps resolutionTrace }

// overlayTraceOf is r's overlay trace, or nil when r is not traced.
func overlayTraceOf(r *http.Request) *overlayTrace {
	t, _ := r.Context().Value(overlayTraceKey{}).(*overlayTrace)
	return t
}

// traceServed reports whether r is decided under a trace and, if so,
// records that it would be served here, with step when it is not empty.
// The helpers an overlay serves through ask it first (serveFile,
// ServeContent, RemoteDeliveryAllowed), so a traced decision serves,
// redirects and counts nothing, as 7.161.26's TraceLoggingResponse sends
// no stream and no redirect.
func traceServed(r *http.Request, step string) bool {
	t := overlayTraceOf(r)
	if t == nil {
		return false
	}
	if step != "" {
		t.steps.stepf("%s", step)
	}
	return true
}

// errTraceSendsNothing is a traced overlay's failure to write content: a
// trace sends none.
var errTraceSendsNothing = errors.New("itemkit: a trace sends no content")

// traceBodyLimit bounds the error body a traced overlay's writer keeps,
// which apierror writes in a few hundred bytes.
const traceBodyLimit = 64 << 10

// traceWriter is the writer an overlay answers a traced request on. It
// keeps the status and an error's body, which name the answer, and refuses
// any other body, so nothing reaches the client and a StatusWriter over it
// never counts a response complete.
type traceWriter struct {
	header http.Header
	status int
	body   strings.Builder
}

func (w *traceWriter) Header() http.Header { return w.header }

func (w *traceWriter) WriteHeader(code int) {
	if w.status == 0 {
		w.status = code
	}
}

func (w *traceWriter) Write(b []byte) (int, error) {
	if w.status == 0 {
		w.status = http.StatusOK
	}
	if w.status < http.StatusBadRequest || w.body.Len()+len(b) > traceBodyLimit {
		return 0, errTraceSendsNothing
	}
	return w.body.Write(b) //nolint:wrapcheck // strings.Builder never fails
}

// answer is the error the overlay wrote, or nil where it would serve: the
// status, and the message of apierror's envelope, or the status text for a
// body that is not one.
func (w *traceWriter) answer() *apierror.Error {
	if w.status < http.StatusBadRequest {
		return nil
	}
	var envelope struct {
		Errors []struct {
			Message string `json:"message"`
		} `json:"errors"`
	}
	message := http.StatusText(w.status)
	if json.Unmarshal([]byte(w.body.String()), &envelope) == nil && len(envelope.Errors) > 0 {
		message = envelope.Errors[0].Message
	}
	return apierror.New(w.status, message)
}

// traceOverlays lets the format overlays decide req under a trace, as
// GetItem lets them before its own lookup: each runs on a traceWriter, with
// the request marked so that the helpers it serves through record the
// decision in place of serving it (traceServed). 7.161.26's
// RepoFilter.doGetOrHeadRequest answers ?trace by running the download
// itself under a TraceLoggingResponse, so its trace follows a type's own
// handling too (#1595). It reports false when every overlay passes,
// leaving req to the generic trace. Otherwise it returns what the overlay
// answered, or nil where it would serve, after the step that names the
// repository, which a caller the overlay refused does not get.
func (s *Kit) traceOverlays(
	r *http.Request,
	st Store,
	c repo.Config,
	cached bool,
	req ItemRequest,
	p kernel.Principal,
	acc Access,
	t *resolutionTrace,
) (bool, error) {
	ot := &overlayTrace{}
	tw := &traceWriter{header: http.Header{}}
	traced := r.WithContext(context.WithValue(r.Context(), overlayTraceKey{}, ot))
	handled := false
	for _, o := range s.Overlays {
		if handled = o(s, tw, traced, st, c, cached, req, p, acc); handled {
			break
		}
	}
	if !handled {
		return false, nil
	}
	answer := tw.answer()
	if answer != nil &&
		(answer.Status == http.StatusUnauthorized || answer.Status == http.StatusForbidden) {
		return true, answer
	}
	traceRetrieving(t, c, cached, req)
	if t != nil {
		t.steps = append(t.steps, ot.steps.steps...)
	}
	if answer != nil {
		// The download's miss follows DbStoringRepoMixin.getInfo's step for
		// the stored file it did not find, a remote's in its cache, and the
		// download policy's refusal of the file follows
		// createUnfoundRepoResource's, as the generic lookup traces both
		// (Lookup).
		var miss *apierror.Error
		if answer.Status == http.StatusNotFound && fileRequest(c, req) &&
			errors.As(missed(c, cached, req), &miss) && miss.Message == answer.Message {
			t.stepf("Unable to find resource in %s",
				traceRepoPath(c, cached || c.Class == repo.Remote, resourcePath(req.Path)))
		}
		if overlayRefused(c, req, answer) {
			t.denied(asNamed(c, cached, generic.CheckDownload(c, resourcePath(req.Path), false)))
		}
		return true, answer
	}
	t.stepf("Identified requested resource as a file")
	traceResponding(r.Method, t)
	return true, nil
}

// overlayRefused reports whether answer, what an overlay answered req in c,
// is the download policy's refusal of the file req names, with the path
// the item route adds to it (Rejected).
func overlayRefused(c repo.Config, req ItemRequest, answer *apierror.Error) bool {
	var refusal *apierror.Error
	return fileRequest(c, req) &&
		errors.As(lookupRefusal(c, req, generic.CheckDownload(c, resourcePath(req.Path), false)),
			&refusal) &&
		refusal.Status == answer.Status && refusal.Message == answer.Message
}

// remoteTraceStep is the step a trace records for result, a remote read an
// overlay would deliver, in traceRemoteItem's words.
func remoteTraceStep(result source.Result) string {
	if result.Stream != nil || result.Item == nil {
		return "Returning validated transient resource"
	}
	key := result.Item.RepoKey
	if result.Item.Cached {
		key += model.CacheSuffix
	}
	return "Returning found resource from " + key + ":" + result.Item.FullPath()
}
