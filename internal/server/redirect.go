package server

import (
	"cmp"
	"context"
	"maps"
	"net/http"
	"strings"
	"sync"
	"time"

	"github.com/weaveplatform/artifactweave/internal/auth"
	"github.com/weaveplatform/artifactweave/pkg/blob"
	"github.com/weaveplatform/artifactweave/pkg/store"
)

// DefaultRedirectTTL is how long a signed download URL stays valid when
// Config.Redirect sets no TTL.
const DefaultRedirectTTL = 5 * time.Minute

// signWarnEvery bounds how often a failing signer is logged. A store that
// cannot sign fails every redirect, and one line a minute says so without
// a line per download.
const signWarnEvery = time.Minute

// redirectChoice is the header a client steers a download with (decision
// 0029): None streams, S3 means the object store rather than a CDN on every
// cloud, and CF changes nothing because a CDN is already preferred.
const redirectChoice = "X-JFrog-Download-Redirect-To"

// Redirect tunes the download redirect policy.
type Redirect struct {
	// MinSize is the smallest file that redirects; zero redirects every file.
	MinSize int64
	// TTL is how long a signed URL stays valid once handed out; zero means
	// DefaultRedirectTTL.
	TTL time.Duration
}

// route applies the policy of decision 0029 to a GET of f and returns the
// signer and the route a redirect takes; ok false means stream. The numbers
// in the comments are the decision's rules. Rule 1, a conditional request
// answered locally, is the caller's: it only matters when there is a
// redirect to avoid, because a streamed file goes through ServeContent,
// which applies the same rules.
func (s artifactoryService) route(r *http.Request, f found) (blob.Signer, blob.Via, bool) {
	download, cdn := f.repo.DownloadRedirect.Bool(), f.repo.CDNRedirect.Bool()
	choice := r.Header.Get(redirectChoice)
	signer, signs := s.cfg.Blobs.(blob.Signer)
	switch {
	case !download && !cdn, // 2: the repository does not redirect
		f.item.Size < s.cfg.Redirect.MinSize, // 3: too small to be worth a round trip
		strings.EqualFold(choice, "None"),    // 4: the client asks to stream
		!signs:                               // local disk signs nothing
		return nil, 0, false
	case !strings.EqualFold(choice, "S3") && signer.Signs(blob.ViaCDN):
		// 5: either flag is enough for the CDN, as on self-hosted
		// Artifactory, where CloudFront is part of direct download.
		return signer, blob.ViaCDN, true
	case download && signer.Signs(blob.ViaStorage):
		return signer, blob.ViaStorage, true // 6
	}
	return nil, 0, false // 7
}

// redirect answers a GET of f with a 302 to a signed URL when the policy
// says so, and reports whether it wrote a response; false means stream the
// file. HEAD never redirects: the JFrog CLI sends a HEAD, following
// redirects and requiring 200, before it downloads any file over 5,120,000
// bytes, and a URL signed for GET answers that HEAD with 403 on S3 and GCS,
// which failed the download. ServeContent answers HEAD locally instead.
func (s artifactoryService) redirect(
	w http.ResponseWriter,
	r *http.Request,
	st store.Store,
	f found,
	p auth.Principal,
) bool {
	if r.Method != http.MethodGet {
		return false
	}
	signer, via, ok := s.route(r, f)
	if !ok {
		return false
	}
	it := f.item
	// The object store would judge If-None-Match against its own ETag, never
	// the sha1 this response carries, so a revalidating client would fetch
	// the whole file again. The item's validators decide here instead.
	if answerPreconditions(w, r, it.Modified) {
		return true
	}
	ctx := r.Context()
	u, err := signer.SignURL(ctx, it.Actual.SHA256, blob.SignOptions{
		Via: via, TTL: cmp.Or(s.cfg.Redirect.TTL, DefaultRedirectTTL),
		Filename: it.Name, ContentType: it.MimeType,
	})
	if err != nil {
		s.signFailed(ctx, f.repo.Key, via, err)
		return false
	}
	// The 302 has no body, so the headers describing the artifact's body go;
	// the signed URL carries the name and media type as overrides instead.
	// The checksums stay for clients that read them before following.
	h := w.Header()
	h.Del("Content-Type")
	h.Del("Content-Disposition")
	h.Del("ETag")
	h.Set("Cache-Control", "private, no-store")
	h.Set("Location", u)
	w.WriteHeader(http.StatusFound)
	// The object store answers the GET, so count it as ServeContent's
	// answer would have been: whole, or a range from the first byte.
	status := http.StatusOK
	if r.Header.Get("Range") != "" {
		status = http.StatusPartialContent
	}
	if countsDownload(r, status) {
		recordDownload(ctx, st, f, p)
	}
	return true
}

