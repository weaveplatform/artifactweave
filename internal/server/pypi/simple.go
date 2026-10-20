package pypi

import (
	"bytes"
	"encoding/json"
	"fmt"
	"html"
	"mime"
	"net/http"
	"strconv"
	"strings"

	pypifmt "github.com/weaveplatform/artifactweave/internal/format/pypi"
	"github.com/weaveplatform/artifactweave/internal/server/apierror"
	"github.com/weaveplatform/artifactweave/internal/server/pkgroute"
	"github.com/weaveplatform/artifactweave/pkg/types"
)

// The simple index's media types (PEP 691). 7.x sends its HTML as
// text/html (RTFACT-29209), which PEP 691 makes an alias of the v1 HTML
// type, and which pip and uv both accept.
const (
	htmlType       = "text/html"
	jsonType       = "application/vnd.pypi.simple.v1+json"
	jsonLatest     = "application/vnd.pypi.simple.latest+json"
	htmlV1Type     = "application/vnd.pypi.simple.v1+html"
	htmlLatestType = "application/vnd.pypi.simple.latest+html"
)

// apiVersion is the JSON form's meta.api-version: 1.1, as its versions,
// size and upload-time are what the index already holds, and uv's
// exclude-newer needs upload-time (RTFACT-31464). [I] What 7.x writes is
// unknown (#216).
const apiVersion = "1.1"

// list answers the project list: one entry per project with a file the
// caller may read. A caller who may read nothing in the repository is
// refused, so pip asks for credentials (RTFACT-17764).
func (h handler) list(w http.ResponseWriter, r *http.Request) {
	if !h.k.RequireFolder(w, r, "", types.ActionRead) {
		return
	}
	names, err := projects(r.Context(), h.k.DB(), pkgroute.RequestFrom(r).Repo.Key, h.reads(r))
	if err != nil {
		h.k.Error(w, r, err)
		return
	}
	if h.json(r) {
		writePage(w, r, jsonType, true, listJSON(names))
		return
	}
	writePage(w, r, htmlType, true, listHTML(names))
}

// project answers a project's page, name being normalised. A caller who
// may read nothing in the repository is refused, and one that may but
// finds no file of the project it can read gets 404, so a page never shows
// a project to a caller who could not install it (decision 0037).
func (h handler) project(w http.ResponseWriter, r *http.Request, name string) {
	if !h.k.RequireFolder(w, r, "", types.ActionRead) {
		return
	}
	p, ok, err := load(r.Context(), h.k.DB(), pkgroute.RequestFrom(r).Repo.Key, name, h.reads(r))
	switch {
	case err != nil:
		h.k.Error(w, r, err)
	case !ok:
		h.k.Error(w, r, apierror.New(http.StatusNotFound,
			fmt.Sprintf("Project '%s' was not found", name)))
	case h.json(r):
		writePage(w, r, jsonType, true, projectJSON(p, h.format.CoreMetadata))
	default:
		writePage(w, r, htmlType, true, projectHTML(p, h.format.CoreMetadata))
	}
}

// json reports whether r gets the JSON form: only with pypi.simple_json
// on, as 7.x falls back to HTML without its setting (pypi-repositories,
// "Limitations"), and then when Accept names a JSON type with a quality
// above 0 and not below that of the best HTML type it accepts. pip and uv
// rank JSON first. Nothing answers 406, which 7.x did before it served
// JSON (RTFACT-30869). [I] The answer without Accept, HTML, is inferred
// (#216).
func (h handler) json(r *http.Request) bool {
	if !h.format.SimpleJSON {
		return false
	}
	jsonQ, htmlQ := 0.0, 0.0
	for entry := range strings.SplitSeq(r.Header.Get("Accept"), ",") {
		mt, params, err := mime.ParseMediaType(strings.TrimSpace(entry))
		if err != nil {
			continue
		}
		q := 1.0
		if v, ok := params["q"]; ok {
			if q, err = strconv.ParseFloat(v, 64); err != nil {
				continue
			}
		}
		switch mt {
		case jsonType, jsonLatest:
			jsonQ = max(jsonQ, q)
		case htmlType, htmlV1Type, htmlLatestType, "text/*", "application/*", "*/*":
			htmlQ = max(htmlQ, q)
		}
	}
	return jsonQ > 0 && jsonQ >= htmlQ
}

// pageHead starts every HTML page as 7.x's does: the doctype pip 22 needs
// (RTFACT-26750), and the head of RTFACT-30288's capture.
const pageHead = "<!DOCTYPE html>\n<html><head><title>Simple Index</title>" +
	`<meta name="api-version" value="2" /></head><body>` + "\n"

const pageTail = "</body></html>\n"

// listHTML is the project list: an anchor per project, to its page.
// [I] 7.x's project list is unknown (#216); the trailing slash spares a
// browser the redirect.
func listHTML(names []string) []byte {
	var b strings.Builder
	b.WriteString(pageHead)
	for _, n := range names {
		e := html.EscapeString(n)
		fmt.Fprintf(&b, `<a href="%s/" rel="internal">%s</a>`+"\n", e, e)
	}
	b.WriteString(pageTail)
	return []byte(b.String())
}

