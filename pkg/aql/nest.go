package aql

import (
	"bytes"
	"cmp"
	"context"
	"errors"
	"fmt"
	"maps"
	"slices"
	"strconv"
	"strings"
	"time"

	"github.com/weaveplatform/artifactweave/pkg/types"
)

// An include may name fields of the domains a relation path reaches
// (research 22 §1.2): items.find().include("artifact.module.build.name")
// renders each item with "artifacts":[{"modules":[{"builds":[{...}]}]}],
// and builds.find().include("module.artifact.item.name") each run with
// "modules":[{"artifacts":[{"items":[{...}]}]}]. Artifactory selects those
// fields through the same joins as the criteria, one table per domain, and
// folds the joined rows into each found row (AqlJsonStreamer and
// InflatableRow in its 6.20 source). Nest does the same over the rows a
// Source loads: a related row is listed when one combination of related
// rows that holds it also satisfies the query's relation-path criteria,
// and rows of one domain are told apart by the values of their included
// fields, or by their identity when the whole domain is included, as
// Artifactory's then include the row's id.

// maxNestCombinations bounds the combinations of related rows Nest tries
// for one query. Every relation step multiplies them, so a long include
// path over large builds could otherwise keep the server busy for as long
// as the caller likes. A variable so a test can lower it.
var maxNestCombinations = 1_000_000

// errNoRun reports a run a Corpus does not hold.
var errNoRun = errors.New("aql: no such run")

// relPart is what an include names of the rows its path reaches.
type relPart int

const (
	partFields relPart = iota
	partProperties
	partStats
)

// relInclude is an include of another domain: the fields, the properties
// or (of an item) the statistics of the rows path reaches from the primary
// domain. field is the one field or statistic named; whole names all of
// them, which also tells the rows apart by identity (rowKey, relRow.fold);
// key is the property key of an "@<key>" include.
type relInclude struct {
	path  string
	rel   rel
	part  relPart
	field string
	whole bool
	key   string
}

// relatedInclude reads an include that names a relation path from the
// domain from: "<path>" or "<path>.*" includes every field of the domain
// path reaches and "<path>.<field>" one of them; the properties and
// statistics of the rows it reaches are named as an items query names its
// own (relInclude.read). A builds query's own properties count too: Row
// has no place for a run's properties, so they nest as a related run's
// do. related is false for any other name that starts with no step from
// from, which is then the primary domain's own.
func relatedInclude(name string, from rel) (ri relInclude, related bool, err error) {
	segs := strings.Split(name, ".")
	to, i := from, 0
	for ; i < len(segs); i++ {
		next, ok := relNames[segs[i]]
		if !ok || !slices.Contains(relSteps[to], next) {
			break
		}
		to = next
	}
	ri = relInclude{path: strings.Join(segs[:i], "."), rel: to}
	known := ri.read(strings.Join(segs[i:], "."))
	switch {
	case i > 0 && !known:
		return ri, true, fmt.Errorf(
			"%w: include of %q (no field, property or statistic of the rows its path reaches)",
			ErrUnsupported, name)
	case i > 0, from == relBuild && known && ri.part == partProperties:
		return ri, true, nil
	}
	return relInclude{}, false, nil
}

// read sets what ri names from rest, the include after its path: "" or "*"
// every field, or one field; "property", "property.*", "property.key",
// "property.value", "@", "@*" or "@<key>" the properties of an item, module
// or run; "stat", "stat.*" or "stat.<field>" the statistics of an item.
// known is false for anything else. As in an items query's own include, "@"
// and "@*" name every key and filter nothing. Like "@<key>", and unlike
// "property.*", they select no property id in 6.20, so the rows they nest
// fold by value.
func (ri *relInclude) read(rest string) (known bool) {
	props := ri.rel.hasProperties()
	switch {
	case rest == "" || rest == "*":
		ri.whole = true
		return true
	case props && (rest == "property" || rest == "property.*"):
		ri.part, ri.whole = partProperties, true
		return true
	case props && (rest == "property.key" || rest == "property.value"):
		ri.part = partProperties
		return true
	case ri.rel == relItem && (rest == "stat" || rest == "stat.*"):
		ri.part, ri.whole = partStats, true
		return true
	}
	if k, ok := strings.CutPrefix(rest, "@"); ok {
		ri.part = partProperties
		if !everyKey(rest) {
			ri.key = k
		}
		return props
	}
	if f, ok := strings.CutPrefix(rest, "stat."); ok && ri.rel == relItem {
		_, known = statKinds[StatField(f)]
		ri.part, ri.field = partStats, f
		return known
	}
	_, known = ri.rel.fieldKind(rest)
	ri.field = rest
	return known
}

