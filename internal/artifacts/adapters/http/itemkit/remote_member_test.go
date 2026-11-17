package itemkit_test

import (
	"archive/zip"
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync/atomic"
	"testing"
	"time"

	accessapi "github.com/weaveplatform/artifactweave/internal/access/api"
	"github.com/weaveplatform/artifactweave/internal/access/api/accessapitest"
	"github.com/weaveplatform/artifactweave/internal/app/adapters/inmem"
	"github.com/weaveplatform/artifactweave/internal/artifacts/adapters/http/itemkit"
	"github.com/weaveplatform/artifactweave/internal/artifacts/adapters/http/itemkit/kittest"
	"github.com/weaveplatform/artifactweave/internal/artifacts/api/repo"
	"github.com/weaveplatform/artifactweave/internal/artifacts/core/format"
	"github.com/weaveplatform/artifactweave/internal/artifacts/core/model"
	"github.com/weaveplatform/artifactweave/internal/artifacts/core/remote/source"
	"github.com/weaveplatform/artifactweave/internal/kernel"
	"github.com/weaveplatform/artifactweave/internal/platform/blob"
)

// memberSource is a remote source whose upstream answers each read with
// answer, and which records every read it is asked for.
type memberSource struct {
	reads  []source.Request
	answer func(source.Request) (source.Result, error)
}

func (m *memberSource) Read(
	_ context.Context, q source.Request, _ source.Permit,
) (source.Result, error) {
	m.reads = append(m.reads, q)
	return m.answer(q)
}

// memberWorld is kittest's world, whose virtual repository libs reads the
// local libs-local, then the remote libs-remote, with the remote source
// src behind every remote; the remote other-remote; pair, the virtual
// repository over libs-remote, then other-remote; and the CRAN remote
// cran-remote under the virtual repository cran, which the item route
// never reads upstream.
type memberWorld struct {
	*kittest.World[*inmem.Store]
	src *memberSource
	k   *itemkit.Kit
	// unresolved makes every remote's source fail to resolve.
	unresolved bool
}

func newMemberWorld(t *testing.T) *memberWorld {
	t.Helper()
	w := &memberWorld{World: kittest.New(t, inmem.New()), src: &memberSource{}}
	for _, c := range []repo.Config{
		{Key: "other-remote", Class: repo.Remote, URL: "https://other.example"},
		{Key: "pair", Class: repo.Virtual, Repositories: []string{kittest.RemoteKey, "other-remote"}},
		{Key: "cran-remote", Class: repo.Remote, PackageType: repo.Cran, URL: "https://cran.example"},
		{Key: "cran", Class: repo.Virtual, PackageType: repo.Cran, Repositories: []string{"cran-remote"}},
	} {
		if err := w.Store.Repos().Create(t.Context(), *repo.Defaults(&c)); err != nil {
			t.Fatal(err)
		}
	}
	// Each remote resolves to the source the store keeps for it, as the
	// application's do, under which a fill records its copies. The source
	// remembers a label's refusal by the source's identity, in memory every
	// world shares, and each stored source is new.
	w.k = w.Kit(itemkit.Deps{RemoteSources: &source.Service{
		Resolve: func(ctx context.Context, c repo.Config) (model.SourceIdentity, error) {
			if w.unresolved {
				return model.SourceIdentity{}, source.ErrUnavailable
			}
			id, err := w.Store.RemoteCache().Source(ctx, c.Key)
			if err != nil {
				return model.SourceIdentity{}, fmt.Errorf("source of %s: %w", c.Key, err)
			}
			return id, nil
		},
		HTTP: w.src,
	}})
	return w
}

// get sends method target through GetItem as admin, or under acc when it
// is given, and returns the answer.
func (w *memberWorld) get(
	t *testing.T, method, target string, acc ...itemkit.Access,
) *httptest.ResponseRecorder {
	t.Helper()
	r := kittest.Request(t, method, target)
	if len(acc) > 0 {
		r = r.WithContext(accessapi.WithCaller(r.Context(),
			kernel.Principal{Name: "reader"}, acc[0].Decider))
	}
	rec := httptest.NewRecorder()
	w.k.GetItem(rec, r)
	return rec
}

