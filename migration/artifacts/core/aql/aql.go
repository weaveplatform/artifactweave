// Package aql parses Artifactory Query Language and runs it (decision 0012).
// It covers the items domain: the shape the JFrog CLI generates for every
// file specification (research 09) and what users write by hand. That is
// $and and $or, field comparisons, $match and $nmatch globs, @property and
// property.key/property.value criteria, $msp, the stat domain, and the
// include, sort, offset, limit, transitive and distinct suffixes with
// Artifactory's output rules (research 22 §1). The builds and
// build.promotions domains are served too, and every domain reaches the
// others through relation paths such as artifact.module.build.name (see
// Joined), which is how the JFrog CLI finds a build's files; an include
// may name those domains' fields too, which Nest fills in (nest.go). It
// also parses the two actions, items.delete and properties.update
// (research 22 §1.5), whose criteria select items the same way; the server
// carries them out. Every repository's root is an items row (Roots).
//
// A parsed Query is run two ways that must agree row for row: Match and
// Compare are the reference semantics (the in-memory store uses them), and
// Compile turns the query into SQL for SQLite or PostgreSQL. The store
// contract suite runs the same queries on every backend and expects the
// same ordered rows. Before either, Resolve rewrites repo criteria against
// the configured repositories, so remote and virtual keys match what they
// stand for.
package aql

import (
	"errors"
	"fmt"
	"slices"
	"strconv"
	"strings"
	"time"
)

// MaxLength is the longest query accepted, in characters: Artifactory's
// artifactory.aql.search.query.size.limit default. ErrTooLong's text names
// it, as Artifactory's message names the configured limit.
const MaxLength = 6000

// maxDepth bounds $and/$or nesting, so a hostile query cannot exhaust the
// stack of the parser or of the database planner.
const maxDepth = 32

var (
	// ErrSyntax reports a query that does not parse.
	ErrSyntax = errors.New("aql: syntax error")
	// ErrUnsupported reports valid AQL that artifactweave does not serve:
	// another domain, a secondary-domain field, or an operator outside the
	// supported set.
	ErrUnsupported = errors.New("aql: unsupported")
	// ErrTooLong reports a query above MaxLength characters. Its text is
	// Artifactory's message (aql-performance).
	ErrTooLong = errors.New("AQL query is too long; please reduce the query length to less than " +
		strconv.Itoa(MaxLength) + " chars")
	// ErrRestricted reports a query a non-admin caller may not run.
	ErrRestricted = errors.New("aql: restricted")
	// ErrEmpty reports a request with no query at all.
	ErrEmpty = errors.New("aql: empty query")
	// ErrInvalid reports a query that parses but breaks an output rule: a
	// sort on a field the output leaves out, or virtual_repos without repo,
	// path and name. It also reports a suffix the query's action does not
	// take, and a properties.update without .keys() or .newValue().
	ErrInvalid = errors.New("aql: invalid query")
)

// Field is an item field name as AQL spells it.
type Field string

// Item fields. Checksums are the actual ones unless named original.
const (
	FieldRepo         Field = "repo"
	FieldPath         Field = "path"
	FieldName         Field = "name"
	FieldType         Field = "type"
	FieldSize         Field = "size"
	FieldDepth        Field = "depth"
	FieldCreated      Field = "created"
	FieldCreatedBy    Field = "created_by"
	FieldModified     Field = "modified"
	FieldModifiedBy   Field = "modified_by"
	FieldUpdated      Field = "updated"
	FieldOriginalMD5  Field = "original_md5"
	FieldActualMD5    Field = "actual_md5"
	FieldOriginalSHA1 Field = "original_sha1"
	FieldActualSHA1   Field = "actual_sha1"
	FieldSHA256       Field = "sha256"
)

// kind is how a field's values are compared.
type kind int

const (
	kindText kind = iota
	kindNumber
	kindTime
	kindType
)

// FieldVirtualRepos lists the virtual repositories that contain an item's
// repository. It can be included but not searched on, and it needs repo,
// path and name in the output (research 22 §1.2).
const FieldVirtualRepos = "virtual_repos"

// StatField is a field of the stat domain, which criteria name
// "stat.<field>". Its values come from the item's download statistics; an
// item that was never downloaded has none, which compares as SQL NULL.
type StatField string

// Statistic fields (research 22 §1.1).
const (
	StatDownloaded         StatField = "downloaded"
	StatDownloads          StatField = "downloads"
	StatDownloadedBy       StatField = "downloaded_by"
	StatRemoteDownloads    StatField = "remote_downloads"
	StatRemoteDownloaded   StatField = "remote_downloaded"
	StatRemoteDownloadedBy StatField = "remote_downloaded_by"
	StatRemoteOrigin       StatField = "remote_origin"
	StatRemotePath         StatField = "remote_path"
)

