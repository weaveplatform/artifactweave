package itemkit

import (
	"net/http"
	"path"
	"slices"
	"strings"

	"github.com/weaveplatform/artifactweave/internal/artifacts/api/repo"
	"github.com/weaveplatform/artifactweave/internal/platform/httpkit"
)

// FilenameHeader is the X-Artifactory-Filename value of a file named name:
// the name query-encoded, as every 7.161.26 response class writes it
// (HttpArtifactoryResponse, JerseyArtifactoryResponse and
// DockerManifestResponse.setFilename all call BasicHttpUtils.encodeQuery),
// so a space is %20 and a character outside ASCII its UTF-8 bytes, while
// '+', '(' and ';' stay (httpkit.EncodeQuery).
func FilenameHeader(name string) string { return httpkit.EncodeQuery(name) }

// ItemDisposition is the Content-Disposition the item route,
// /artifactory/<repo>/<path>, names a file with on a repository that does
// not allow content browsing (FileDisposition). 7.161.26's
// HttpArtifactoryResponse.setContentDispositionAttachment writes
//
//	attachment; filename="<name>"; filename*=UTF-8''<encodeQuery(name)>
//
// the quoted name as it is (RTFACT-29749 had JFrog fix a %20 there on
// 7.59.9) and filename* query-encoded, '@' raw as RTFACT-25859 shows it on
// 7.17.11. A name with a character outside printable ASCII is named by
// filename* alone, every byte but RFC 3986's unreserved characters
// percent-encoded: RequestResponseHelper.setHeaderForNonAscii replaces the
// header so on a GET and a HEAD, and its tests
// (RequestResponseHelperTest) pin the form. 7.x's 304 skips that step and
// would send the raw name inside the quotes; this sends filename* there
// too, as the package download service's form does [I].
func ItemDisposition(name string) string {
	if !httpkit.PrintableASCII(name) {
		return nonASCIIDisposition("attachment", name)
	}
	return `attachment; filename="` + httpkit.EscapeQuoted(name) + `"; filename*=UTF-8''` +
		httpkit.EncodeQuery(name)
}

// nonASCIIDisposition is setHeaderForNonAscii's value for a file named
// name, of the disposition type kind.
func nonASCIIDisposition(kind, name string) string {
	return kind + "; filename*=UTF-8''" + httpkit.EncodeUnreserved(name)
}

// Browsable reports whether c allows content browsing
// (archiveBrowsingEnabled, "Allow Artifact Content Browsing"): a local,
// federated or remote repository with the flag on. 7.161.26's
// RequestResponseHelper.contentBrowsingDisabled reads the flag of a real
// repository's descriptor alone, so a virtual repository never allows it,
// whatever it stores; a download through one answers by the member that
// holds the file (its response repo path). A remote's cache reads the
// remote's flag (LocalCacheRepoDescriptor.isArchiveBrowsingEnabled).
func Browsable(c repo.Config) bool {
	return c.Class != repo.Virtual && c.ArchiveBrowsingEnabled != nil && *c.ArchiveBrowsingEnabled
}

// FileDisposition is the Content-Disposition a GET or HEAD of the file
// named name from the repository c answers with, "" for none: form's value
// on a repository that does not allow content browsing; none on one that
// does, where a browser shows the file rather than saving it; and there,
// for a name outside printable ASCII, inline with filename* alone.
// 7.161.26's RequestResponseHelper.setBasicHeaders sends the attachment
// only when contentBrowsingDisabled holds ("content disposition is not set
// only for archive resources when archived browsing is enabled"), and
// setHeaderForNonAscii then names a non-ASCII file inline there, on the
// item route and the package download service alike. A 304 never reaches
// setHeaderForNonAscii, so it names no file on a browsable repository
// (notModifiedDisposition).
func FileDisposition(c repo.Config, name string, form func(string) string) string {
	switch {
	case !Browsable(c):
		return form(name)
	case httpkit.PrintableASCII(name):
		return ""
	default:
		return nonASCIIDisposition("inline", name)
	}
}

// setDisposition sets h's Content-Disposition to d, or removes it when d
// is "".
func setDisposition(h http.Header, d string) {
	if d == "" {
		h.Del("Content-Disposition")
		return
	}
	h.Set("Content-Disposition", d)
}

// notModifiedDisposition is the Content-Disposition of a 304 for the file
// named name from c: setBasicHeaders' alone, which is none on a browsable
// repository.
func notModifiedDisposition(c repo.Config, name string, form func(string) string) string {
	if Browsable(c) {
		return ""
	}
	return form(name)
}

// contentSecurityPolicy is the Content-Security-Policy a browser gets with
// a file from a browsable repository: the default of 7.x's
// artifactory.repo.browsing.content.security.policy (ConstantValues,
// 7.161.26), which runs an HTML page sandboxed. JFrog closed RTFACT-26502
// (7.41.13) saying so: newer versions run such pages "in a sandboxed
// environment in order to prevent potential attacks such as XSS or CSRF".
// The property is not offered here (DV-202).
const contentSecurityPolicy = "sandbox"

// browserEngines is the default of 7.x's
// artifactory.redirect.native.browser.engine.list (ConstantValues,
// 7.161.26), which BrowserRequestHelper.isBrowserUserAgent matches a
// User-Agent against (DV-202).
var browserEngines = []string{"webkit", "blink", "edgehtml", "trident", "gecko", "flow", "goanna"}

// browserAgent reports whether r comes from a browser as 7.161.26's
// BrowserRequestHelper.isBrowserUserAgent decides it: a User-Agent that,
// lower-cased, starts with "mozilla" and names one of browserEngines.
func browserAgent(r *http.Request) bool {
	agent := strings.ToLower(r.Header.Get("User-Agent"))
	return strings.HasPrefix(agent, "mozilla") &&
		slices.ContainsFunc(
			browserEngines,
			func(e string) bool { return strings.Contains(agent, e) },
		)
}

// browserType is the type 7.161.26's BrowserRequestHelper.getMimeType
// gives a browser for a file named name that it can show as text:
// application/xml for a .pom and text/plain for a .md5, .sha1 or .md
// (RTFACT-8743, fixed in 4.9.0); "" keeps the file's own type.
func browserType(name string) string {
	switch path.Ext(name) {
	case ".pom":
		return "application/xml"
	case ".md5", ".sha1", ".md":
		return "text/plain"
	}
	return ""
}

// browserView sets what 7.161.26's RequestResponseHelper.getModifiedMimeType
// sets on a GET with a body, of the file or checksum file at name from the
// repository c, when a browser asks and c allows content browsing: the
// sandbox policy, and a type a browser shows for the extensions
// browserType names. getModifiedMimeType runs in sendBodyResponse and
// sendBodyResponseForChecksum alone, before the range is read, so a HEAD
// and a 304 get neither, a redirect never reaches it, and a 416 keeps the
// policy.
func browserView(h http.Header, r *http.Request, c repo.Config, name string) {
	if r.Method != http.MethodGet || !Browsable(c) || !browserAgent(r) {
		return
	}
	h.Set("Content-Security-Policy", contentSecurityPolicy)
	if t := browserType(name); t != "" {
		h.Set("Content-Type", t)
	}
}
