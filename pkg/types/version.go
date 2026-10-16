package types

import (
	"regexp"
	"sort"
	"strings"
	"time"

	"github.com/weaveplatform/artifactweave/pkg/repo"
)

// VersionComparator orders the versions of one package (decision 0018).
// Compare returns a negative number when a sorts before b, zero when they
// are equivalent and a positive number otherwise. Retention
// (keepLastNVersions), tag limits, "latest" resolution and the registration
// endpoints all sort through it, so one ecosystem never borrows another's
// rules.
type VersionComparator interface {
	Compare(a, b PackageVersion) int
}

// StringComparator adapts a comparison of version strings.
type StringComparator func(a, b string) int

// Compare orders by the version string alone.
func (f StringComparator) Compare(a, b PackageVersion) int { return f(a.Version, b.Version) }

// CreatedOrder orders by creation time, then by version string so the order
// is total. Docker and OCI tags carry no ordering of their own; Artifactory
// sorts them by manifest timestamp.
type CreatedOrder struct{}

// Compare orders by Created, falling back to the tag name.
func (CreatedOrder) Compare(a, b PackageVersion) int {
	if c := a.Created.Compare(b.Created); c != 0 {
		return c
	}
	return strings.Compare(a.Version, b.Version)
}

// ComparatorFor returns the comparator a package type sorts with. Types
// without a documented scheme (generic and the ecosystems artifactweave does
// not implement) use Maven ordering, which is what Artifactory's
// layout-based version search applies to any layout with a [baseRev] token.
func ComparatorFor(t repo.PackageType) VersionComparator {
	switch t {
	case repo.NPM, repo.Go, repo.Helm, repo.HelmOCI, repo.Terraform:
		return StringComparator(CompareSemver)
	case repo.PyPI:
		return StringComparator(ComparePEP440)
	case repo.NuGet:
		return StringComparator(CompareNuGet)
	case repo.Docker, repo.OCI:
		return CreatedOrder{}
	default:
		return StringComparator(CompareMaven)
	}
}

// SortVersions sorts versions oldest first in place.
func SortVersions(c VersionComparator, versions []PackageVersion) {
	sort.SliceStable(
		versions,
		func(i, j int) bool { return c.Compare(versions[i], versions[j]) < 0 },
	)
}

// compareNumeric compares two strings of decimal digits of any length, so a
// version component wider than int64 (date-stamped builds, Go
// pseudo-version timestamps) never overflows.
func compareNumeric(a, b string) int {
	a = strings.TrimLeft(a, "0")
	b = strings.TrimLeft(b, "0")
	if len(a) != len(b) {
		return sign(len(a) - len(b))
	}
	return strings.Compare(a, b)
}

func isDigits(s string) bool {
	if s == "" {
		return false
	}
	for _, r := range s {
		if r < '0' || r > '9' {
			return false
		}
	}
	return true
}

func sign(n int) int {
	switch {
	case n < 0:
		return -1
	case n > 0:
		return 1
	default:
		return 0
	}
}

// compareInvalid orders strings a parser rejected: they sort before every
// valid version and lexically among themselves, so a stray tag never becomes
// "latest" and the order stays total.
func compareInvalid(a string, aOK bool, b string, bOK bool) (int, bool) {
	switch {
	case aOK && bOK:
		return 0, false
	case !aOK && !bOK:
		return strings.Compare(a, b), true
	case !aOK:
		return -1, true
	default:
		return 1, true
	}
}

// --- Semantic versioning (npm, Go, Helm, Terraform) and NuGet ---------------

type dotted struct {
	core []string
	pre  []string
}