// keyFilters are the criteria an "@<key>" include of a related row, or of
// a run's own properties, adds: the row its path reaches must have the
// key. Artifactory 6.20 filters these as it does an item's own "@<key>"
// (ParserToAqlAdapter.handleIncludePropertyKeyFilter puts the same result
// filter on module and build properties), and as a criterion on the path
// the filter shares the path's row with the include, so only rows with
// the key nest.
func (q *Query) keyFilters() []Node {
	var out []Node
	for _, ri := range q.related {
		if ri.key != "" {
			out = append(out, &PathProperty{
				Path: ri.path, Criterion: &PropertyCriterion{Key: ri.key, Op: OpEq, Value: "*"},
				rel: ri.rel,
			})
		}
	}
	return out
}

// addRelated adds an include of a related domain unless q has it already.
func (q *Query) addRelated(ri relInclude) {
	if !slices.Contains(q.related, ri) {
		q.related = append(q.related, ri)
	}
}

// Relates reports whether q's include names fields of other domains, which
// Nest or NestBuilds then fills in.
func (q *Query) Relates() bool { return len(q.related) > 0 }

// ForNesting is the query to run for q's rows when q relates: a copy whose
// item rows carry every property and statistic, which the relation-path
// criteria Nest evaluates again may read. q itself still renders the
// response, so they show only as q includes them.
func (q *Query) ForNesting() *Query {
	if !q.Relates() || q.Domain != DomainItems {
		return q
	}
	wide := *q
	wide.Include = append(slices.Clone(q.Include), "property", "stat")
	return &wide
}

// Source loads the rows of other domains that Nest follows relation paths
// to. Runs come with their modules and properties, and items with their
// properties, because the query's criteria may read them, and with their
// statistics, which an include may render.
type Source interface {
	// RunsWithFile returns the runs with an artifact, or with dependency a
	// dependency, whose SHA-1 is sha1, by ID.
	RunsWithFile(ctx context.Context, dependency bool, sha1 string) ([]types.Build, error)
	// ItemsWithSHA1 returns the files whose actual SHA-1 is sha1, by ID,
	// each with Stats set only when it has a statistics record.
	ItemsWithSHA1(ctx context.Context, sha1 string) ([]Hit, error)
	// Run returns the run with id.
	Run(ctx context.Context, id int64) (types.Build, error)
	// RunPromotions returns the promotions of the run with id, by ID.
	RunPromotions(ctx context.Context, id int64) ([]types.BuildPromotion, error)
}

// Nesting is how Nest renders related rows for one caller.
type Nesting struct {
	// Now resolves $before and $last in the relation-path criteria; zero
	// means time.Now.
	Now time.Time
	// ItemVisible and RunVisible, when set, report whether the caller may
	// see a related item, or a run with its modules, files and promotions.
	// A row they refuse is left out with every row below it.
	ItemVisible func(types.Item) bool
	RunVisible  func(types.Build) bool
}

// nestNode is one path of the include tree: what renders of the rows it
// reaches, and the paths one step further, in the order their groups
// render. props says the rows' properties render, keys limiting them to
// those keys when it is not nil; stats are the statistic fields an item
// renders. propsWhole and statsWhole tell the property and statistics rows
// of folded rows apart by the identity of the row they belong to.
type nestNode struct {
	path       string
	rel        rel
	whole      bool
	fields     []string
	children   []*nestNode
	props      bool
	keys       []string
	propsWhole bool
	stats      []StatField
	statsWhole bool
}

// relGroup is the rows of one related domain nested in a row, under key.
type relGroup struct {
	key  string
	rows []*relRow
	byID map[string]*relRow
}

// relRow is one nested row, or a found row's holder of what nests in it:
// the groups nested in it, one per child of its node, and the property and
// statistics rows of the rows folded into it, each once (seen).
type relRow struct {
	r      *row
	node   *nestNode
	groups []relGroup
	props  [][2]string
	stats  [][]byte
	seen   map[string]bool
}

