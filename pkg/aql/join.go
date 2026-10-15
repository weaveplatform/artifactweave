package aql

import (
	"cmp"
	"slices"
	"strings"
	"time"

	"github.com/weaveplatform/artifactweave/pkg/types"
)

// Corpus is everything the reference evaluator may find or join: the items
// with their properties and statistics, the build runs with their
// properties and modules, and the promotions. It is read, never changed;
// SelectItems and SelectBuilds index it on first use, so it must not change
// between calls.
type Corpus struct {
	Items      []Hit
	Builds     []types.Build
	Promotions []types.BuildPromotion
	idx        *corpusIndex
}

// BuildHit is one row a builds or build.promotions query found: the run,
// without its modules, properties or document, and for build.promotions
// the promotion.
type BuildHit struct {
	Build     types.Build
	Promotion *types.BuildPromotion
}

type corpusIndex struct {
	items        map[string][]*row
	artifacts    map[string][]*row
	dependencies map[string][]*row
	builds       map[int64]*types.Build
	promotions   map[int64][]*row
}

// row is one row of a domain: an item, a run, a promotion, one module of a
// run, or one file of a module. A nil *row is the null row an outer join
// gives a path that reaches nothing.
type row struct {
	rel    rel
	hit    *Hit
	build  *types.Build
	module int
	file   *types.BuildFile
	promo  *types.BuildPromotion
}

func (c *Corpus) index() *corpusIndex {
	if c.idx != nil {
		return c.idx
	}
	x := &corpusIndex{
		items: map[string][]*row{}, artifacts: map[string][]*row{},
		dependencies: map[string][]*row{}, builds: map[int64]*types.Build{},
		promotions: map[int64][]*row{},
	}
	for i := range c.Items {
		h := &c.Items[i]
		if sha1 := h.Item.Actual.SHA1; sha1 != "" {
			x.items[sha1] = append(x.items[sha1], &row{rel: relItem, hit: h})
		}
	}
	for i := range c.Builds {
		b := &c.Builds[i]
		x.builds[b.ID] = b
		for m := range b.Modules {
			files(b, m, relArtifact, func(r *row) {
				x.artifacts[r.file.Checksums.SHA1] = append(x.artifacts[r.file.Checksums.SHA1], r)
			})
			files(b, m, relDependency, func(r *row) {
				x.dependencies[r.file.Checksums.SHA1] = append(
					x.dependencies[r.file.Checksums.SHA1], r,
				)
			})
		}
	}
	for i := range c.Promotions {
		p := &c.Promotions[i]
		x.promotions[p.BuildID] = append(x.promotions[p.BuildID], &row{rel: relPromotion, promo: p})
	}
	c.idx = x
	return x
}

// files calls fn with a row for each artifact or dependency of module m.
func files(b *types.Build, m int, r rel, fn func(*row)) {
	list := b.Modules[m].Artifacts
	if r == relDependency {
		list = b.Modules[m].Dependencies
	}
	for i := range list {
		fn(&row{rel: r, build: b, module: m, file: &list[i]})
	}
}

// related returns the rows of domain to that r relates to, as the SQL
// joins in joinsql.go link them. A file reaches items only through a SHA-1
// it has.
func (c *Corpus) related(r *row, to rel) []*row {
	x := c.index()
	switch {
	case r.rel == relItem:
		sha1 := r.hit.Item.Actual.SHA1
		if sha1 == "" {
			return nil
		}
		if to == relArtifact {
			return x.artifacts[sha1]
		}
		return x.dependencies[sha1]
	case to == relItem:
		if r.file.Checksums.SHA1 == "" {
			return nil
		}
		return x.items[r.file.Checksums.SHA1]
	case to == relModule && r.rel == relBuild:
		out := make([]*row, len(r.build.Modules))
		for m := range r.build.Modules {
			out[m] = &row{rel: relModule, build: r.build, module: m}
		}
		return out
	case to == relModule:
		return []*row{{rel: relModule, build: r.build, module: r.module}}
	case to == relArtifact || to == relDependency:
		var out []*row
		files(r.build, r.module, to, func(f *row) { out = append(out, f) })
		return out
	case to == relPromotion:
		return x.promotions[r.build.ID]
	case r.rel == relPromotion:
		if b, ok := x.builds[r.promo.BuildID]; ok {
			return []*row{{rel: relBuild, build: b}}
		}
		return nil
	}
	// A module's run.
	return []*row{{rel: relBuild, build: r.build}}
}