// streamed is an upstream's answer of body that the cache does not keep,
// with sums as the checksums its headers carry; closed counts its closes.
func streamed(body string, sums kernel.Checksums, closed *atomic.Int32) source.Result {
	return source.Result{Stream: &source.Stream{
		Body:   &countedBody{Reader: strings.NewReader(body), closed: closed},
		Status: http.StatusOK,
		Representation: source.Representation{
			Size: int64(len(body)), MediaType: "application/octet-stream", Checksums: sums,
		},
	}}
}

type countedBody struct {
	io.Reader
	closed *atomic.Int32
}

func (b *countedBody) Close() error {
	if b.closed != nil {
		b.closed.Add(1)
	}
	return nil
}

// wantMessage fails unless rec answered status with the Artifactory error
// envelope of message, or with no body to a HEAD.
func wantMessage(t *testing.T, what string, rec *httptest.ResponseRecorder, status int, message string) {
	t.Helper()
	if rec.Code != status {
		t.Fatalf("%s: %d %q, want %d", what, rec.Code, rec.Body, status)
	}
	var body struct {
		Errors []struct {
			Status  int    `json:"status"`
			Message string `json:"message"`
		} `json:"errors"`
	}
	if err := json.Unmarshal(rec.Body.Bytes(), &body); err != nil || len(body.Errors) != 1 ||
		body.Errors[0].Message != message {
		t.Fatalf("%s: %q, want %q", what, rec.Body, message)
	}
}

// TestUnitVirtualUpstreamStepJudgesEachRead (#1535): a walk asks a remote
// member's upstream only after every member's own copy, with the member's
// own source request, and passes over every read that finds nothing or
// fails upstream (decision 0008, R3), to the virtual's 404, or to the next
// member. A Read refusal is kept as any member's, and a refused cache
// fill, a fault of the server's own storage and a superseded read are the
// member's answers.
func TestUnitVirtualUpstreamStepJudgesEachRead(t *testing.T) {
	const miss = "Could not find resource; Path: 'libs:f.bin'"
	for _, tc := range []struct {
		name    string
		err     error
		status  int
		message string
	}{
		{"miss", kernel.ErrNotFound, 404, miss},
		{"assumed offline", source.ErrAssumedOffline, 404, miss},
		{"policy", format.Rejectf(http.StatusNotFound, "refused by policy"), 404, miss},
		{"label", &source.MIMERefusal{RejectionError: format.Rejectf(409, "label")}, 404, miss},
		{"upstream 500", &source.HTTPError{Status: 500}, 404, miss},
		{"upstream 401", &source.HTTPError{Status: 401}, 404, miss},
		{"upstream 403", &source.HTTPError{Status: 403}, 404, miss},
		{"unavailable", source.ErrUnavailable, 404, miss},
		{"invalid result", source.ErrInvalidResult, 404, miss},
		{"invalid request", source.ErrInvalidRequest, 404, miss},
		{"unsupported", source.ErrUnsupported, 404, miss},
		{"timeout", context.DeadlineExceeded, 404, miss},
		{"cancelled", context.Canceled, 404, miss},
		{
			"denied", source.ErrDenied, 403,
			"Download request for repo:path 'libs-remote-cache:f.bin' is forbidden for user: " +
				"'admin'.; Path: 'libs:f.bin'",
		},
		{
			"refused fill",
			&source.CacheFillDenied{User: "admin", Repo: kittest.RemoteKey, Path: "f.bin"}, 403,
			"Rejected artifact download request: User admin is not permitted to deploy " +
				"'f.bin' into 'libs-remote-cache:f.bin'.",
		},
		{"storage", fmt.Errorf("keep: %w", format.ErrStorageFault), 500, "internal error"},
		{"superseded", source.ErrSuperseded, 503, "Service Unavailable"},
	} {
		t.Run(tc.name, func(t *testing.T) {
			w := newMemberWorld(t)
			w.src.answer = func(source.Request) (source.Result, error) { return source.Result{}, tc.err }
			wantMessage(t, tc.name, w.get(t, http.MethodGet, "/artifactory/libs/f.bin"),
				tc.status, tc.message)
			if len(w.src.reads) != 1 {
				t.Fatalf("%d reads", len(w.src.reads))
			}
		})
	}

	// A member passed over lets the next one's upstream serve.
	w := newMemberWorld(t)
	w.src.answer = func(q source.Request) (source.Result, error) {
		if q.Repo.Key == kittest.RemoteKey {
			return source.Result{}, &source.HTTPError{Status: 502}
		}
		return streamed("other bytes", kernel.Checksums{}, nil), nil
	}
	if rec := w.get(t, http.MethodGet, "/artifactory/pair/f.bin"); rec.Code != 200 ||
		rec.Body.String() != "other bytes" || len(w.src.reads) != 2 {
		t.Fatalf("next member: %d %q after %d reads", rec.Code, rec.Body, len(w.src.reads))
	}
}

