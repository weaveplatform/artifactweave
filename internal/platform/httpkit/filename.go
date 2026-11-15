package httpkit

import "strings"

// queryAllowed are the bytes EncodeQuery keeps as they are: RFC 2396's
// unreserved characters (letters, digits and -_.!~*'()) and its reserved
// ones (;/?:@&=+$,), the set commons-httpclient's URI.allowed_query holds.
var queryAllowed = func() (set [256]bool) {
	for _, c := range []byte("-_.!~*'();/?:@&=+$,") {
		set[c] = true
	}
	for c := range 256 {
		set[c] = set[c] || isAlphaNum(byte(c))
	}
	return set
}()

// EncodeQuery percent-encodes s as Artifactory's BasicHttpUtils.encodeQuery
// writes a file's name into X-Artifactory-Filename and into the item
// route's filename* (7.161.26, URIUtil.encodeQuery in UTF-8): queryAllowed
// stay, and every other byte, a space, '"', '%' and each byte of a
// character outside ASCII among them, becomes %XX in upper-case hex. It
// keeps '@', which RTFACT-25859 (7.17.11) shows raw in filename*, and the
// sub-delimiters url.PathEscape encodes, so pkg(1).zip stays pkg(1).zip.
func EncodeQuery(s string) string {
	return percentEncode(s, &queryAllowed)
}

// unreservedAllowed are RFC 3986's unreserved characters: letters, digits
// and -._~.
var unreservedAllowed = func() (set [256]bool) {
	for _, c := range []byte("-._~") {
		set[c] = true
	}
	for c := range 256 {
		set[c] = set[c] || isAlphaNum(byte(c))
	}
	return set
}()

// EncodeUnreserved percent-encodes every byte of s but RFC 3986's
// unreserved characters, in upper-case hex, as Spring's UriUtils.encode
// does for a URI component. 7.161.26's
// RequestResponseHelper.setHeaderForNonAscii writes a name outside
// printable ASCII into filename* so; url.PathEscape keeps sub-delimiters
// such as '+' and '&', which UriUtils.encode encodes.
func EncodeUnreserved(s string) string {
	return percentEncode(s, &unreservedAllowed)
}

// quotedPairs escapes the two characters a quoted-string cannot hold bare
// (RFC 9110 section 5.6.4).
var quotedPairs = strings.NewReplacer(`\`, `\\`, `"`, `\"`)

// EscapeQuoted escapes a quote or a backslash in s as a quoted pair, so
// that s can go inside a quoted-string whole. 7.x writes a file's name
// into filename="..." as it is, but its PathValidator refuses a path that
// holds either character, so it never writes one; here such a name cannot
// end the quoted string early.
func EscapeQuoted(s string) string { return quotedPairs.Replace(s) }

// PrintableASCII reports whether every byte of s is printable ASCII, ' '
// to '~', as commons-lang's StringUtils.isAsciiPrintable decides for the
// file names 7.x's setHeaderForNonAscii leaves in their quoted form.
func PrintableASCII(s string) bool {
	for i := range len(s) {
		if s[i] < ' ' || s[i] > '~' {
			return false
		}
	}
	return true
}

func percentEncode(s string, allowed *[256]bool) string {
	const hex = "0123456789ABCDEF"
	var b strings.Builder
	b.Grow(len(s))
	for i := range len(s) {
		c := s[i]
		if allowed[c] {
			b.WriteByte(c)
			continue
		}
		b.WriteByte('%')
		b.WriteByte(hex[c>>4])
		b.WriteByte(hex[c&15])
	}
	return b.String()
}

func isAlphaNum(c byte) bool {
	return 'a' <= c && c <= 'z' || 'A' <= c && c <= 'Z' || '0' <= c && c <= '9'
}