// statOrder lists the statistic fields in the order results render them,
// which is jfrog-client-go's Stat struct order for the fields it reads.
var statOrder = []StatField{
	StatDownloaded, StatDownloads, StatDownloadedBy, StatRemoteDownloads,
	StatRemoteDownloaded, StatRemoteDownloadedBy, StatRemoteOrigin, StatRemotePath,
}

// statKinds maps each statistic field to its kind.
var statKinds = map[StatField]kind{
	StatDownloaded: kindTime, StatDownloads: kindNumber, StatDownloadedBy: kindText,
	StatRemoteDownloads: kindNumber, StatRemoteDownloaded: kindTime,
	StatRemoteDownloadedBy: kindText, StatRemoteOrigin: kindText, StatRemotePath: kindText,
}

// fieldOrder lists the item fields in the order results render them.
var fieldOrder = []Field{
	FieldRepo, FieldPath, FieldName, FieldType, FieldSize, FieldDepth,
	FieldCreated, FieldCreatedBy, FieldModified, FieldModifiedBy, FieldUpdated,
	FieldOriginalMD5, FieldActualMD5, FieldOriginalSHA1, FieldActualSHA1, FieldSHA256,
}

// fieldKinds maps each supported item field to its kind.
var fieldKinds = map[Field]kind{
	FieldRepo: kindText, FieldPath: kindText, FieldName: kindText, FieldType: kindType,
	FieldSize: kindNumber, FieldDepth: kindNumber,
	FieldCreated: kindTime, FieldModified: kindTime, FieldUpdated: kindTime,
	FieldCreatedBy: kindText, FieldModifiedBy: kindText,
	FieldOriginalMD5: kindText, FieldActualMD5: kindText,
	FieldOriginalSHA1: kindText, FieldActualSHA1: kindText, FieldSHA256: kindText,
}

// defaultFields are what an items query returns without an include of item
// fields, as Artifactory documents for the items domain.
var defaultFields = []Field{
	FieldRepo, FieldPath, FieldName, FieldType, FieldSize,
	FieldCreated, FieldCreatedBy, FieldModified, FieldModifiedBy, FieldUpdated,
}

// Op is a comparison operator.
type Op string

// Operators. Before and Last are relative times, resolved against the
// caller's clock when the query runs.
const (
	OpEq     Op = "$eq"
	OpNe     Op = "$ne"
	OpGt     Op = "$gt"
	OpGte    Op = "$gte"
	OpLt     Op = "$lt"
	OpLte    Op = "$lte"
	OpMatch  Op = "$match"
	OpNMatch Op = "$nmatch"
	OpBefore Op = "$before"
	OpLast   Op = "$last"
)

// Node is a criteria tree node: And, Or, *FieldCriterion,
// *PropertyCriterion, *StatCriterion, MSP, the RepoSet that Resolve puts in
// place of repo criteria, or a criterion of the builds domains and relation
// paths: *PathCriterion, *PathProperty, *PathMSP and the *Joined that
// gathers those naming paths.
type Node interface{ node() }

// And holds when every child holds; an empty And always holds.
type And []Node

// Or holds when any child holds; an empty Or never holds.
type Or []Node

// FieldCriterion compares an item field. Exactly one of Text, Number and
// Time is meaningful, by the field's kind; Before and Last carry their
// span in Span instead.
type FieldCriterion struct {
	Field  Field
	Op     Op
	Text   string
	Number int64
	Time   time.Time
	Span   Span
}

// PropertyPart says which part of a property a PropertyCriterion tests.
type PropertyPart int

// Property parts: "@key" tests a key's values, "property.key" the keys
// alone and "property.value" the values alone.
const (
	PartPair PropertyPart = iota
	PartKey
	PartValue
)

// PropertyCriterion matches items by property. For PartPair, Key "*" is any
// key and Value "*" with $eq (or $match) is any value. For PartKey and
// PartValue, Value is what the key or the value is compared with, and "*"
// with $eq is any. A negative operator ($ne, $nmatch) holds when the item has
// no property that matches the positive form, so items without the key match
// it too. Each criterion is tested on its own: two of them may be satisfied
// by different properties of the item, unless they sit in an MSP.
type PropertyCriterion struct {
	Part  PropertyPart
	Key   string
	Op    Op
	Value string
}

// MSP is $msp, match on a single property: it holds when one property of the
// item satisfies every criterion at once. Inside it, a negative operator
// tests that one property ($ne is a different value, $nmatch a value that
// does not match), not the absence of every property that would match.
type MSP []*PropertyCriterion