// parseDotted reads up to maxCore numeric components and an optional
// pre-release, dropping build metadata. Missing components count as zero,
// which is NuGet's rule (1.0 == 1.0.0.0) and a leniency Helm charts in the
// wild rely on ("1.2").
func parseDotted(s string, maxCore int) (dotted, bool) {
	if i := strings.IndexByte(s, '+'); i >= 0 {
		s = s[:i]
	}
	var v dotted
	core := s
	if i := strings.IndexByte(s, '-'); i >= 0 {
		core = s[:i]
		v.pre = strings.Split(s[i+1:], ".")
		for _, p := range v.pre {
			if p == "" {
				return dotted{}, false
			}
		}
	}
	v.core = strings.Split(core, ".")
	if len(v.core) > maxCore {
		return dotted{}, false
	}
	for _, c := range v.core {
		if !isDigits(c) {
			return dotted{}, false
		}
	}
	for len(v.core) < maxCore {
		v.core = append(v.core, "0")
	}
	return v, true
}

func compareDotted(a, b dotted, foldCase bool) int {
	for i := range a.core {
		if c := compareNumeric(a.core[i], b.core[i]); c != 0 {
			return c
		}
	}
	return comparePrerelease(a.pre, b.pre, foldCase)
}

// CompareSemver orders semantic versions: the numeric core first, then a
// release sorts after its pre-releases, and pre-release identifiers compare
// numerically when both are numeric, numeric before alphanumeric otherwise,
// with the longer list winning a shared prefix. Build metadata is ignored, as
// semver requires. A leading "v" is accepted because Go modules require it.
func CompareSemver(a, b string) int {
	va, okA := parseDotted(strings.TrimPrefix(a, "v"), 3)
	vb, okB := parseDotted(strings.TrimPrefix(b, "v"), 3)
	if c, done := compareInvalid(a, okA, b, okB); done {
		return c
	}
	return compareDotted(va, vb, false)
}

// CompareNuGet orders NuGet versions: up to four numeric components (NuGet
// keeps the legacy revision), then pre-release labels compared like semver
// but case-insensitively, which is how NuGet treats 1.0.0-Beta and
// 1.0.0-beta as one version.
func CompareNuGet(a, b string) int {
	va, okA := parseDotted(a, 4)
	vb, okB := parseDotted(b, 4)
	if c, done := compareInvalid(a, okA, b, okB); done {
		return c
	}
	return compareDotted(va, vb, true)
}

func comparePrerelease(a, b []string, foldCase bool) int {
	switch {
	case len(a) == 0 && len(b) == 0:
		return 0
	case len(a) == 0:
		return 1
	case len(b) == 0:
		return -1
	}
	for i := 0; i < len(a) && i < len(b); i++ {
		if c := compareIdentifier(a[i], b[i], foldCase); c != 0 {
			return c
		}
	}
	return sign(len(a) - len(b))
}

func compareIdentifier(x, y string, foldCase bool) int {
	dx, dy := isDigits(x), isDigits(y)
	switch {
	case dx && dy:
		return compareNumeric(x, y)
	case dx:
		return -1
	case dy:
		return 1
	case foldCase:
		return strings.Compare(strings.ToLower(x), strings.ToLower(y))
	default:
		return strings.Compare(x, y)
	}
}

// --- PEP 440 (PyPI) ------------------------------------------------------------

// pep440Pattern is the canonical PEP 440 grammar published with the
// specification, case-insensitive; callers trim surrounding whitespace.
var pep440Pattern = regexp.MustCompile(`(?i)^v?` +
	`(?:(?P<epoch>[0-9]+)!)?` +
	`(?P<release>[0-9]+(?:\.[0-9]+)*)` +
	`(?P<pre>[-_.]?(?P<pre_l>alpha|a|beta|b|preview|pre|c|rc)[-_.]?(?P<pre_n>[0-9]+)?)?` +
	`(?P<post>(?:-(?P<post_n1>[0-9]+))|(?:[-_.]?(?P<post_l>post|rev|r)[-_.]?(?P<post_n2>[0-9]+)?))?` +
	`(?P<dev>[-_.]?(?P<dev_l>dev)[-_.]?(?P<dev_n>[0-9]+)?)?` +
	`(?:\+(?P<local>[a-z0-9]+(?:[-_.][a-z0-9]+)*))?$`)