// TestUnitVirtualUpstreamStepServesTheMembersRead (#1535): the read is the
// member's own, under its key and with the caller and the request's path,
// method, query and matrix parameters, and the virtual answers it as a
// direct read of the member: the stream's bytes on a GET and none on a
// HEAD.
func TestUnitVirtualUpstreamStepServesTheMembersRead(t *testing.T) {
	w := newMemberWorld(t)
	var closed atomic.Int32
	w.src.answer = func(source.Request) (source.Result, error) {
		return streamed("fetched", kernel.Checksums{}, &closed), nil
	}
	for _, method := range []string{http.MethodGet, http.MethodHead} {
		rec := w.get(t, method, "/artifactory/libs/d/f.bin;k=v?x=1")
		want := "fetched"
		if method == http.MethodHead {
			want = ""
		}
		if rec.Code != 200 || rec.Body.String() != want ||
			rec.Header().Get("Content-Length") != "7" {
			t.Fatalf("%s: %d %q %v", method, rec.Code, rec.Body, rec.Header())
		}
		q := w.src.reads[len(w.src.reads)-1]
		if q.Repo.Key != kittest.RemoteKey || q.Subject.Name != "admin" ||
			q.Resource.Path != "d/f.bin" || q.Resource.Method != method ||
			q.Resource.Query != "x=1" || q.MatrixParams != ";k=v" || q.CacheOnly {
			t.Fatalf("%s read %+v", method, q)
		}
	}
	if closed.Load() != 2 {
		t.Fatalf("streams closed %d times", closed.Load())
	}

	// A local holding the path wins, and so does one lacking a matrix
	// parameter the request names, over a remote that does not synchronize
	// properties: the upstream is never asked.
	w.Put(t, w.Local, false, "held.bin", []byte("local"))
	for _, target := range []string{"/artifactory/libs/held.bin", "/artifactory/libs/held.bin;k=v"} {
		reads := len(w.src.reads)
		if rec := w.get(t, http.MethodGet, target); rec.Code != 200 || rec.Body.String() != "local" ||
			len(w.src.reads) != reads {
			t.Fatalf("%s: %d %q, %d reads", target, rec.Code, rec.Body, len(w.src.reads)-reads)
		}
	}

	// A folder request and a member type the item route never reads
	// upstream take no upstream step.
	reads := len(w.src.reads)
	if rec := w.get(t, http.MethodGet, "/artifactory/cran/src/contrib/a_1.0.tar.gz"); rec.Code != 404 {
		t.Fatalf("CRAN member: %d %q", rec.Code, rec.Body)
	}
	if rec := w.get(t, http.MethodGet, "/artifactory/libs/d/"); rec.Code != 404 {
		t.Fatalf("folder: %d %q", rec.Code, rec.Body)
	}
	if len(w.src.reads) != reads {
		t.Fatalf("%d reads for paths the walk reads no upstream for", len(w.src.reads)-reads)
	}
}

