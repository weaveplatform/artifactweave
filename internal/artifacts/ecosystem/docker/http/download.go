package dockerhttp

import (
	"net/http"

	"github.com/weaveplatform/artifactweave/internal/artifacts/adapters/http/pkgroute"
	layout "github.com/weaveplatform/artifactweave/internal/artifacts/ecosystem/docker"
)

// wholeRequest is r as the registry serves a blob or a manifest: without
// Range and If-Range, whatever the method, so the answer is 200 with the
// whole content and still Accept-Ranges: bytes. 7.71.4 answered a blob GET
// through /artifactory/api/docker/<repo>/v2/ with Range: bytes=0-250 that
// way, all 58 MB of it, and JFrog tracks Range on the Docker API as an open
// request (RTFACT-30157, open; RTFACT-30847, partial pulls, open). The item
// route on the same file applies Range (RTFACT-17182's 7.57.0 comment), and
// still does here. No source shows a manifest or a remote repository's
// answer to Range; they are taken to be the blob's [I].
func wholeRequest(r *http.Request) *http.Request {
	return pkgroute.WithoutRange(r)
}

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