// bound is a component that may be absent and, when absent, sorts either
// below or above every present value. PEP 440 needs both directions.
type bound struct {
	kind int // -1 below every value, 0 present, +1 above every value
	rank int // pre-release phase: a, b, rc
	num  string
}

func compareBound(a, b bound) int {
	if a.kind != b.kind {
		return sign(a.kind - b.kind)
	}
	if a.kind != 0 {
		return 0
	}
	if a.rank != b.rank {
		return sign(a.rank - b.rank)
	}
	return compareNumeric(a.num, b.num)
}

type pep440 struct {
	epoch   string
	release []string
	pre     bound
	post    bound
	dev     bound
	local   []string
}

func parsePEP440(s string) (pep440, bool) {
	m := pep440Pattern.FindStringSubmatch(strings.TrimSpace(s))
	if m == nil {
		return pep440{}, false
	}
	g := func(name string) string { return m[pep440Pattern.SubexpIndex(name)] }
	v := pep440{epoch: zeroIfEmpty(g("epoch")), release: strings.Split(g("release"), ".")}
	// Trailing zeros are insignificant: 1.0 == 1.0.0.
	for len(v.release) > 1 && strings.TrimLeft(v.release[len(v.release)-1], "0") == "" {
		v.release = v.release[:len(v.release)-1]
	}
	hasPre, hasPost, hasDev := g("pre") != "", g("post") != "", g("dev") != ""
	switch {
	case hasPre:
		v.pre = bound{rank: preRank(strings.ToLower(g("pre_l"))), num: zeroIfEmpty(g("pre_n"))}
	case !hasPost && hasDev:
		// 1.0.dev1 sorts before 1.0a1: a bare development release precedes
		// every pre-release of the same release.
		v.pre = bound{kind: -1}
	default:
		v.pre = bound{kind: 1}
	}
	v.post = bound{kind: -1}
	if hasPost {
		v.post = bound{num: zeroIfEmpty(g("post_n1") + g("post_n2"))}
	}
	v.dev = bound{kind: 1}
	if hasDev {
		v.dev = bound{num: zeroIfEmpty(g("dev_n"))}
	}
	if l := g("local"); l != "" {
		v.local = strings.FieldsFunc(strings.ToLower(l), func(r rune) bool {
			return r == '.' || r == '-' || r == '_'
		})
	}
	return v, true
}

func preRank(label string) int {
	switch label {
	case "a", "alpha":
		return 0
	case "b", "beta":
		return 1
	default: // c, rc, pre, preview
		return 2
	}
}

func zeroIfEmpty(s string) string {
	if s == "" {
		return "0"
	}
	return s
}

// ComparePEP440 orders Python versions by epoch, release, pre-release,
// post-release, development release and local label with the sentinel rules
// of the packaging library: a final release sorts after its pre-releases and
// before its post-releases, and a version without a local label sorts before
// the same version with one.
func ComparePEP440(a, b string) int {
	va, okA := parsePEP440(a)
	vb, okB := parsePEP440(b)
	if c, done := compareInvalid(a, okA, b, okB); done {
		return c
	}
	if c := compareNumeric(va.epoch, vb.epoch); c != 0 {
		return c
	}
	if c := compareNumericList(va.release, vb.release); c != 0 {
		return c
	}
	for _, pair := range [][2]bound{{va.pre, vb.pre}, {va.post, vb.post}, {va.dev, vb.dev}} {
		if c := compareBound(pair[0], pair[1]); c != 0 {
			return c
		}
	}
	return compareLocal(va.local, vb.local)
}

func compareNumericList(a, b []string) int {
	for i := 0; i < len(a) && i < len(b); i++ {
		if c := compareNumeric(a[i], b[i]); c != 0 {
			return c
		}
	}
	return sign(len(a) - len(b))
}

// compareLocal applies PEP 440's local-label rules: numeric segments compare
// numerically and sort after alphanumeric ones; no label sorts first.
func compareLocal(a, b []string) int {
	for i := 0; i < len(a) && i < len(b); i++ {
		dx, dy := isDigits(a[i]), isDigits(b[i])
		var c int
		switch {
		case dx && dy:
			c = compareNumeric(a[i], b[i])
		case dx:
			c = 1
		case dy:
			c = -1
		default:
			c = strings.Compare(a[i], b[i])
		}
		if c != 0 {
			return c
		}
	}
	return sign(len(a) - len(b))
}