// relGroupKeys are the keys Artifactory nests each domain's rows under
// (AqlRestResult.Row).
var relGroupKeys = map[rel]string{
	relItem: "items", relArtifact: "artifacts", relDependency: "dependencies",
	relModule: "modules", relBuild: "builds", relPromotion: "build.promotions",
}

// relFieldPrefixes are what precedes a field's name in a nested row of
// each domain (AqlRestResult.Row's JSON names).
var relFieldPrefixes = map[rel]string{
	relItem: "", relArtifact: "artifact.", relDependency: "dependency.", relModule: "module.",
	relBuild: "build.", relPromotion: "build.promotion.",
}

// propGroupKeys and propFieldPrefixes are the key a row's properties nest
// under and what precedes "key" and "value" in each property row, for the
// domains that have properties (AqlRestResult.Row: "properties" with
// "key", "module.properties" with "module.property.key", and
// "build.properties" with "build.property.key"). Statistics nest under
// "stats" with the names an items query's own use.
var (
	propGroupKeys = map[rel]string{
		relItem: "properties", relModule: "module.properties", relBuild: "build.properties",
	}
	propFieldPrefixes = map[rel]string{
		relItem: "", relModule: "module.property.", relBuild: "build.property.",
	}
)

// renderOrder lists the fields of r in the order a nested row renders
// them: the item fields as items render, the others alphabetically, as
// Artifactory's rows serialise them.
func renderOrder(r rel) []string {
	switch r {
	case relItem:
		out := make([]string, len(fieldOrder))
		for i, f := range fieldOrder {
			out[i] = string(f)
		}
		return out
	case relBuild:
		return buildFieldOrder
	case relPromotion:
		return promotionFieldOrder
	}
	out := make([]string, 0, len(relFields[r]))
	for f := range relFields[r] {
		out = append(out, f)
	}
	slices.Sort(out)
	return out
}

// includeTree is the tree of the related includes from the primary domain
// primary: one node per path an include names or passes through.
func includeTree(primary rel, related []relInclude) *nestNode {
	nodes := map[string]*nestNode{"": {rel: primary}}
	var node func(path string) *nestNode
	node = func(path string) *nestNode {
		if n, ok := nodes[path]; ok {
			return n
		}
		n := &nestNode{path: path, rel: pathRel(primary, path)}
		nodes[path] = n
		parent := node(parentPath(path))
		parent.children = append(parent.children, n)
		return n
	}
	// Every key, or every statistic, once one include asks for them all.
	allKeys, allStats := map[*nestNode]bool{}, map[*nestNode]bool{}
	for _, ri := range related {
		n := node(ri.path)
		switch ri.part {
		case partProperties:
			n.props, n.propsWhole = true, n.propsWhole || ri.whole
			allKeys[n] = allKeys[n] || ri.key == ""
			if ri.key != "" && !slices.Contains(n.keys, ri.key) {
				n.keys = append(n.keys, ri.key)
			}
		case partStats:
			n.statsWhole = n.statsWhole || ri.whole
			allStats[n] = allStats[n] || ri.whole
			if ri.field != "" && !slices.Contains(n.stats, StatField(ri.field)) {
				n.stats = append(n.stats, StatField(ri.field))
			}
		default:
			n.whole = n.whole || ri.whole
			if ri.field != "" && !slices.Contains(n.fields, ri.field) {
				n.fields = append(n.fields, ri.field)
			}
		}
	}
	for _, n := range nodes {
		order := renderOrder(n.rel)
		if n.whole {
			n.fields = order
		} else {
			n.fields = slices.DeleteFunc(slices.Clone(order), func(f string) bool {
				return !slices.Contains(n.fields, f)
			})
		}
		if allKeys[n] {
			n.keys = nil
		}
		named := n.stats
		n.stats = slices.DeleteFunc(slices.Clone(statOrder), func(f StatField) bool {
			return !allStats[n] && !slices.Contains(named, f)
		})
		slices.SortFunc(n.children, func(a, b *nestNode) int {
			return cmp.Compare(relGroupKeys[a.rel], relGroupKeys[b.rel])
		})
	}
	return nodes[""]
}

