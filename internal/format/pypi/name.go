package pypi

import (
	"regexp"
	"strings"
	"unicode"
	"unicode/utf8"
)

// namePattern is a valid project name (PEP 508; "Names and normalization"):
// ASCII letters and digits, with ".", "-" and "_" inside. A name the
// metadata gives otherwise is no name the format indexes, since it would
// make no folder of the canonical path, nor a project pip could ask for.
// [I] What 7.x indexes for such a name, or for a version that would name
// another folder, is unknown (#216).
var namePattern = regexp.MustCompile(`(?i)^([a-z0-9]|[a-z0-9][a-z0-9._-]*[a-z0-9])$`)

// separators are the runs PEP 503 folds into one "-".
var separators = regexp.MustCompile(`[-_.]+`)

// ValidName reports whether name is a valid project name.
func ValidName(name string) bool { return namePattern.MatchString(name) }

// Normalize is a project name as PEP 503 normalises it: lower case, with
// every run of ".", "-" and "_" one "-". The package index keeps each
// project under it, so files whose metadata names normalise alike are one
// project, as 7.x finds them by pypi.normalized.name (RTFACT-29892).
func Normalize(name string) string {
	return strings.ToLower(separators.ReplaceAllString(name, "-"))
}

// maxVersion bounds a version the index keeps: PEP 440 sets no limit, and
// a version is a path segment of its canonical path.
const maxVersion = 256

// validVersion reports whether v can be a version of the index: one path
// segment of printable characters without space, which a PEP 440 version
// always is. A version PEP 440 cannot read is kept as written (decision
// 0037), so the check keeps it from naming another folder.
func validVersion(v string) bool {
	if v == "" || len(v) > maxVersion || v == "." || v == ".." || !utf8.ValidString(v) {
		return false
	}
	return !strings.ContainsFunc(v, func(r rune) bool {
		return r == '/' || r == '\\' || unicode.IsSpace(r) || !unicode.IsPrint(r)
	})
}

// pep440 is the canonical PEP 440 grammar published with the
// specification, read without regard to case.
var pep440 = regexp.MustCompile(`(?i)^v?` +
	`(?:(?P<epoch>[0-9]+)!)?` +
	`(?P<release>[0-9]+(?:\.[0-9]+)*)` +
	`(?:[-_.]?(?P<pre_l>alpha|a|beta|b|preview|pre|c|rc)[-_.]?(?P<pre_n>[0-9]+)?)?` +
	`(?:-(?P<post_n1>[0-9]+)|[-_.]?(?P<post_l>post|rev|r)[-_.]?(?P<post_n2>[0-9]+)?)?` +
	`(?:[-_.]?(?P<dev_l>dev)[-_.]?(?P<dev_n>[0-9]+)?)?` +
	`(?:\+(?P<local>[a-z0-9]+(?:[-_.][a-z0-9]+)*))?$`)

// prePhases are the pre-release spellings PEP 440 normalises.
var prePhases = map[string]string{
	"a": "a", "alpha": "a", "b": "b", "beta": "b",
	"c": "rc", "rc": "rc", "pre": "rc", "preview": "rc",
}

// NormalizeVersion is v in the normal form PEP 440 gives it, as the
// packaging library prints a Version: "1.0-ALPHA.1" is "1.0a1", "v1.0" is
// "1.0" and "1!2.0" keeps its epoch. A version PEP 440 cannot read is
// returned as written, as decision 0037 indexes it.
func NormalizeVersion(v string) string {
	m := pep440.FindStringSubmatch(strings.TrimSpace(v))
	if m == nil {
		return v
	}
	g := func(name string) string { return m[pep440.SubexpIndex(name)] }
	var b strings.Builder
	if e := number(g("epoch")); e != "0" {
		b.WriteString(e + "!")
	}
	for i, r := range strings.Split(g("release"), ".") {
		if i > 0 {
			b.WriteByte('.')
		}
		b.WriteString(number(r))
	}
	if l := g("pre_l"); l != "" {
		b.WriteString(prePhases[strings.ToLower(l)] + number(g("pre_n")))
	}
	if g("post_n1") != "" || g("post_l") != "" {
		b.WriteString(".post" + number(g("post_n1")+g("post_n2")))
	}
	if g("dev_l") != "" {
		b.WriteString(".dev" + number(g("dev_n")))
	}
	if l := g("local"); l != "" {
		b.WriteString("+" + strings.ToLower(separators.ReplaceAllString(l, ".")))
	}
	return b.String()
}

// number is a run of digits as an integer prints it: without leading
// zeros, and "0" for none. A string keeps numbers past any integer type.
func number(digits string) string {
	if n := strings.TrimLeft(digits, "0"); n != "" {
		return n
	}
	return "0"
}

// CanonicalPath is where an upload through the upload API stores a file
// (decision 0037): <normalised name>/<Version as the metadata writes
// it>/<file name as sent>. 7.x stores a twine upload at
// <name>/<version>/<file> with "-" for "_" in the folder (RTFACT-20390,
// RTFACT-29973). [I] Its folder for a name with upper case or dots is
// unknown (#216).
func CanonicalPath(name, version, file string) string {
	return Normalize(name) + "/" + version + "/" + file
}