// signFailed logs a signing failure, at most once per signWarnEvery with a
// count of the failures held back, so a misconfigured store is visible
// without a line per download.
func (s artifactoryService) signFailed(
	ctx context.Context,
	repoKey string,
	via blob.Via,
	err error,
) {
	ok, held := s.signWarn.allow(time.Now())
	if !ok {
		return
	}
	s.log().WarnContext(ctx, "download redirect: signing failed, streaming instead",
		"repo", repoKey, "via", via.String(), "request_id", requestIDFrom(ctx), "error", err,
		"held_back", held)
}

// answerPreconditions answers the request when its conditional headers
// decide the response (304 Not Modified or 412 Precondition Failed) and
// reports whether it did. http.ServeContent evaluates them before it reads
// any content, so a probe with an empty body applies exactly the standard
// library's rules (RFC 9110 section 13.2.2); its response is discarded
// unless it is that decision. The ETag must already be in w's headers.
func answerPreconditions(w http.ResponseWriter, r *http.Request, modified time.Time) bool {
	probe := &headerProbe{header: w.Header().Clone()}
	http.ServeContent(probe, r, "", modified, strings.NewReader(""))
	if probe.status != http.StatusNotModified && probe.status != http.StatusPreconditionFailed {
		return false
	}
	// ServeContent removes what a 304 must not carry; keep its headers.
	h := w.Header()
	for k := range h {
		delete(h, k)
	}
	maps.Copy(h, probe.header)
	w.WriteHeader(probe.status)
	return true
}

// headerProbe is a ResponseWriter that keeps the status and headers and
// drops the body.
type headerProbe struct {
	header http.Header
	status int
}

func (p *headerProbe) Header() http.Header { return p.header }

func (p *headerProbe) WriteHeader(code int) {
	if p.status == 0 {
		p.status = code
	}
}

func (p *headerProbe) Write(b []byte) (int, error) {
	p.WriteHeader(http.StatusOK)
	return len(b), nil
}

// countsDownload reports whether a response with status counts as a
// download: a whole file, or the first part of a split download, so a split
// download counts once. skipUpdateStats=true opts out.
func countsDownload(r *http.Request, status int) bool {
	if r.Method != http.MethodGet || r.URL.Query().Get("skipUpdateStats") == "true" {
		return false
	}
	return status == http.StatusOK ||
		(status == http.StatusPartialContent && startsAtZero(r.Header.Get("Range")))
}

// recordDownload updates the item's statistics. Best effort: a failed count
// must not fail the download.
func recordDownload(ctx context.Context, st store.Store, f found, p auth.Principal) {
	_ = st.Items().RecordDownload(ctx, f.item.ID, store.Download{By: p.Name, At: time.Now().UTC()})
}

// throttle lets one event through per interval and counts the ones it holds
// back. A nil throttle lets every event through.
type throttle struct {
	every time.Duration
	mu    sync.Mutex
	last  time.Time
	held  int
}

func newThrottle(every time.Duration) *throttle { return &throttle{every: every} }

// allow reports whether an event at now goes through and, when it does, how
// many were held back since the last one that did.
func (t *throttle) allow(now time.Time) (bool, int) {
	if t == nil {
		return true, 0
	}
	t.mu.Lock()
	defer t.mu.Unlock()
	if !t.last.IsZero() && now.Sub(t.last) < t.every {
		t.held++
		return false, 0
	}
	held := t.held
	t.last, t.held = now, 0
	return true, held
}