// paths lists the paths below n, parents first.
func (n *nestNode) paths() []string {
	var out []string
	for _, c := range n.children {
		out = append(out, c.path)
		out = append(out, c.paths()...)
	}
	return out
}

// nester folds the related rows of one query's found rows into them.
type nester struct {
	ctx    context.Context
	src    Source
	opt    Nesting
	now    time.Time
	tree   *nestNode
	plan   plan
	joined *Joined
	c      *Corpus
	tries  int
	// What has been loaded: runs by ID, files by kind and SHA-1, items by
	// SHA-1, promotions by run ID.
	runs, promos map[int64]bool
	files, sha1s map[string]bool
	visibleRuns  map[int64]bool
	visibleItems map[int64]bool
}

func (q *Query) nester(ctx context.Context, src Source, opt Nesting) *nester {
	now := opt.Now
	if now.IsZero() {
		now = time.Now()
	}
	tree := includeTree(q.Domain.rel(), q.related)
	x := &nester{
		ctx: ctx, src: src, opt: opt, now: now, tree: tree, c: &Corpus{},
		runs: map[int64]bool{}, promos: map[int64]bool{}, files: map[string]bool{},
		sha1s: map[string]bool{}, visibleRuns: map[int64]bool{}, visibleItems: map[int64]bool{},
	}
	if root, ok := q.Criteria.(And); ok {
		for _, n := range root {
			if j, ok := n.(*Joined); ok {
				x.joined = j
			}
		}
	}
	var crit Node
	if x.joined != nil {
		crit = x.joined.Criteria
	}
	x.plan = newPlan(q.Domain.rel(), crit, tree.paths()...)
	return x
}

// Related holds, for each row a query found, the rows of other domains its
// include names, which Response.WithRelated nests in the response.
type Related struct {
	holders []*relRow
}

// Nest finds the rows of the other domains q's include names for each hit.
// The hits must carry the properties and statistics q's criteria read.
// Rows src cannot load make the error; a query whose include reaches too
// many combinations of related rows wraps ErrInvalid.
func (q *Query) Nest(ctx context.Context, src Source, opt Nesting, hits []Hit) (Related, error) {
	if !q.Relates() {
		return Related{}, nil
	}
	x := q.nester(ctx, src, opt)
	found := make([]*row, len(hits))
	for i := range hits {
		found[i] = &row{rel: relItem, hit: &hits[i]}
	}
	holders, err := x.run(found)
	return Related{holders: holders}, err
}

// NestBuilds is Nest for the hits of a builds or build.promotions query.
func (q *Query) NestBuilds(
	ctx context.Context,
	src Source,
	opt Nesting,
	hits []BuildHit,
) (Related, error) {
	if !q.Relates() {
		return Related{}, nil
	}
	x := q.nester(ctx, src, opt)
	found := make([]*row, len(hits))
	for i := range hits {
		if p := hits[i].Promotion; p != nil {
			found[i] = &row{rel: relPromotion, promo: p}
			continue
		}
		// A hit's run has its own columns only; the criteria and the
		// include read its modules and properties.
		if err := x.loadRuns([]int64{hits[i].Build.ID}); err != nil {
			return Related{}, err
		}
		found[i] = &row{rel: relBuild, build: x.c.index().builds[hits[i].Build.ID]}
	}
	holders, err := x.run(found)
	return Related{holders: holders}, err
}

// WithRelated nests rel's rows in the response's rows, which must be those
// of the hits Nest or NestBuilds was given, in the same order.
func (r Response) WithRelated(rel Related) Response {
	for i := range min(len(r.Rows), len(rel.holders)) {
		r.Rows[i].related = rel.holders[i]
	}
	return r
}

// run loads what the plan's paths reach from found, then folds each found
// row's satisfying combinations into its holder: the groups nested in it
// and, for a run, its own properties.
func (x *nester) run(found []*row) ([]*relRow, error) {
	reach := make([][]*row, len(x.plan.nodes))
	for i, n := range x.plan.nodes {
		parents := found
		if n.parent >= 0 {
			parents = reach[n.parent]
		}
		if err := x.load(parents, n.rel); err != nil {
			return nil, err
		}
		seen := map[string]bool{}
		for _, p := range parents {
			for _, r := range x.c.related(p, n.rel) {
				if id := identity(r); !seen[id] {
					seen[id] = true
					reach[i] = append(reach[i], r)
				}
			}
		}
	}
	out := make([]*relRow, len(found))
	for i, f := range found {
		holder := &relRow{r: f, node: x.tree, groups: newGroups(x.tree)}
		holder.fold(f)
		e := &env{c: x.c, now: x.now, self: f, plan: x.plan, rows: make([]*row, len(x.plan.nodes))}
		if err := x.combine(e, 0, holder); err != nil {
			return nil, err
		}
		out[i] = holder
	}
	return out, nil
}