// text is a text field of a build-domain row.
func (r *row) text(f string) string {
	switch r.rel {
	case relBuild:
		return buildText(r.build, f)
	case relPromotion:
		return promotionText(r.promo, f)
	case relModule:
		return r.build.Modules[r.module].ID
	}
	switch f {
	case "name":
		if r.rel == relDependency {
			return r.file.ID
		}
		return r.file.Name
	case "type":
		return r.file.Type
	case "sha1":
		return r.file.Checksums.SHA1
	case "md5":
		return r.file.Checksums.MD5
	}
	return strings.Join(r.file.Scopes, ",")
}

func buildText(b *types.Build, f string) string {
	switch f {
	case "url":
		return b.URL
	case "name":
		return b.Name
	case "number":
		return b.Number
	case "created_by":
		return b.CreatedBy
	case "modified_by":
		return b.ModifiedBy
	}
	return types.BuildInfoRepo(b.ProjectKey)
}

func promotionText(p *types.BuildPromotion, f string) string {
	switch f {
	case "created_by":
		return p.CreatedBy
	case "status":
		return p.Status
	case "repo":
		return p.Repo
	case "comment":
		return p.Comment
	}
	return p.CIUser
}

// time is a time field of a run or a promotion, truncated to the
// microseconds the SQL stores keep; zero is SQL NULL.
func (r *row) time(f string) time.Time {
	var t time.Time
	switch {
	case r.rel == relPromotion:
		t = r.promo.Created
	case f == "created":
		t = r.build.Created
	case f == "modified":
		t = r.build.Modified
	default:
		t = r.build.Started
	}
	if t.IsZero() {
		return t
	}
	return micro(t)
}

func (r *row) properties() types.Properties {
	switch r.rel {
	case relItem:
		return r.hit.Properties
	case relBuild:
		return r.build.Properties
	}
	return r.build.Modules[r.module].Properties
}

// match compares the field on r.
func (c *PathCriterion) match(r *row, now time.Time) bool {
	if r.rel == relItem {
		fc := FieldCriterion{
			Field: Field(c.Field), Op: c.Op, Text: c.Text, Number: c.Number, Time: c.Time,
			Span: c.Span,
		}
		return fc.match(r.hit.Item, now)
	}
	if c.kind == kindTime {
		t := r.time(c.Field)
		if t.IsZero() {
			return false
		}
		switch c.Op {
		case OpBefore:
			return t.Before(micro(c.Span.back(now)))
		case OpLast:
			return t.After(micro(c.Span.back(now)))
		}
		return compareOp(c.Op, t.Compare(micro(c.Time)))
	}
	s := r.text(c.Field)
	switch c.Op {
	case OpMatch:
		return globMatch(c.Text, s)
	case OpNMatch:
		return !globMatch(c.Text, s)
	}
	return compareOp(c.Op, strings.Compare(s, c.Text))
}

// plan lists the paths a Joined reaches, and the paths leading to them,
// parents first; at indexes it by path. The SQL compiler joins the same
// tables in the same order.
type plan struct {
	nodes []planNode
	at    map[string]int
}

type planNode struct {
	path   string
	rel    rel
	parent int // index into nodes, or -1 for the found row
}