// TestUnitVirtualUpstreamStepNeedsRead (#1535): a caller who may not read
// the remote member's file is refused there and the upstream is not asked,
// whether the folder or the file refuses it; one who may read it is
// served from the upstream.
func TestUnitVirtualUpstreamStepNeedsRead(t *testing.T) {
	w := newMemberWorld(t)
	w.src.answer = func(source.Request) (source.Result, error) {
		return streamed("secret", kernel.Checksums{}, nil), nil
	}
	notRemote := func(_ accessapi.ResourceType, r accessapi.Repo, _ string, _ accessapi.Actions) bool {
		return r.Key != kittest.RemoteKey
	}
	all := func(accessapi.ResourceType, accessapi.Repo, string, accessapi.Actions) bool { return true }
	const refused = "Download request for repo:path 'libs-remote-cache:f.bin' is forbidden for " +
		"user: 'reader'.; Path: 'libs:f.bin'"
	for name, decider := range map[string]*accessapitest.Decider{
		"folder": {AllowsFolderFunc: notRemote, AllowsFunc: notRemote},
		"file":   {AllowsFolderFunc: all, AllowsFunc: notRemote},
	} {
		wantMessage(t, name, w.get(t, http.MethodGet, "/artifactory/libs/f.bin",
			itemkit.Access{Decider: decider}), 403, refused)
	}
	if len(w.src.reads) != 0 {
		t.Fatalf("%d reads for a caller who may not read the member", len(w.src.reads))
	}
	if rec := w.get(t, http.MethodGet, "/artifactory/libs/f.bin"); rec.Code != 200 ||
		rec.Body.String() != "secret" || len(w.src.reads) != 1 {
		t.Fatalf("reader of the member: %d %q after %d reads", rec.Code, rec.Body, len(w.src.reads))
	}
}

// TestUnitVirtualUpstreamStepChecksums (#1535): a checksum file through the
// virtual reads the member as a direct checksum request does, and answers
// the checksum the upstream's answer carries under the pass-through
// policy, or "Checksum not found" for a file that names none.
func TestUnitVirtualUpstreamStepChecksums(t *testing.T) {
	w := newMemberWorld(t)
	w.Remote.RemoteRepoChecksumPolicyType = repo.PassThru
	if err := w.Store.Repos().Update(t.Context(), w.Remote); err != nil {
		t.Fatal(err)
	}
	const sha1 = "0123456789abcdef0123456789abcdef01234567"
	w.src.answer = func(q source.Request) (source.Result, error) {
		if !q.Resource.ChecksumRequest || q.Resource.Method != http.MethodHead {
			return source.Result{}, fmt.Errorf("read %+v: %w", q.Resource, source.ErrInvalidRequest)
		}
		if q.Resource.Path == "summed.bin" {
			return streamed("", kernel.Checksums{SHA1: sha1}, nil), nil
		}
		return streamed("", kernel.Checksums{}, nil), nil
	}
	if rec := w.get(t, http.MethodGet, "/artifactory/libs/summed.bin.sha1"); rec.Code != 200 ||
		rec.Body.String() != sha1 {
		t.Fatalf("checksum: %d %q", rec.Code, rec.Body)
	}
	wantMessage(t, "no checksum", w.get(t, http.MethodGet, "/artifactory/libs/plain.bin.sha1"),
		404, "Checksum not found for plain.bin")
}

// zipOf is a zip archive holding name with body.
func zipOf(t *testing.T, name, body string) []byte {
	t.Helper()
	var b bytes.Buffer
	z := zip.NewWriter(&b)
	f, err := z.Create(name)
	if err != nil {
		t.Fatal(err)
	}
	if _, err = f.Write([]byte(body)); err != nil {
		t.Fatal(err)
	}
	if err = z.Close(); err != nil {
		t.Fatal(err)
	}
	return b.Bytes()
}