// combine tries every combination of the rows plan nodes i onwards reach,
// as env.combine does, and records each that satisfies the relation-path
// criteria.
func (x *nester) combine(e *env, i int, holder *relRow) error {
	if i == len(e.plan.nodes) {
		x.tries++
		if x.tries > maxNestCombinations {
			return fmt.Errorf("%w: the include reaches more than %d combinations of related rows",
				ErrInvalid, maxNestCombinations)
		}
		if x.joined == nil || e.eval(x.joined.Criteria) {
			x.record(e, holder)
		}
		return nil
	}
	n := e.plan.nodes[i]
	parent := e.self
	if n.parent >= 0 {
		parent = e.rows[n.parent]
	}
	var rows []*row
	if parent != nil {
		rows = e.c.related(parent, n.rel)
	}
	if len(rows) == 0 {
		e.rows[i] = nil
		return x.combine(e, i+1, holder)
	}
	for _, r := range rows {
		e.rows[i] = r
		if err := x.combine(e, i+1, holder); err != nil {
			return err
		}
	}
	return nil
}

func newGroups(n *nestNode) []relGroup {
	out := make([]relGroup, len(n.children))
	for i, c := range n.children {
		out[i] = relGroup{key: relGroupKeys[c.rel], byID: map[string]*relRow{}}
	}
	return out
}

// record adds the rows of one combination below at, along the include
// tree. A path the combination reaches no row on, or a row the caller may
// not see, adds nothing there or below: neither the row nor its properties
// and statistics.
func (x *nester) record(e *env, at *relRow) {
	for gi, child := range at.node.children {
		r := e.rows[e.plan.at[child.path]]
		if r == nil || !x.visible(r) {
			continue
		}
		g := &at.groups[gi]
		key := rowKey(r, child)
		nr, ok := g.byID[key]
		if !ok {
			nr = &relRow{r: r, node: child, groups: newGroups(child)}
			g.byID[key] = nr
			g.rows = append(g.rows, nr)
		}
		nr.fold(r)
		x.record(e, nr)
	}
}

// fold adds the properties and statistics of r, one of the rows rr stands
// for, as rr's node renders them. Folded rows list each property, and each
// statistics record, once by its values, as Artifactory's InflatableRow
// keys the rows of a domain by the values selected; an include of the
// whole property or statistic domain selects the row's id too, so then
// each row's are kept apart.
func (rr *relRow) fold(r *row) {
	n := rr.node
	if !n.props && len(n.stats) == 0 {
		return
	}
	if rr.seen == nil {
		rr.seen = map[string]bool{}
	}
	// Each combination that holds r folds it again; once is enough.
	folded := "r" + identity(r)
	if rr.seen[folded] {
		return
	}
	rr.seen[folded] = true
	once := func(kind, values string, whole bool) bool {
		id := kind + values
		if whole {
			id += "\x00" + identity(r)
		}
		if rr.seen[id] {
			return false
		}
		rr.seen[id] = true
		return true
	}
	if n.props {
		props := r.properties()
		keys := slices.Sorted(maps.Keys(props))
		for _, k := range keys {
			if n.keys != nil && !slices.Contains(n.keys, k) {
				continue
			}
			for _, v := range slices.Sorted(slices.Values(props[k])) {
				if once("p", k+"\x00"+v, n.propsWhole) {
					rr.props = append(rr.props, [2]string{k, v})
				}
			}
		}
	}
	if len(n.stats) > 0 && r.rel == relItem && r.hit.Stats != nil {
		var b bytes.Buffer
		writeStats(&b, r.hit.Stats, n.stats)
		// A record with none of the included values shows nothing, and
		// Artifactory drops related rows without data.
		if obj := b.Bytes(); len(obj) > 2 && once("s", string(obj), n.statsWhole) {
			rr.stats = append(rr.stats, obj)
		}
	}
}