func newPlan(primary rel, n Node) plan {
	seen := map[string]bool{}
	var paths []string
	var walk func(Node)
	add := func(path string) {
		for ; path != "" && !seen[path]; path = parentPath(path) {
			seen[path] = true
			paths = append(paths, path)
		}
	}
	walk = func(n Node) {
		switch n := n.(type) {
		case And:
			for _, c := range n {
				walk(c)
			}
		case Or:
			for _, c := range n {
				walk(c)
			}
		case *PathCriterion:
			add(n.Path)
		case *PathProperty:
			add(n.Path)
		case *PathMSP:
			add(n.Path)
		}
	}
	walk(n)
	slices.SortFunc(paths, func(a, b string) int {
		return cmp.Or(cmp.Compare(strings.Count(a, "."), strings.Count(b, ".")), cmp.Compare(a, b))
	})
	p := plan{nodes: make([]planNode, len(paths)), at: make(map[string]int, len(paths))}
	for i, path := range paths {
		parent := -1
		if pp := parentPath(path); pp != "" {
			parent = p.at[pp]
		}
		p.nodes[i] = planNode{path: path, rel: pathRel(primary, path), parent: parent}
		p.at[path] = i
	}
	return p
}

// inner reports whether no combination with a null row satisfies crit: an
// inner join then finds the same rows as the outer joins, which lets the
// database drive the join from the related table's index.
func (p plan) inner(crit Node) bool {
	for _, n := range p.nodes {
		if !falseWithout(crit, n.path) {
			return false
		}
	}
	return true
}

// falseWithout reports whether n cannot hold when the row at dead, and so
// every row below it, is null.
func falseWithout(n Node, dead string) bool {
	switch n := n.(type) {
	case And:
		return slices.ContainsFunc(n, func(c Node) bool { return falseWithout(c, dead) })
	case Or:
		return !slices.ContainsFunc(n, func(c Node) bool { return !falseWithout(c, dead) })
	case *PathCriterion:
		return n.Path != "" && under(n.Path, dead)
	case *PathProperty:
		return n.Path != "" && under(n.Path, dead)
	case *PathMSP:
		return n.Path != "" && under(n.Path, dead)
	}
	return false
}

// env evaluates criteria for one found row, self, over the combination of
// related rows the current Joined has reached.
type env struct {
	c    *Corpus
	now  time.Time
	self *row
	plan plan
	rows []*row
}

func (e *env) at(path string) *row {
	if path == "" {
		return e.self
	}
	return e.rows[e.plan.at[path]]
}

func (e *env) eval(n Node) bool {
	switch n := n.(type) {
	case And:
		for _, c := range n {
			if !e.eval(c) {
				return false
			}
		}
		return true
	case Or:
		for _, c := range n {
			if e.eval(c) {
				return true
			}
		}
		return false
	case *Joined:
		saved, rows := e.plan, e.rows
		e.plan = newPlan(e.self.rel, n.Criteria)
		e.rows = make([]*row, len(e.plan.nodes))
		ok := e.combine(0, n.Criteria)
		e.plan, e.rows = saved, rows
		return ok
	case *PathCriterion:
		r := e.at(n.Path)
		return r != nil && n.match(r, e.now)
	case *PathProperty:
		r := e.at(n.Path)
		return r != nil && n.Criterion.match(r.properties())
	case *PathMSP:
		r := e.at(n.Path)
		return r != nil && n.MSP.match(r.properties())
	}
	// A criterion on the found item itself.
	return e.self.hit != nil && match(n, *e.self.hit, e.now)
}

// combine tries every combination of the rows plan nodes i onwards reach,
// the null row where one reaches nothing, until one satisfies crit.
func (e *env) combine(i int, crit Node) bool {
	if i == len(e.plan.nodes) {
		return e.eval(crit)
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
		return e.combine(i+1, crit)
	}
	for _, r := range rows {
		e.rows[i] = r
		if e.combine(i+1, crit) {
			return true
		}
	}
	return false
}

// SelectItems is Select over a corpus: the candidates are c.Items, and the
// relation paths in q's criteria join them to c's builds and promotions.
func SelectItems(q *Query, o Options, c *Corpus) []Hit {
	e := env{c: c, now: o.now()}
	var out []Hit
	for i := range c.Items {
		e.self = &row{rel: relItem, hit: &c.Items[i]}
		if q.Criteria != nil && !e.eval(q.Criteria) {
			continue
		}
		h := c.Items[i]
		if !q.Properties() {
			h.Properties = nil
		}
		if len(q.Stats()) == 0 {
			h.Stats = nil
		}
		out = append(out, h)
	}
	slices.SortFunc(out, func(a, b Hit) int { return q.Compare(a.Item, b.Item) })
	return window(q, o, out)
}