// TestUnitRemoteArchiveEntriesFetchTheArchive (#1535): an archive entry a
// remote's cache lacks has its archive fetched into the cache with a plain
// GET of the archive, as 7.161.26's getRemoteZipRepoResource downloads it,
// directly and through a virtual repository; a refused cache fill answers
// 7.x's "Could not download" directly and passes over the member through
// the virtual, a stream the cache does not keep and a remote that stores
// nothing keep the miss, and an upstream miss answers as the archive's.
func TestUnitRemoteArchiveEntriesFetchTheArchive(t *testing.T) {
	w := newMemberWorld(t)
	archive := zipOf(t, "in/a.txt", "entry")
	var closed atomic.Int32
	w.src.answer = func(q source.Request) (source.Result, error) {
		switch path := q.Resource.Path; path {
		case "d/a b.zip", "v.zip":
			it := w.Put(t, w.Remote, true, path, archive)
			return source.Result{Item: &it}, nil
		case "fill.zip":
			return source.Result{}, &source.CacheFillDenied{User: "admin", Repo: q.Repo.Key, Path: path}
		case "stream.zip":
			return streamed(string(archive), kernel.Checksums{}, &closed), nil
		}
		return source.Result{}, kernel.ErrNotFound
	}
	r := kittest.Request(t, http.MethodGet, "/artifactory/libs-remote/d/a%20b.zip!/in/a.txt;k=v?x=1")
	r.Header.Set("Range", "bytes=0-1")
	r.Header.Set("Accept", "text/plain")
	rec := httptest.NewRecorder()
	w.k.GetItem(rec, r)
	// The client's range is the entry's, not the archive's.
	if rec.Code != 206 || rec.Body.String() != "en" || len(w.src.reads) != 1 {
		t.Fatalf("direct entry: %d %q after %d reads", rec.Code, rec.Body, len(w.src.reads))
	}
	q := w.src.reads[0].Resource
	if q.Method != http.MethodGet || q.EscapedPath != "d/a%20b.zip" || q.Query != "" ||
		q.Range != "" || len(q.ClientMIME.Accept) != 0 || w.src.reads[0].MatrixParams != "" ||
		len(w.src.reads[0].Matrix.Props) != 0 {
		t.Fatalf("archive read %+v", w.src.reads[0])
	}
	if rec := w.get(t, http.MethodGet, "/artifactory/libs/v.zip!/in/a.txt"); rec.Code != 200 ||
		rec.Body.String() != "entry" {
		t.Fatalf("virtual entry: %d %q", rec.Code, rec.Body)
	}

	wantMessage(t, "refused fill", w.get(t, http.MethodGet, "/artifactory/libs-remote/fill.zip!/in/a.txt"),
		404, "Could not download 'in/a.txt' from main zip resource; Path: 'libs-remote:fill.zip'")
	wantMessage(t, "refused fill through the virtual",
		w.get(t, http.MethodGet, "/artifactory/libs/fill.zip!/in/a.txt"),
		404, "Could not find resource; Path: 'libs:fill.zip'")
	wantMessage(t, "upstream miss", w.get(t, http.MethodGet, "/artifactory/libs-remote/none.zip!/in/a.txt"),
		404, "Not Found; Path: 'libs-remote:none.zip'")
	wantMessage(t, "stream", w.get(t, http.MethodGet, "/artifactory/libs-remote/stream.zip!/in/a.txt"),
		404, "Not Found; Path: 'libs-remote:stream.zip'")
	if closed.Load() != 1 {
		t.Fatalf("stream closed %d times", closed.Load())
	}

	reads := len(w.src.reads)
	w.Remote.StoreArtifactsLocally = new(false)
	if err := w.Store.Repos().Update(t.Context(), w.Remote); err != nil {
		t.Fatal(err)
	}
	for _, target := range []string{
		"/artifactory/libs-remote/kept.zip!/in/a.txt", "/artifactory/libs/kept.zip!/in/a.txt",
	} {
		if rec := w.get(t, http.MethodGet, target); rec.Code != 404 {
			t.Fatalf("%s: %d %q", target, rec.Code, rec.Body)
		}
	}
	if len(w.src.reads) != reads {
		t.Fatalf("a remote that stores nothing was read %d times", len(w.src.reads)-reads)
	}
}