// visible applies the caller's visibility to a related row: an item's
// own, and for a run, module, file or promotion that of its run, which a
// promotion is nested below.
func (x *nester) visible(r *row) bool {
	switch {
	case r.rel == relItem:
		if x.opt.ItemVisible == nil {
			return true
		}
		id := r.hit.Item.ID
		v, ok := x.visibleItems[id]
		if !ok {
			v = x.opt.ItemVisible(r.hit.Item)
			x.visibleItems[id] = v
		}
		return v
	case r.build == nil || x.opt.RunVisible == nil:
		return true
	}
	v, ok := x.visibleRuns[r.build.ID]
	if !ok {
		v = x.opt.RunVisible(*r.build)
		x.visibleRuns[r.build.ID] = v
	}
	return v
}

// identity names a related row uniquely: its record's ID, or for a module
// or a build file its place in its run.
func identity(r *row) string {
	switch r.rel {
	case relItem:
		return "i" + strconv.FormatInt(r.hit.Item.ID, 10)
	case relBuild:
		return "b" + strconv.FormatInt(r.build.ID, 10)
	case relPromotion:
		return "p" + strconv.FormatInt(r.promo.ID, 10)
	case relModule:
		return fmt.Sprintf("m%d/%d", r.build.ID, r.module)
	}
	// A file's address is its place in the run's module, shared by every
	// row any index of the corpus makes for it.
	return fmt.Sprintf("f%d/%d/%p", r.build.ID, r.module, r.file)
}

// rowKey tells the rows of one nested group apart: by the values of the
// fields that render, and by identity when the whole domain is included.
func rowKey(r *row, n *nestNode) string {
	var b strings.Builder
	if n.whole {
		b.WriteString(identity(r))
	}
	for _, f := range n.fields {
		v, _ := relValue(r, f)
		b.WriteString("\x00" + fmt.Sprint(v))
	}
	return b.String()
}

// relValue is field f of a related row as it renders; ok is false for a
// value left out, as Artifactory leaves out nulls.
func relValue(r *row, f string) (any, bool) {
	if r.rel == relItem {
		it := r.hit.Item
		switch fieldKinds[Field(f)] {
		case kindType:
			return itemType(it), true
		case kindNumber:
			return number(it, Field(f)), true
		case kindTime:
			t := itemTime(it, Field(f))
			return t.UTC().Format(timeFormat), !t.IsZero()
		}
		s := text(it, Field(f))
		return s, s != "" || f == string(FieldRepo) || f == string(FieldPath) ||
			f == string(FieldName)
	}
	if k, _ := r.rel.fieldKind(f); k == kindTime {
		t := r.time(f)
		return t.UTC().Format(timeFormat), !t.IsZero()
	}
	s := r.text(f)
	return s, s != ""
}

// load fetches from the source what the step from parents to domain to
// needs and the corpus lacks.
func (x *nester) load(parents []*row, to rel) error {
	if len(parents) == 0 {
		return nil
	}
	from := parents[0].rel
	switch {
	case from == relItem:
		return x.loadFiles(parents, to == relDependency)
	case to == relItem:
		return x.loadItems(parents)
	case to == relPromotion:
		return x.loadPromotions(parents)
	case from == relPromotion:
		ids := make([]int64, len(parents))
		for i, p := range parents {
			ids[i] = p.promo.BuildID
		}
		return x.loadRuns(ids)
	}
	// Modules and files come with their run.
	return nil
}

func (x *nester) loadFiles(items []*row, dependency bool) error {
	var added []types.Build
	for _, it := range items {
		sha1 := it.hit.Item.Actual.SHA1
		key := strconv.FormatBool(dependency) + sha1
		if sha1 == "" || x.files[key] {
			continue
		}
		x.files[key] = true
		runs, err := x.src.RunsWithFile(x.ctx, dependency, sha1)
		if err != nil {
			return fmt.Errorf("aql: runs with %s: %w", sha1, err)
		}
		for _, b := range runs {
			if !x.runs[b.ID] {
				x.runs[b.ID] = true
				added = append(added, b)
			}
		}
	}
	x.addRuns(added)
	return nil
}

