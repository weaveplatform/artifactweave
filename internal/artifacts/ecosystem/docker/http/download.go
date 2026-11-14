package dockerhttp

import (
	"net/http"

	"github.com/weaveplatform/artifactweave/internal/artifacts/adapters/http/pkgroute"
	"github.com/weaveplatform/artifactweave/internal/artifacts/core/model"
	layout "github.com/weaveplatform/artifactweave/internal/artifacts/ecosystem/docker"
)

// pullRequest is r as the registry serves a blob or a manifest, local or
// remote, GET or HEAD: without the request headers 7.x's Docker API does
// not act on (ignoredOnPull), so the answer is the plain request's, or 304
// when If-None-Match or If-Modified-Since says the client's copy is
// current. The copy leaves the caller's request as it was sent.
func pullRequest(r *http.Request) *http.Request {
	present := false
	for _, k := range ignoredOnPull {
		present = present || len(r.Header.Values(k)) > 0
	}
	if !present {
		return r
	}
	out := r.WithContext(r.Context())
	out.Header = r.Header.Clone()
	for _, k := range ignoredOnPull {
		out.Header.Del(k)
	}
	return out
}

// notModified answers a pull of the stored file it, a local manifest or a
// blob the registry streams itself, with 304 when the request's
// If-None-Match or If-Modified-Since finds it unchanged, and reports
// whether it did. 7.x answers a pull's conditional request as its file
// download does (ignoredOnPull), which compares one If-None-Match line at a
// time, less a W/ prefix and every quote, with the file's SHA-1, or with
// its MD5 when X-Artifactory-If-None-Match-MD5 asks, and reads
// If-Modified-Since only without If-None-Match
// (DownloadServiceImpl.isNotModified, 7.161.26; pkgroute.FileNotModified).
// So neither "*" nor a list of tags on one line matches, and a second line
// does: http.ServeContent, which decided these before #1483, matched the
// first two and read only the first line. The 304 is the one ServeContent
// wrote: the headers already set, less the body's, and Last-Modified only
// beside no ETag (pkgroute.WriteNotModified). A local blob through the
// server's download (Options.ServeBlob) and a remote blob or manifest
// (pkgroute.ServeStream) are decided by the same rule.
func notModified(w http.ResponseWriter, r *http.Request, it model.Item) bool {
	if !pkgroute.FileNotModified(r, []string{it.Actual.SHA1}, it.Actual.MD5, it.Modified) {
		return false
	}
	if !it.Modified.IsZero() {
		w.Header().Set("Last-Modified", it.Modified.UTC().Format(http.TimeFormat))
	}
	pkgroute.WriteNotModified(w)
	return true
}

// withoutValidators is r without the headers notModified decides, once it
// has decided them.
func withoutValidators(r *http.Request) *http.Request {
	return pkgroute.WithoutHeaders(r, "If-None-Match", "If-Modified-Since")
}

// ignoredOnPull are the request headers a pull ignores.
//
// Range and If-Range: the answer is 200 with the whole content and still
// Accept-Ranges: bytes. 7.71.4 answered a blob GET through
// /artifactory/api/docker/<repo>/v2/ with Range: bytes=0-250 that way, all
// 58 MB of it, and JFrog tracks Range on the Docker API as an open request
// (RTFACT-30157, open; RTFACT-30847, partial pulls, open). If-Range only
// ever narrows what Range asks for, so it goes with it. The item route on
// the same file applies Range (RTFACT-17182's 7.57.0 comment), and still
// does here. No source shows a manifest or a remote repository's answer to
// Range; they are taken to be the blob's [I].
//
// If-Match and If-Unmodified-Since: neither answers 412. 7.x answers a
// pull's conditional request as its file download answers one: a local
// manifest GET answered If-Modified-Since with 304 on 7.19.4, and JFrog
// fixed the remote and virtual manifest to answer it too (RTFACT-26059).
// That download decides from If-None-Match and If-Modified-Since alone
// (6.20's DownloadServiceImpl.isNotModified; 7.125.6's RTDEV-64461 names
// no other precondition), and JFrog deferred RTFACT-17259, which asked for
// If-Match and If-Unmodified-Since. A local blob ignored both through the
// item kit's download already (#1314); a manifest, which the registry
// serves itself, and a remote blob or manifest, served from the remote's
// stream, answer as it does (#1437), and decide If-None-Match and
// If-Modified-Since by its rule too (notModified, #1469, #1483). No capture
// shows a manifest's or a remote's answer to either [I]. Neither
// specification asks a registry to evaluate them on a pull: the Docker
// Registry HTTP API asks a blob GET for caching validators, and the OCI
// distribution specification names conditional requests only for a
// client's push of a referrers tag.
var ignoredOnPull = []string{"Range", "If-Range", "If-Match", "If-Unmodified-Since"}

// setFileHeaders sets the headers 7.x sends on a blob or manifest through
// the Docker API to name the file: X-Artifactory-Filename and a
// Content-Disposition with the quoted name alone. 7.x sends no filename*
// part there, though a Maven download from the same server does (blob GETs
// of RTFACT-29977 on 7.55.8 and RTFACT-30157 on 7.71.4, a manifest HEAD of
// RTFACT-27199 on 7.33.12), so blob.Disposition's form, which the item route
// sends, is not used. Every name here is sha256__<hex>, manifest.json or
// list.manifest.json, which need no escaping.
func setFileHeaders(h http.Header, name string) {
	h.Set("X-Artifactory-Filename", name)
	h.Set("Content-Disposition", `attachment; filename="`+name+`"`)
}

// noStore is the Cache-Control 7.x sends on a manifest, whose tag can move:
// RTFACT-23106 (7.5.7) and RTFACT-27199 (7.33.12) show it on a local
// manifest. A blob, addressed by its content, carries none.
const noStore = "no-store"

// manifestFileFor is the file name 7.x gives a manifest of mediaType: an
// image index or manifest list is list.manifest.json and anything else
// manifest.json, as a local push stores them (layout.Manifest.File). A
// remote repository's cache keeps every manifest as manifest.json, unlike
// 7.x's, so a remote manifest is named from its media type rather than from
// the cached item [I].
func manifestFileFor(mediaType string) string {
	if mediaType == layout.MediaTypeOCIIndex || mediaType == layout.MediaTypeDockerList {
		return layout.ListManifestFile
	}
	return layout.ManifestFile
}