// TestUnitVirtualUpstreamStepTrace (#1535): a trace through the virtual
// names the remote's cache and its upstream step apart, and traces the
// member's read as a direct read of the member is traced, without serving
// it.
func TestUnitVirtualUpstreamStepTrace(t *testing.T) {
	w := newMemberWorld(t)
	w.src.answer = func(q source.Request) (source.Result, error) {
		switch q.Resource.Path {
		case "f.bin":
			return streamed("traced", kernel.Checksums{}, nil), nil
		case "a.zip":
			it := w.Put(t, w.Remote, true, "a.zip", zipOf(t, "in/a.txt", "entry"))
			return source.Result{Item: &it}, nil
		case "gone.bin":
			return source.Result{}, &source.HTTPError{Status: 500}
		}
		return source.Result{}, format.Rejectf(http.StatusForbidden, "policy")
	}
	for _, tc := range []struct {
		target string
		steps  []string
	}{
		{"/artifactory/libs/f.bin?trace", []string{
			"Searching for the resource within libs-local",
			"Searching for the resource within libs-remote-cache",
			"Searching for the resource within libs-remote",
			"Resolving remote source with current read authorization",
			"Returning validated transient resource",
			"Responding with found resource",
		}},
		{"/artifactory/libs/a.zip!/in/a.txt?trace", []string{
			"Requested resource is contained within an archive",
			"Searching for the resource within libs-remote",
			"Returning found resource from libs-remote-cache:a.zip",
		}},
		{"/artifactory/libs/f.bin.sha1?trace", []string{
			"Request is for a checksum",
			"Searching for the resource within libs-remote",
		}},
		{"/artifactory/libs/gone.bin?trace", []string{
			"Failed to retrieve information: Internal Server Error",
		}},
		{"/artifactory/libs/refused.bin?trace", []string{
			"Download denied (policy) - returning un-found resource",
		}},
		{"/artifactory/libs-remote/b.zip!/in/a.txt?trace", []string{
			"Requested resource is contained within an archive",
			"Resolving remote source with current read authorization",
		}},
	} {
		rec := w.get(t, http.MethodGet, tc.target)
		body := rec.Body.String()
		at := 0
		for _, step := range tc.steps {
			i := strings.Index(body[at:], step)
			if i < 0 {
				t.Fatalf("%s: no %q after offset %d in\n%s", tc.target, step, at, body)
			}
			at += i + len(step)
		}
	}
}

// TestUnitVirtualUpstreamStepTracesAPolicyRefusalOnce (#1535): a remote
// member whose download policy refuses its upstream read is traced once,
// as a direct read of it is, and the walk passes over it: the refusal is
// not the virtual repository's answer, which here is the next member's
// 403, so the trace does not respond with an unfound resource.
func TestUnitVirtualUpstreamStepTracesAPolicyRefusalOnce(t *testing.T) {
	w := newMemberWorld(t)
	w.src.answer = func(q source.Request) (source.Result, error) {
		if q.Repo.Key == kittest.RemoteKey {
			return source.Result{}, &format.RejectionError{
				Status: http.StatusConflict, Message: "blocked", Policy: true,
			}
		}
		return source.Result{}, source.ErrDenied
	}
	body := w.get(t, http.MethodGet, "/artifactory/pair/f.bin?trace").Body.String()
	const denied = "Download denied (blocked) - returning un-found resource"
	if n := strings.Count(body, denied); n != 1 ||
		!strings.Contains(body, "Sending a response with the status '403'") ||
		strings.Contains(body, "Responding with unfound resource") {
		t.Fatalf("%d refusal lines in\n%s", n, body)
	}
}

// cache puts body at path in the cache of the remote repository key and
// records it as a fill under the remote's source records a copy of kind,
// last validated at validated.
func (w *memberWorld) cache(
	t *testing.T, key, path, body string, kind model.RemoteKind, validated time.Time,
) model.Item {
	t.Helper()
	ctx := t.Context()
	c, err := w.Store.Repos().Get(ctx, key)
	if err != nil {
		t.Fatal(err)
	}
	it := w.Put(t, c, true, path, []byte(body))
	token, err := w.Store.RemoteCache().Begin(ctx, model.RemoteCacheKey{RepoKey: key, Path: path})
	if err != nil {
		t.Fatal(err)
	}
	if _, err = w.Store.RemoteCache().Put(ctx, token, model.RemoteCacheEntry{
		Kind: kind, ItemID: it.ID, Blob: blob.Blob{Checksums: it.Actual}, ValidatedAt: validated,
	}); err != nil {
		t.Fatal(err)
	}
	return it
}