func (x *nester) loadItems(files []*row) error {
	var added []Hit
	have := map[int64]bool{}
	for _, h := range x.c.Items {
		have[h.Item.ID] = true
	}
	for _, f := range files {
		sha1 := f.file.Checksums.SHA1
		if sha1 == "" || x.sha1s[sha1] {
			continue
		}
		x.sha1s[sha1] = true
		hits, err := x.src.ItemsWithSHA1(x.ctx, sha1)
		if err != nil {
			return fmt.Errorf("aql: items with %s: %w", sha1, err)
		}
		for _, h := range hits {
			if !have[h.Item.ID] {
				have[h.Item.ID] = true
				added = append(added, h)
			}
		}
	}
	if len(added) > 0 {
		// A new slice, so the rows already made keep pointing at what
		// they were made from.
		items := append(slices.Clone(x.c.Items), added...)
		slices.SortFunc(items, func(a, b Hit) int { return cmp.Compare(a.Item.ID, b.Item.ID) })
		x.c.Items, x.c.idx = items, nil
	}
	return nil
}

func (x *nester) loadPromotions(builds []*row) error {
	var added []types.BuildPromotion
	for _, b := range builds {
		id := b.build.ID
		if x.promos[id] {
			continue
		}
		x.promos[id] = true
		ps, err := x.src.RunPromotions(x.ctx, id)
		if err != nil {
			return fmt.Errorf("aql: promotions of run %d: %w", id, err)
		}
		added = append(added, ps...)
	}
	if len(added) > 0 {
		promos := append(slices.Clone(x.c.Promotions), added...)
		slices.SortFunc(
			promos,
			func(a, b types.BuildPromotion) int { return cmp.Compare(a.ID, b.ID) },
		)
		x.c.Promotions, x.c.idx = promos, nil
	}
	return nil
}

func (x *nester) loadRuns(ids []int64) error {
	var added []types.Build
	for _, id := range ids {
		if x.runs[id] {
			continue
		}
		x.runs[id] = true
		b, err := x.src.Run(x.ctx, id)
		if err != nil {
			return fmt.Errorf("aql: run %d: %w", id, err)
		}
		added = append(added, b)
	}
	x.addRuns(added)
	return nil
}

// addRuns adds runs to the corpus in a new slice ordered by ID, so the rows
// already made keep pointing at the runs they were made from.
func (x *nester) addRuns(added []types.Build) {
	if len(added) == 0 {
		return
	}
	runs := append(slices.Clone(x.c.Builds), added...)
	slices.SortFunc(runs, func(a, b types.Build) int { return cmp.Compare(a.ID, b.ID) })
	x.c.Builds, x.c.idx = runs, nil
}

// RunsWithFile implements Source over the corpus.
func (c *Corpus) RunsWithFile(
	_ context.Context,
	dependency bool,
	sha1 string,
) ([]types.Build, error) {
	x := c.index()
	files := x.artifacts[sha1]
	if dependency {
		files = x.dependencies[sha1]
	}
	var out []types.Build
	for _, f := range files {
		if !slices.ContainsFunc(out, func(b types.Build) bool { return b.ID == f.build.ID }) {
			out = append(out, *f.build)
		}
	}
	slices.SortFunc(out, func(a, b types.Build) int { return cmp.Compare(a.ID, b.ID) })
	return out, nil
}

// ItemsWithSHA1 implements Source over the corpus.
func (c *Corpus) ItemsWithSHA1(_ context.Context, sha1 string) ([]Hit, error) {
	rows := c.index().items[sha1]
	out := make([]Hit, len(rows))
	for i, r := range rows {
		out[i] = *r.hit
	}
	slices.SortFunc(out, func(a, b Hit) int { return cmp.Compare(a.Item.ID, b.Item.ID) })
	return out, nil
}

// Run implements Source over the corpus.
func (c *Corpus) Run(_ context.Context, id int64) (types.Build, error) {
	b, ok := c.index().builds[id]
	if !ok {
		return types.Build{}, fmt.Errorf("%w: %d", errNoRun, id)
	}
	return *b, nil
}

// RunPromotions implements Source over the corpus.
func (c *Corpus) RunPromotions(_ context.Context, id int64) ([]types.BuildPromotion, error) {
	rows := c.index().promotions[id]
	out := make([]types.BuildPromotion, len(rows))
	for i, r := range rows {
		out[i] = *r.promo
	}
	return out, nil
}