// StatCriterion compares a statistic field. Null with $eq holds for an item
// that has no value for the field (never downloaded), and with $ne for one
// that has; otherwise a missing value compares as SQL NULL and no comparison
// holds.
type StatCriterion struct {
	Field  StatField
	Op     Op
	Null   bool
	Text   string
	Number int64
	Time   time.Time
	Span   Span
}

func (And) node()                {}
func (Or) node()                 {}
func (MSP) node()                {}
func (*FieldCriterion) node()    {}
func (*PropertyCriterion) node() {}
func (*StatCriterion) node()     {}

// Span is a relative time for $before and $last: N units back from now.
type Span struct {
	N    int
	Unit string
}

// back returns now moved back by the span.
func (s Span) back(now time.Time) time.Time {
	switch s.Unit {
	case "mo":
		return now.AddDate(0, -s.N, 0)
	case "y":
		return now.AddDate(-s.N, 0, 0)
	}
	return now.Add(-time.Duration(s.N) * spanUnits[s.Unit])
}

// spanUnits are the fixed-length units of $before and $last; months and
// years are calendar units handled in back.
var spanUnits = map[string]time.Duration{
	"ms": time.Millisecond, "s": time.Second, "minutes": time.Minute,
	"d": 24 * time.Hour, "w": 7 * 24 * time.Hour,
}

// spanAliases maps every unit $before and $last accept to the one Span
// keeps. The long forms are those aql-search-criteria documents (mills,
// seconds, days, weeks, months, years); millis and mi are what Artifactory
// 6.20's AqlRelativeDateComparatorEnum also parses.
var spanAliases = map[string]string{
	"ms": "ms", "mills": "ms", "millis": "ms",
	"s": "s", "seconds": "s",
	"minutes": "minutes", "mi": "minutes",
	"d": "d", "days": "d",
	"w": "w", "weeks": "w",
	"mo": "mo", "months": "mo",
	"y": "y", "years": "y",
}

// Sort orders results by Fields, all ascending or all descending.
type Sort struct {
	Desc   bool
	Fields []Field
}

// Action is what a query does with the items its criteria select.
type Action int

// Actions. Find returns the items; DeleteItems is items.delete, which
// removes them, and UpdateProperties is properties.update, which replaces
// the values of the query's Keys on them.
const (
	Find Action = iota
	DeleteItems
	UpdateProperties
)

// Query is a parsed query, or an action over items.
type Query struct {
	// Domain is the primary domain: what the query finds. Actions are on
	// items.
	Domain Domain
	// Action is Find unless the query is items.delete or properties.update.
	Action Action
	// DryRun is an action's .dryRun(), true unless it says false, so an
	// action that leaves it out changes nothing. Find has none.
	DryRun bool
	// Keys and NewValue are properties.update's .keys() and .newValue().
	Keys     []string
	NewValue string
	// Criteria is the find criteria; it already carries the implicit
	// type=file that applies when the query names no type.
	Criteria Node
	// Include is the include list as given, in order.
	Include []string
	// Sort is nil when the query does not sort.
	Sort *Sort
	// Offset and Limit are the suffixes; HasOffset and HasLimit tell
	// .offset(0) and .limit(0) from none. Like Sort, they count result rows,
	// which are items whatever the include: properties and statistics render
	// inside their item's row, so they never add rows to skip or count.
	// Artifactory counts its joined item-property rows instead (DV-074).
	Offset    int
	HasOffset bool
	Limit     int
	HasLimit  bool
	// Transitive selects bounded Smart Remote search in the HTTP adapter.
	// Distinct remains compatibility syntax: item rows are always distinct.
	Transitive bool
	Distinct   bool
	// hasImpliedVirtualRepos is set by Resolve when an $eq names a virtual
	// repository: the rows then carry virtual_repos without the include.
	hasImpliedVirtualRepos bool
	// related are the includes that name fields of other domains, in
	// include order (relatedInclude).
	related []relInclude
	// isRooted marks an items query parsed from AQL text, whose rows include
	// every repository's root as Artifactory's do; Resolve then lists the
	// roots in roots. The GET searches build their queries in code and
	// leave it unset, so they never meet a root without a record.
	isRooted bool
	roots    []RepoRef
}

// Options are the caller's settings for running a query.
type Options struct {
	// Now resolves $before and $last; zero means time.Now.
	Now time.Time
	// Max is the most rows any query returns, its own limit included: the
	// search hard limit (decision 0012). Zero means no cap.
	Max int
}

func (o Options) now() time.Time {
	if o.Now.IsZero() {
		return time.Now()
	}
	return o.Now
}