// update saves c over the stored repository of its key.
func (w *memberWorld) update(t *testing.T, c repo.Config) {
	t.Helper()
	if err := w.Store.Repos().Update(t.Context(), *repo.Defaults(&c)); err != nil {
		t.Fatal(err)
	}
}

// TestUnitVirtualWalkPassesOverAnExpiredCachedCopy (#1641): a remote
// member's cache step passes over a copy that has expired by the rule a
// direct read of the member revalidates it by, as 7.161.26's
// DbCacheRepo.getInfo returns it as an ExpiredRepoResource: a later
// member's fresh copy serves, without asking the first member's upstream,
// and otherwise the member's upstream step reads the file, falling back on
// the expired copy when it finds nothing (RemoteRepoBase.internalGetInfo
// 535-539). A copy that has not expired, an offline member's, one no entry
// of the current source records, and one whose source cannot be resolved
// serve from the cache step.
func TestUnitVirtualWalkPassesOverAnExpiredCachedCopy(t *testing.T) {
	stale := time.Now().Add(-24 * time.Hour)
	w := newMemberWorld(t)
	w.src.answer = func(q source.Request) (source.Result, error) {
		if q.Resource.Path == "newer.bin" {
			it := w.Put(t, w.Remote, true, "newer.bin", []byte("newer"))
			return source.Result{Item: &it}, nil
		}
		return source.Result{}, kernel.ErrNotFound
	}
	trace := func(target string, steps ...string) {
		t.Helper()
		body := w.get(t, http.MethodGet, target+"?trace").Body.String()
		at := 0
		for _, step := range steps {
			i := strings.Index(body[at:], step)
			if i < 0 {
				t.Fatalf("%s: no %q after offset %d in\n%s", target, step, at, body)
			}
			at += i + len(step)
		}
	}

	w.cache(t, kittest.RemoteKey, "both.bin", "stale", model.RemoteMetadata, stale)
	w.cache(t, "other-remote", "both.bin", "fresh", model.RemoteMetadata, time.Now())
	rec := w.get(t, http.MethodGet, "/artifactory/pair/both.bin")
	if rec.Code != 200 || rec.Body.String() != "fresh" || len(w.src.reads) != 0 {
		t.Fatalf("a later fresh copy: %d %q after %d reads", rec.Code, rec.Body, len(w.src.reads))
	}
	trace("/artifactory/pair/both.bin",
		"Searching for the resource within libs-remote-cache",
		"Found the resource in the cache - checking for expiry",
		"Returning resource as expired",
		"Searching for the resource within other-remote-cache",
		"Found the resource in the cache - checking for expiry",
		"Returning cached resource",
		"Returning found resource from other-remote-cache:both.bin",
	)

	w.cache(t, kittest.RemoteKey, "newer.bin", "stale", model.RemoteMetadata, stale)
	rec = w.get(t, http.MethodGet, "/artifactory/pair/newer.bin")
	if rec.Code != 200 || rec.Body.String() != "newer" || len(w.src.reads) != 1 ||
		w.src.reads[0].Repo.Key != kittest.RemoteKey {
		t.Fatalf("the expired copy alone: %d %q after %d reads", rec.Code, rec.Body, len(w.src.reads))
	}

	w.cache(t, kittest.RemoteKey, "gone.bin", "stale", model.RemoteMetadata, stale)
	if rec = w.get(t, http.MethodGet, "/artifactory/pair/gone.bin"); rec.Code != 200 ||
		rec.Body.String() != "stale" {
		t.Fatalf("an upstream miss: %d %q", rec.Code, rec.Body)
	}
	trace("/artifactory/pair/gone.bin",
		"Returning resource as expired",
		"Searching for the resource within libs-remote",
		"Unable to find resource in libs-remote:gone.bin",
		"Resource doesn't exist remotely but is expired in the caches - "+
			"returning expired cached resource",
		"Returning found resource from libs-remote-cache:gone.bin",
	)

	reads := len(w.src.reads)
	w.cache(t, kittest.RemoteKey, "unique.bin", "kept", model.RemoteUnique, stale)
	w.cache(t, kittest.RemoteKey, "recent.bin", "kept", model.RemoteMetadata, time.Now())
	w.Put(t, w.Remote, true, "unrecorded.bin", []byte("kept"))
	for _, name := range []string{"unique.bin", "recent.bin", "unrecorded.bin"} {
		if rec = w.get(t, http.MethodGet, "/artifactory/pair/"+name); rec.Code != 200 ||
			rec.Body.String() != "kept" || len(w.src.reads) != reads {
			t.Fatalf("%s: %d %q after %d reads", name, rec.Code, rec.Body, len(w.src.reads)-reads)
		}
	}
	w.unresolved = true
	w.cache(t, kittest.RemoteKey, "unresolved.bin", "kept", model.RemoteMetadata, stale)
	if rec = w.get(t, http.MethodGet, "/artifactory/pair/unresolved.bin"); rec.Code != 200 ||
		rec.Body.String() != "kept" {
		t.Fatalf("an unresolved source: %d %q", rec.Code, rec.Body)
	}
	w.unresolved = false
	w.cache(t, kittest.RemoteKey, "offline.bin", "kept", model.RemoteMetadata, stale)
	w.update(t, repo.Config{
		Key: kittest.RemoteKey, Class: repo.Remote, URL: "https://remote.example", Offline: new(true),
	})
	if rec = w.get(t, http.MethodGet, "/artifactory/pair/offline.bin"); rec.Code != 200 ||
		rec.Body.String() != "kept" || len(w.src.reads) != reads {
		t.Fatalf("an offline member: %d %q after %d reads", rec.Code, rec.Body, len(w.src.reads)-reads)
	}
}

