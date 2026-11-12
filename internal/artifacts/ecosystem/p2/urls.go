package p2

import (
	"cmp"
	"net/url"
	"strings"
)

// A P2 virtual repository's p2Urls name the update sites it aggregates, as
// 7.x's definition of one does (P2 Repositories, "Use Multiple P2 Remote
// Repositories with the Same Base URL"; p2.urls in the YAML form). An entry
// names a local repository's site as local://<key>[/<suffix>], the form
// 7.x's UI writes for a local member and its Path Suffix (RTFACT-29915), or
// is an http or https URL that a member remote's url starts. 7.x serves the
// site under the repository's key and the rest of the URL, as in
// p2-virtual/download.eclipse.org/releases/luna/p2.index (RTFACT-7631).

// localSite begins a p2Urls entry that names a local repository.
const localSite = "local://"

// SiteURL is one p2Urls entry that ParseSiteURL accepted.
type SiteURL struct {
	// Raw is the entry as configured. A remote covers it by its text
	// (Covers), as 7.x's UI matches a remote's url to it.
	Raw string
	// Key and Suffix name a local repository and the site's path in it,
	// possibly empty, for a local:// entry.
	Key, Suffix string
	// URL is the parsed entry for an http or https one, nil for a local one.
	URL *url.URL
}

// Local reports whether s names a local repository's site.
func (s SiteURL) Local() bool { return s.URL == nil }

// ParseSiteURL checks raw, one p2Urls entry: local://<key>[/<suffix>], or an
// absolute http or https URL with a host and no user information, query or
// fragment. The path a client is sent to must resolve as given: each
// segment but a trailing empty one is neither empty, "." nor "..", even
// unescaped, and holds only what a URL path may carry unescaped or as a
// %XX escape. Anything else is ErrInvalid.
func ParseSiteURL(raw string) (SiteURL, error) {
	// url.Parse takes a space or a quote in a path and escapes it again,
	// so what a URL may not hold unescaped is refused in the raw entry.
	if strings.ContainsFunc(raw, func(r rune) bool {
		return r <= ' ' || r >= 0x7f || strings.ContainsRune("?#\\\"<>^`{|}", r)
	}) {
		return SiteURL{}, ErrInvalid
	}
	if rest, ok := strings.CutPrefix(raw, localSite); ok {
		key, suffix, _ := strings.Cut(rest, "/")
		if key == "" || strings.Contains(key, "%") || !sitePath(suffix) {
			return SiteURL{}, ErrInvalid
		}
		return SiteURL{Raw: raw, Key: key, Suffix: suffix}, nil
	}
	u, err := url.Parse(raw)
	if err != nil || (u.Scheme != "http" && u.Scheme != "https") || u.Host == "" ||
		u.User != nil || u.Opaque != "" || !sitePath(strings.TrimPrefix(u.EscapedPath(), "/")) {
		return SiteURL{}, ErrInvalid
	}
	return SiteURL{Raw: raw, URL: u}, nil
}

// sitePath reports whether p, a path relative to a repository, is one a
// client resolves as given, as ParseSiteURL describes.
func sitePath(p string) bool {
	if p == "" {
		return true
	}
	for _, seg := range strings.Split(strings.TrimSuffix(p, "/"), "/") {
		plain, err := url.PathUnescape(seg)
		if err != nil || plain == "" || plain == "." || plain == ".." ||
			strings.ContainsAny(plain, "/\\") {
			return false
		}
		for _, c := range []byte(seg) {
			if !pathByte(c) {
				return false
			}
		}
	}
	return true
}

// pathByte reports whether c may stand unescaped in a URL path segment
// (RFC 3986 pchar), or begins a %XX escape.
func pathByte(c byte) bool {
	return 'a' <= c && c <= 'z' || 'A' <= c && c <= 'Z' || '0' <= c && c <= '9' ||
		strings.IndexByte("-._~!$&'()*+,;=:@%", c) >= 0
}

// Covers reports whether a remote repository whose url is base serves the
// update site at raw, an http or https p2Urls entry, and returns the site's
// path under base. Both must name the same scheme and host, which compare
// without regard to case, and base's path must start raw's at a path
// boundary: it ends in "/", or raw's goes on with "/" or ends there. An
// empty path is "/", as RFC 3986 has both for http, so the remote created
// for https://host.example/ covers https://host.example. A site archive's
// entry (site.zip!/) is covered only by a remote whose url is that entry,
// the one form under which a remote serves an archive's files [I].
func Covers(base, raw string) (string, bool) {
	bn, rn := originLen(base), originLen(raw)
	if bn == 0 || !strings.EqualFold(base[:bn], raw[:rn]) {
		return "", false
	}
	basePath, rawPath := cmp.Or(base[bn:], "/"), cmp.Or(raw[rn:], "/")
	if strings.Contains(raw, "!/") {
		return "", basePath == rawPath
	}
	rest, ok := strings.CutPrefix(rawPath, basePath)
	if !ok || (!strings.HasSuffix(basePath, "/") && rest != "" && !strings.HasPrefix(rest, "/")) {
		return "", false
	}
	return strings.TrimPrefix(rest, "/"), true
}

// originLen returns the length of u's scheme and authority, up to the "/"
// that starts its path, or 0 when u has no "://".
func originLen(u string) int {
	scheme, after, ok := strings.Cut(u, "://")
	if !ok {
		return 0
	}
	if i := strings.IndexByte(after, '/'); i >= 0 {
		return len(scheme) + len("://") + i
	}
	return len(u)
}

// Member is a virtual repository member as the p2Urls rules see it: its
// key, and its url when it is a P2 remote repository, which is what can
// cover an http or https entry.
type Member struct {
	Key, URL string
}

// SiteChild returns the member that serves s among members, which are in
// the virtual repository's order, and the composite child location of the
// site, relative to the virtual repository. A local:// entry is served by
// the member with its key, at <key>[/<suffix>]; an http or https entry by
// the first member whose url covers it, at <key>[/<rest of the URL>]. ok
// is false when no member serves s.
func SiteChild(s SiteURL, members []Member) (key, child string, ok bool) {
	for _, m := range members {
		var rest string
		switch {
		case s.Local() && m.Key == s.Key:
			rest = s.Suffix
		case !s.Local() && m.URL != "":
			var covers bool
			if rest, covers = Covers(m.URL, s.Raw); !covers {
				continue
			}
		default:
			continue
		}
		child = url.PathEscape(m.Key)
		if rest != "" {
			child += "/" + rest
		}
		return m.Key, child, true
	}
	return "", "", false
}