// projectHTML is a project's page: an anchor per file, one per line
// without <br />, as RTFACT-30288 shows 7.x's, linking to the file
// relative to the page with its SHA-256 (RTFACT-18495), and with the
// attributes PEP 503, 592 and 658 define. Every value is HTML-escaped: an
// unescaped "<" in Requires-Python broke 6.x's pages (RTFACT-17770).
func projectHTML(p project, core bool) []byte {
	var b strings.Builder
	b.WriteString(pageHead)
	for _, rel := range p.releases {
		for _, f := range rel.files {
			fmt.Fprintf(&b, `<a href="%s#sha256=%s"`, html.EscapeString(link(f.Path)), f.SHA256)
			if f.RequiresPython != "" {
				fmt.Fprintf(&b, ` data-requires-python="%s"`, html.EscapeString(f.RequiresPython))
			}
			if f.Yanked {
				fmt.Fprintf(&b, ` data-yanked="%s"`, html.EscapeString(f.YankedReason))
			}
			if core && f.Core {
				fmt.Fprintf(
					&b,
					` data-dist-info-metadata="sha256=%s" data-core-metadata="sha256=%s"`,
					f.Metadata,
					f.Metadata,
				)
			}
			fmt.Fprintf(&b, ` rel="internal">%s</a>`+"\n", html.EscapeString(f.Name))
		}
	}
	b.WriteString(pageTail)
	return []byte(b.String())
}

// link is a file's URL relative to its project's page,
// /api/pypi/<repo>/simple/<project>/: the file's path in the repository,
// which the download route serves (RTFACT-30288).
func link(path string) string { return "../../" + escapePath(path) }

// listJSON is the project list in JSON (PEP 691).
func listJSON(names []string) []byte {
	out := listDoc{Meta: meta{apiVersion}, Projects: make([]listEntry, 0, len(names))}
	for _, n := range names {
		out.Projects = append(out.Projects, listEntry{n})
	}
	return encode(out)
}

// listDoc is the project list's JSON form.
type listDoc struct {
	Meta     meta        `json:"meta"`
	Projects []listEntry `json:"projects"`
}

type listEntry struct {
	Name string `json:"name"`
}

// meta is a JSON page's meta object.
//
//nolint:tagliatelle // PEP 691's names
type meta struct {
	APIVersion string `json:"api-version"`
}

// projectDoc is a project page's JSON form.
type projectDoc struct {
	Meta     meta       `json:"meta"`
	Name     string     `json:"name"`
	Files    []jsonFile `json:"files"`
	Versions []string   `json:"versions"`
}

// jsonFile is a file of a project's JSON page (PEP 691, 700 and 714).
//
//nolint:tagliatelle // PEP 691's names
type jsonFile struct {
	Filename       string            `json:"filename"`
	URL            string            `json:"url"`
	Hashes         map[string]string `json:"hashes"`
	RequiresPython string            `json:"requires-python,omitempty"`
	// Yanked is false, true, or the reason (PEP 592).
	Yanked     any    `json:"yanked"`
	Size       int64  `json:"size"`
	UploadTime string `json:"upload-time"`
	// CoreMetadata and DistInfoMetadata are {"sha256": ...}, or false for a
	// file without core metadata, with pypi.core_metadata on, and absent
	// with it off.
	CoreMetadata     any `json:"core-metadata,omitempty"`
	DistInfoMetadata any `json:"dist-info-metadata,omitempty"`
}

// uploadTime is PEP 700's upload-time format.
const uploadTime = "2006-01-02T15:04:05.000000Z"

// projectJSON is a project's page in JSON: the same files and links as
// the HTML page, and the versions they belong to.
func projectJSON(p project, core bool) []byte {
	out := projectDoc{
		Meta:     meta{apiVersion},
		Name:     p.name,
		Files:    []jsonFile{},
		Versions: []string{},
	}
	for _, rel := range p.releases {
		out.Versions = append(out.Versions, rel.version)
		for _, f := range rel.files {
			jf := jsonFile{
				Filename: f.Name, URL: link(f.Path), Hashes: map[string]string{"sha256": f.SHA256},
				RequiresPython: f.RequiresPython, Yanked: yanked(f), Size: f.Size,
				UploadTime: f.Uploaded.UTC().Format(uploadTime),
			}
			if core {
				var m any = false
				if f.Core {
					m = map[string]string{"sha256": f.Metadata}
				}
				jf.CoreMetadata, jf.DistInfoMetadata = m, m
			}
			out.Files = append(out.Files, jf)
		}
	}
	return encode(out)
}

// yanked is a file's PEP 592 yanked value: false, true, or the reason.
func yanked(f pypifmt.File) any {
	switch {
	case !f.Yanked:
		return false
	case f.YankedReason == "":
		return true
	}
	return f.YankedReason
}

// encode is v as JSON with HTML characters as written, so a
// Requires-Python of ">=3.9" reads as the metadata gives it. Every value
// given is built of strings, numbers and maps of them, which always
// encode.
func encode(v any) []byte {
	var b bytes.Buffer
	e := json.NewEncoder(&b)
	e.SetEscapeHTML(false)
	_ = e.Encode(v)
	return b.Bytes()
}