// --- Maven -----------------------------------------------------------------------

// The Maven comparison follows org.apache.maven.artifact.versioning.
// ComparableVersion: a version is a list of integer, string and nested list
// items split on '.', '-' and digit/letter transitions, known qualifiers
// rank alpha < beta < milestone < rc < snapshot < (release) < sp, and unknown
// qualifiers sort after them lexically.

type mavenKind int

const (
	mavenInt mavenKind = iota
	mavenString
	mavenList
)

type mavenItem struct {
	kind  mavenKind
	value string      // digits for mavenInt, comparable qualifier for mavenString
	list  []mavenItem // children for mavenList
}

var mavenQualifiers = []string{"alpha", "beta", "milestone", "rc", "snapshot", "", "sp"}

var mavenAliases = map[string]string{"ga": "", "final": "", "release": "", "cr": "rc"}

// releaseIndex is the comparable form of the empty qualifier: a string item
// is equivalent to a missing item exactly when it ranks here. The release
// is sixth in both mavenQualifiers and mercuryQualifiers.
const releaseIndex = "5"

// rankQualifier is q's comparable form: its index among known, or after
// every known qualifier, ordered lexically, when it is none of them.
func rankQualifier(known []string, q string) string {
	for i, k := range known {
		if q == k {
			return string(rune('0' + i))
		}
	}
	return string(rune('0'+len(known))) + "-" + q
}

// newQualifier is the string item for s, a qualifier ranked among known
// after aliases; a single a, b or m followed by a digit stands for alpha,
// beta or milestone.
func newQualifier(
	s string,
	followedByDigit bool,
	known []string,
	aliases map[string]string,
) mavenItem {
	if followedByDigit && len(s) == 1 {
		switch s[0] {
		case 'a':
			s = "alpha"
		case 'b':
			s = "beta"
		case 'm':
			s = "milestone"
		}
	}
	if alias, ok := aliases[s]; ok {
		s = alias
	}
	return mavenItem{kind: mavenString, value: rankQualifier(known, s)}
}

func newMavenString(s string, followedByDigit bool) mavenItem {
	return newQualifier(s, followedByDigit, mavenQualifiers, mavenAliases)
}

func newMavenItem(isDigit bool, s string) mavenItem {
	if isDigit {
		return mavenItem{kind: mavenInt, value: strings.TrimLeft(s, "0")}
	}
	return newMavenString(s, false)
}

func (it mavenItem) isNull() bool {
	switch it.kind {
	case mavenInt:
		return it.value == ""
	case mavenString:
		return it.value == releaseIndex
	default:
		return len(it.list) == 0
	}
}

// mavenBuilder holds the parse state; lists are referred to by path from the
// root because appending to a parent can move its children in memory.
type mavenBuilder struct {
	root  mavenItem
	stack [][]int
}

func (b *mavenBuilder) list(path []int) *mavenItem {
	l := &b.root
	for _, i := range path {
		l = &l.list[i]
	}
	return l
}

func (b *mavenBuilder) top() []int { return b.stack[len(b.stack)-1] }

func (b *mavenBuilder) add(it mavenItem) { l := b.list(b.top()); l.list = append(l.list, it) }

func (b *mavenBuilder) push() {
	parent := b.top()
	l := b.list(parent)
	l.list = append(l.list, mavenItem{kind: mavenList})
	path := append(append([]int{}, parent...), len(l.list)-1)
	b.stack = append(b.stack, path)
}