// TestUnitVirtualWalkFallsBackOnAnExpiredArchive (#1641): an archive entry
// read through a virtual repository looks for the archive as a download
// of it, so an expired archive passes over to the member's upstream step,
// which serves the entry from the archive it fetches, or from the expired
// one when the upstream lacks it or the caller may not fill the cache. A
// remote that stores nothing does not expire the archive its cache holds.
func TestUnitVirtualWalkFallsBackOnAnExpiredArchive(t *testing.T) {
	stale := time.Now().Add(-24 * time.Hour)
	w := newMemberWorld(t)
	w.src.answer = func(q source.Request) (source.Result, error) {
		switch q.Resource.Path {
		case "newer.zip":
			it := w.Put(t, w.Remote, true, "newer.zip", zipOf(t, "in/a.txt", "newer"))
			return source.Result{Item: &it}, nil
		case "fill.zip":
			return source.Result{}, &source.CacheFillDenied{User: "admin", Repo: kittest.RemoteKey, Path: "fill.zip"}
		}
		return source.Result{}, kernel.ErrNotFound
	}
	for _, name := range []string{"newer.zip", "gone.zip", "fill.zip"} {
		w.cache(t, kittest.RemoteKey, name, string(zipOf(t, "in/a.txt", "stale")), model.RemoteMetadata, stale)
	}
	for name, want := range map[string]string{"newer.zip": "newer", "gone.zip": "stale", "fill.zip": "stale"} {
		if rec := w.get(t, http.MethodGet, "/artifactory/pair/"+name+"!/in/a.txt"); rec.Code != 200 ||
			rec.Body.String() != want {
			t.Fatalf("%s: %d %q, want %q", name, rec.Code, rec.Body, want)
		}
	}
	reads := len(w.src.reads)
	w.cache(t, kittest.RemoteKey, "nostore.zip", string(zipOf(t, "in/a.txt", "kept")), model.RemoteMetadata, stale)
	w.update(t, repo.Config{
		Key: kittest.RemoteKey, Class: repo.Remote, URL: "https://remote.example",
		StoreArtifactsLocally: new(false),
	})
	if rec := w.get(t, http.MethodGet, "/artifactory/pair/nostore.zip!/in/a.txt"); rec.Code != 200 ||
		rec.Body.String() != "kept" || len(w.src.reads) != reads {
		t.Fatalf("a remote that stores nothing: %d %q after %d reads", rec.Code, rec.Body, len(w.src.reads)-reads)
	}
}
