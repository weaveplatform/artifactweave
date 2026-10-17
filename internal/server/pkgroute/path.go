package pkgroute

import (
	"fmt"
	"net/http"
	"net/url"
	"strings"

	"github.com/weaveplatform/artifactweave/internal/server/apierror"
	"github.com/weaveplatform/artifactweave/pkg/types"
)

// Path is an escaped path with its matrix parameters parsed out.
type Path struct {
	// Segments are the path's segments, each unescaped on its own: an
	// encoded slash, as npm sends in "@scope%2fname", stays inside its
	// segment, so a protocol can tell it from a literal one.
	Segments []string
	// Folder reports a trailing slash, or matrix parameters after one
	// ("a/b/;k=v").
	Folder bool
	// Props are the matrix parameters (";key=value" after any segment), and
	// Mandatory the keys sent as "key+=value", which a download requires the
	// item to carry.
	Props     types.Properties
	Mandatory map[string]bool
}

// String joins the segments with slashes, which is the item path the
// segments name: an encoded slash becomes a folder there, as it does for
// the item routes.
func (p Path) String() string { return strings.Join(p.Segments, "/") }

// ParsePath parses an escaped path, as r.URL.EscapedPath() gives it after a
// route's prefix. Working on the escaped form keeps an encoded ";" or "/"
// inside the name it belongs to. A malformed escape or matrix parameter
// answers 400.
func ParsePath(escaped string) (Path, error) {
	p := Path{
		Folder: strings.HasSuffix(escaped, "/"), Props: types.Properties{},
		Mandatory: map[string]bool{},
	}
	trimmed := strings.Trim(escaped, "/")
	if trimmed == "" {
		return p, nil
	}
	segments := strings.Split(trimmed, "/")
	for i, seg := range segments {
		name, params, _ := strings.Cut(seg, ";")
		n, err := url.PathUnescape(name)
		if err != nil {
			return p, badRequest("malformed path: %v", err)
		}
		if n == "" && i == len(segments)-1 && params != "" {
			// "a/b/;k=v": matrix parameters after a folder's slash.
			p.Folder = true
		} else {
			p.Segments = append(p.Segments, n)
		}
		if params == "" {
			continue
		}
		if err := p.addMatrix(params); err != nil {
			return p, err
		}
	}
	return p, nil
}

// addMatrix parses "k=v1,v2;k2+=v3". Commas separate values unless escaped
// with a backslash, as in the properties API. A value is form-decoded, so "+"
// is a space: the JFrog CLI escapes values with url.QueryEscape, and
// Artifactory decodes them with URLDecoder (research 17). Keys keep "+",
// whose trailing use marks a mandatory property.
func (p *Path) addMatrix(params string) error {
	for _, param := range strings.Split(params, ";") {
		if param == "" {
			continue
		}
		k, v, _ := strings.Cut(param, "=")
		k, err := url.PathUnescape(k)
		if err != nil {
			return badRequest("malformed matrix parameter %q", param)
		}
		mandatory := false
		if base, ok := strings.CutSuffix(k, "+"); ok {
			k, mandatory = base, true
		}
		if err := types.ValidatePropertyKey(k); err != nil {
			return badRequest("%v", err)
		}
		v, err = url.QueryUnescape(v)
		if err != nil {
			return badRequest("malformed matrix parameter %q", param)
		}
		p.Props[k] = append(p.Props[k], SplitValues(v)...)
		if mandatory {
			p.Mandatory[k] = true
		}
	}
	return nil
}

// SplitValues splits a property value list at its commas; a backslash
// escapes the character after it, so "a\,b" is one value.
func SplitValues(v string) []string {
	var out []string
	var cur strings.Builder
	for i := 0; i < len(v); i++ {
		switch {
		case v[i] == '\\' && i+1 < len(v):
			i++
			cur.WriteByte(v[i])
		case v[i] == ',':
			out = append(out, cur.String())
			cur.Reset()
		default:
			cur.WriteByte(v[i])
		}
	}
	return append(out, cur.String())
}

// DeclaredChecksums reads the checksums a deploy declares in its
// X-Checksum-Sha1, -Sha256 and -Md5 headers. X-Checksum names its
// algorithm by length; the JFrog CLI sends the SHA-256 in it.
func DeclaredChecksums(h http.Header) types.Checksums {
	declared := types.Checksums{
		SHA1: h.Get("X-Checksum-Sha1"), SHA256: h.Get("X-Checksum-Sha256"),
		MD5: h.Get("X-Checksum-Md5"),
	}
	if v := strings.TrimSpace(h.Get("X-Checksum")); v != "" {
		switch len(v) {
		case 64:
			declared.SHA256 = v
		case 40:
			declared.SHA1 = v
		default:
			declared.MD5 = v
		}
	}
	return declared
}

// SetChecksums sets the X-Checksum-Sha1, -Sha256 and -Md5 headers a
// download and a protocol deploy answer with; jf go-publish reads the
// SHA-256 from a deploy's (research 26 section 3.1).
func SetChecksums(h http.Header, c types.Checksums) {
	h.Set("X-Checksum-Sha1", c.SHA1)
	h.Set("X-Checksum-Sha256", c.SHA256)
	h.Set("X-Checksum-Md5", c.MD5)
}

func badRequest(format string, a ...any) error {
	return apierror.New(http.StatusBadRequest, fmt.Sprintf(format, a...))
}