// Window returns the offset and the row limit to apply; a negative limit
// means none. The cap trims a query's own limit too.
func (q *Query) Window(o Options) (offset, limit int) {
	limit = -1
	if q.HasLimit {
		limit = q.Limit
	}
	if o.Max > 0 && (limit < 0 || limit > o.Max) {
		limit = o.Max
	}
	return q.Offset, limit
}

// Fields returns the item fields each result row carries, in render order:
// the included item fields, or the default fields when the include names
// none. Including only properties or statistics keeps the defaults, as
// Artifactory does.
func (q *Query) Fields() []Field {
	if q.Domain != DomainItems {
		return q.domainFields()
	}
	var out []Field
	for _, f := range fieldOrder {
		if slices.Contains(q.Include, string(f)) || slices.Contains(q.Include, "*") {
			out = append(out, f)
		}
	}
	// Only an include of virtual_repos replaces the defaults; the
	// virtual_repos a virtual repository's key implies are added to them.
	if len(out) == 0 && !slices.Contains(q.Include, FieldVirtualRepos) {
		return slices.Clone(defaultFields)
	}
	return out
}

// VirtualRepos reports whether result rows carry virtual_repos: the query
// includes them, or its criteria name a virtual repository (Resolve).
func (q *Query) VirtualRepos() bool {
	return q.hasImpliedVirtualRepos || slices.Contains(q.Include, FieldVirtualRepos)
}

// Properties reports whether result rows carry the item's properties: an
// include of "property", "property.*", "property.key", "property.value",
// "@", "@*" or "@<key>", or a properties.update, whose rows show the keys it
// changes.
func (q *Query) Properties() bool {
	return q.Action == UpdateProperties || slices.ContainsFunc(q.Include, func(s string) bool {
		return s == "property" || strings.HasPrefix(s, "property.") || strings.HasPrefix(s, "@")
	})
}

// PropertyKeys returns the keys an "@<key>" include limits the rendered
// properties to, in include order; nil means every key, which an include of
// the whole property domain also asks for.
func (q *Query) PropertyKeys() []string {
	if q.Action == UpdateProperties {
		return q.Keys
	}
	for _, s := range q.Include {
		if s == "property" || s == "property.*" || s == "property.key" || s == "property.value" ||
			everyKey(s) {
			return nil
		}
	}
	return q.keyIncludes()
}

// keyIncludes returns the keys of the "@<key>" includes, in include order.
// Each also filters out items without that key, which Parse adds to the
// criteria whatever else the include names.
func (q *Query) keyIncludes() []string {
	var out []string
	for _, s := range q.Include {
		if k, ok := strings.CutPrefix(s, "@"); ok && !everyKey(s) && !slices.Contains(out, k) {
			out = append(out, k)
		}
	}
	return out
}

// everyKey reports whether an include's property part is "@" or "@*", which
// name every key, as "property.*" does, and filter nothing. The "Sorting in
// AQL" example of JFrog's aql-query-output includes "@"; 6.20 adds no key
// filter for "*" (ParserToAqlAdapter.addIncludePropertyKeyFilter), and its
// AqlIncludeServiceTest.includePropertiesWithAtAndAsterisk expects "@*" to
// return what "property.*" does.
func everyKey(s string) bool { return s == "@" || s == "@*" }

// Stats returns the statistic fields result rows carry, in render order:
// all of them for "stat" or "stat.*", or those named "stat.<field>".
func (q *Query) Stats() []StatField {
	var out []StatField
	for _, f := range statOrder {
		for _, s := range q.Include {
			if s == "stat" || s == "stat.*" || s == "stat."+string(f) {
				out = append(out, f)
				break
			}
		}
	}
	return out
}

// CheckRestricted applies decision 0012's rule for non-admin callers: every
// items result must carry repo, path and name, and every builds result
// name, number and repo, so each row can be checked against the caller's
// permissions. Whether the caller may query builds at all is the caller's
// to decide, from its token's scope.
func (q *Query) CheckRestricted() error {
	fields := q.Fields()
	switch q.Domain {
	case DomainBuilds:
		for _, f := range []Field{"name", "number", "repo"} {
			if !slices.Contains(fields, f) {
				return fmt.Errorf(
					"%w: non-admin builds queries must include name, number and repo",
					ErrRestricted,
				)
			}
		}
		return nil
	case DomainPromotions:
		// A promotion is checked against its run, which every row carries.
		return nil
	}
	for _, f := range []Field{FieldRepo, FieldPath, FieldName} {
		if !slices.Contains(fields, f) {
			return fmt.Errorf(
				"%w: non-admin queries must include repo, path and name",
				ErrRestricted,
			)
		}
	}
	return nil
}