func parseMaven(version string) mavenItem {
	version = strings.ToLower(version)
	b := &mavenBuilder{root: mavenItem{kind: mavenList}, stack: [][]int{{}}}
	isDigit := false
	start := 0
	for i := 0; i < len(version); i++ {
		c := version[i]
		switch {
		case c == '.' || c == '-':
			if i == start {
				b.add(mavenItem{kind: mavenInt})
			} else {
				b.add(newMavenItem(isDigit, version[start:i]))
			}
			start = i + 1
			if c == '-' {
				b.push()
			}
		case c >= '0' && c <= '9':
			if !isDigit && i > start {
				b.add(newMavenString(version[start:i], true))
				start = i
				b.push()
			}
			isDigit = true
		default:
			if isDigit && i > start {
				b.add(newMavenItem(true, version[start:i]))
				start = i
				b.push()
			}
			isDigit = false
		}
	}
	if len(version) > start {
		b.add(newMavenItem(isDigit, version[start:]))
	}
	// Normalise innermost lists first, as ComparableVersion pops its stack:
	// trailing null items (0, release qualifiers, empty lists) are dropped so
	// 1.0 == 1 and 1-ga == 1.
	for i := len(b.stack) - 1; i >= 0; i-- {
		normaliseMaven(b.list(b.stack[i]))
	}
	return b.root
}

func normaliseMaven(l *mavenItem) {
	for i := len(l.list) - 1; i >= 0; i-- {
		it := l.list[i]
		if it.isNull() {
			l.list = append(l.list[:i], l.list[i+1:]...)
		} else if it.kind != mavenList {
			break
		}
	}
}

// compareMavenItem compares it with other; a nil other stands for a missing
// item, which is how ComparableVersion pads the shorter list.
func compareMavenItem(it mavenItem, other *mavenItem) int {
	switch it.kind {
	case mavenInt:
		switch {
		case other == nil:
			return compareNumeric(it.value, "")
		case other.kind == mavenInt:
			return compareNumeric(it.value, other.value)
		default:
			return 1
		}
	case mavenString:
		switch {
		case other == nil:
			return strings.Compare(it.value, releaseIndex)
		case other.kind == mavenString:
			return strings.Compare(it.value, other.value)
		default:
			return -1
		}
	default:
		switch {
		case other == nil:
			if len(it.list) == 0 {
				return 0
			}
			return compareMavenItem(it.list[0], nil)
		case other.kind == mavenInt:
			return -1
		case other.kind == mavenString:
			return 1
		}
		return compareMavenLists(it.list, other.list)
	}
}

func compareMavenLists(a, b []mavenItem) int {
	for i := 0; i < len(a) || i < len(b); i++ {
		var c int
		switch {
		case i >= len(a):
			c = -compareMavenItem(b[i], nil)
		case i >= len(b):
			c = compareMavenItem(a[i], nil)
		default:
			c = compareMavenItem(a[i], &b[i])
		}
		if c != 0 {
			return c
		}
	}
	return 0
}

// CompareMaven orders Maven, Gradle, Ivy and SBT versions the way Maven's
// ComparableVersion does, so 1.0-SNAPSHOT < 1.0 < 1.0-sp1 and 1.10 > 1.9.
func CompareMaven(a, b string) int {
	ia, ib := parseMaven(a), parseMaven(b)
	return sign(compareMavenItem(ia, &ib))
}

// --- Mercury ---------------------------------------------------------------------

// The Mercury comparison follows org.apache.maven.mercury.artifact.version.
// ComparableVersion (mercury-artifact 1.0-alpha-6), the comparator
// versions-maven-plugin names "mercury" and Artifactory's
// MavenVersionComparator wraps to order its versions searches. Its items
// compare as ComparableVersion's do (compareMavenItem); it parses and ranks
// differently:
//   - snapshot ranks below every other qualifier, so 1.0-SNAPSHOT <
//     1.0-alpha-1 < 1.0-rc1 < 1.0, where ComparableVersion puts SNAPSHOT
//     just below the release;
//   - release is not an alias of the release, so 1.0-release sorts as an
//     unknown qualifier, after 1.0-sp;
//   - only a '-' between digits opens a sublist, so 1.0-alpha-1 and
//     1.0-alpha.1 are one flat list and equal;
//   - normalising drops trailing null items up to the first item that is
//     not null, a list included.