// SelectBuilds runs a builds or build.promotions query over a corpus, as
// the SQL compiler's output does: filter, sort (by ID when the query does
// not sort, and by ID within equal sort keys), then offset and limit. A
// promotion of a run the corpus lacks is skipped, as the SQL join skips it.
func SelectBuilds(q *Query, o Options, c *Corpus) []BuildHit {
	e := env{c: c, now: o.now()}
	var out []BuildHit
	switch q.Domain {
	case DomainBuilds:
		for i := range c.Builds {
			b := &c.Builds[i]
			e.self = &row{rel: relBuild, build: b}
			if q.Criteria == nil || e.eval(q.Criteria) {
				out = append(out, BuildHit{Build: runOf(b)})
			}
		}
	case DomainPromotions:
		x := c.index()
		for i := range c.Promotions {
			p := &c.Promotions[i]
			b, ok := x.builds[p.BuildID]
			if !ok {
				continue
			}
			e.self = &row{rel: relPromotion, promo: p}
			if q.Criteria == nil || e.eval(q.Criteria) {
				promo := *p
				out = append(out, BuildHit{Build: runOf(b), Promotion: &promo})
			}
		}
	}
	slices.SortFunc(out, q.compareBuildHits)
	return window(q, o, out)
}

// runOf is the run's own columns, which is what a store's search returns.
func runOf(b *types.Build) types.Build {
	return types.Build{
		ID: b.ID, ProjectKey: b.ProjectKey, Name: b.Name, Number: b.Number, Started: b.Started,
		URL: b.URL, Created: b.Created, CreatedBy: b.CreatedBy, Modified: b.Modified,
		ModifiedBy: b.ModifiedBy,
	}
}

func window[T any](q *Query, o Options, out []T) []T {
	offset, limit := q.Window(o)
	out = out[min(offset, len(out)):]
	if limit >= 0 && limit < len(out) {
		out = out[:limit]
	}
	return out
}

// hitRow is the found row of a builds or build.promotions hit.
func hitRow(h *BuildHit) *row {
	if h.Promotion != nil {
		return &row{rel: relPromotion, promo: h.Promotion}
	}
	return &row{rel: relBuild, build: &h.Build}
}

// compareBuildHits orders two hits by the query's sort, then by ID, as the
// SQL ORDER BY does. A missing time sorts first ascending and last
// descending.
func (q *Query) compareBuildHits(a, b BuildHit) int {
	ra, rb := hitRow(&a), hitRow(&b)
	if q.Sort != nil {
		for _, f := range q.Sort.Fields {
			var c int
			if k, _ := ra.rel.fieldKind(string(f)); k == kindTime {
				c = ra.time(string(f)).Compare(rb.time(string(f)))
			} else {
				c = strings.Compare(ra.text(string(f)), rb.text(string(f)))
			}
			if q.Sort.Desc {
				c = -c
			}
			if c != 0 {
				return c
			}
		}
	}
	if a.Promotion != nil && b.Promotion != nil {
		return cmp.Compare(a.Promotion.ID, b.Promotion.ID)
	}
	return cmp.Compare(a.Build.ID, b.Build.ID)
}

// domainFields returns the fields builds and build.promotions rows carry,
// in render order: the included ones, or all of them, which is
// Artifactory's default for these domains.
func (q *Query) domainFields() []Field {
	order := buildFieldOrder
	if q.Domain == DomainPromotions {
		order = promotionFieldOrder
	}
	var out []Field
	for _, f := range order {
		if slices.Contains(q.Include, f) || slices.Contains(q.Include, "*") {
			out = append(out, Field(f))
		}
	}
	if len(out) == 0 {
		for _, f := range order {
			out = append(out, Field(f))
		}
	}
	return out
}