var mercuryQualifiers = []string{"snapshot", "alpha", "beta", "milestone", "rc", "", "sp"}

var mercuryAliases = map[string]string{"ga": "", "final": "", "cr": "rc"}

func newMercuryItem(isDigit bool, s string) mavenItem {
	if isDigit {
		return mavenItem{kind: mavenInt, value: strings.TrimLeft(s, "0")}
	}
	return newQualifier(s, false, mercuryQualifiers, mercuryAliases)
}

func parseMercury(version string) mavenItem {
	version = strings.ToLower(version)
	b := &mavenBuilder{root: mavenItem{kind: mavenList}, stack: [][]int{{}}}
	isDigit := false
	start := 0
	for i := 0; i < len(version); i++ {
		c := version[i]
		switch {
		case c == '.' || c == '-':
			if i == start {
				b.add(mavenItem{kind: mavenInt})
			} else {
				b.add(newMercuryItem(isDigit, version[start:i]))
			}
			start = i + 1
			if c == '-' && isDigit {
				// 1.0-x is 1-x; a digit after the '-' opens a sublist, which
				// is what tells 1-1 from 1.1.
				normaliseMercury(b.list(b.top()))
				if i+1 < len(version) && version[i+1] >= '0' && version[i+1] <= '9' {
					b.push()
				}
			}
		case c >= '0' && c <= '9':
			if !isDigit && i > start {
				b.add(newQualifier(version[start:i], true, mercuryQualifiers, mercuryAliases))
				start = i
			}
			isDigit = true
		default:
			if isDigit && i > start {
				b.add(newMercuryItem(true, version[start:i]))
				start = i
			}
			isDigit = false
		}
	}
	if len(version) > start {
		b.add(newMercuryItem(isDigit, version[start:]))
	}
	for i := len(b.stack) - 1; i >= 0; i-- {
		normaliseMercury(b.list(b.stack[i]))
	}
	return b.root
}

func normaliseMercury(l *mavenItem) {
	for len(l.list) > 0 && l.list[len(l.list)-1].isNull() {
		l.list = l.list[:len(l.list)-1]
	}
}

// CompareMercury orders versions the way Artifactory's versions and latest
// version searches do (Mercury's ComparableVersion): 1.0-SNAPSHOT <
// 1.0-alpha-1 < 1.0-rc1 < 1.0 < 1.0-sp1, and 1.10 > 1.9.
func CompareMercury(a, b string) int {
	ia, ib := parseMercury(a), parseMercury(b)
	return sign(compareMavenItem(ia, &ib))
}

// --- Index records -----------------------------------------------------------------

// Package is one name in one repository's package index (decision 0018). The
// pair (RepoKey, Name) is unique; Type is copied from the repository so the
// index can be queried without a join.
type Package struct {
	ID      int64
	RepoKey string
	Type    repo.PackageType
	Name    string
	Created time.Time
}

// PackageVersion is one version of a package. OrderKey is the version's rank
// under the package type's comparator, recomputed by the store whenever the
// package's version set changes, so SQL can sort and window
// (keepLastNVersions) without reimplementing every comparator in two
// dialects. Metadata is the format handler's per-version document (a
// packument fragment, a nuspec summary, a chart entry) and is opaque here.
type PackageVersion struct {
	ID             int64
	PackageID      int64
	Version        string
	OrderKey       int64
	Created        time.Time
	LastDownloaded time.Time
	Metadata       []byte
}

// VersionFile links a version to an item that belongs to it.
type VersionFile struct {
	VersionID int64
	ItemID    int64
}

// AssignOrderKeys sorts versions with c and sets OrderKey to each version's
// rank, starting at 1 for the oldest. Equivalent versions share a rank.
func AssignOrderKeys(c VersionComparator, versions []PackageVersion) {
	SortVersions(c, versions)
	var rank int64
	for i := range versions {
		if i == 0 || c.Compare(versions[i-1], versions[i]) != 0 {
			rank++
		}
		versions[i].OrderKey = rank
	}
}
