package aql

import (
	"strconv"
)

// relTables are the tables of the domains a relation path reaches
// (migration 0012 and the items tables).
var relTables = map[rel]string{
	relItem: "items", relBuild: "builds", relPromotion: "build_promotions",
	relModule: "build_modules", relArtifact: "build_artifacts", relDependency: "build_dependencies",
}

// relKeys name a column of each table that is never NULL in a real row,
// so a NULL there marks the null row of an outer join.
var relKeys = map[rel]string{
	relItem: "id", relBuild: "id", relPromotion: "id",
	relModule: "build_id", relArtifact: "build_id", relDependency: "build_id",
}

// joined writes a Joined as one EXISTS over the tables its paths reach,
// joined in plan order. When no null row can satisfy the criteria the
// joins are inner, so the database may start from the related table's
// index (for the JFrog CLI's build searches, from the build's name); the
// general case outer-joins from a one-row base, which gives a path that
// reaches nothing its null row, as the reference evaluator does.
func (c *compiler) joined(j *Joined) error {
	p := newPlan(c.primary, j.Criteria)
	if len(p.nodes) == 0 {
		// Only a hand-built Joined names no path: nothing to join.
		return c.node(j.Criteria)
	}
	inner := p.inner(j.Criteria)
	for _, n := range p.nodes {
		c.seq++
		c.alias[n.path] = "j" + strconv.Itoa(c.seq)
		c.nullable[n.path] = !inner
	}
	c.b.WriteString("EXISTS (SELECT 1 FROM ")
	if inner {
		for i, n := range p.nodes {
			if i > 0 {
				c.b.WriteString(" JOIN ")
			}
			c.b.WriteString(relTables[n.rel] + " " + c.alias[n.path])
			if i > 0 {
				c.b.WriteString(" ON " + c.link(p, n))
			}
		}
		c.b.WriteString(" WHERE " + c.link(p, p.nodes[0]) + " AND ")
	} else {
		c.b.WriteString("(SELECT 1 AS one) j0")
		for _, n := range p.nodes {
			c.b.WriteString(" LEFT JOIN " + relTables[n.rel] + " " + c.alias[n.path] +
				" ON " + c.link(p, n))
		}
		c.b.WriteString(" WHERE ")
	}
	if err := c.node(j.Criteria); err != nil {
		return err
	}
	c.b.WriteByte(')')
	return nil
}

// link is the join condition of plan node n to its parent, the relations
// of relSteps: items and build files by a SHA-1 the file has, files to
// their module, modules to their run, promotions to theirs.
func (c *compiler) link(p plan, n planNode) string {
	a, from := c.alias[n.path], c.primary
	parent := c.alias[""]
	if n.parent >= 0 {
		parent, from = c.alias[p.nodes[n.parent].path], p.nodes[n.parent].rel
	}
	switch {
	case from == relItem:
		return a + ".sha1 = " + parent + ".sha1 AND " + a + ".sha1 <> ''"
	case n.rel == relItem:
		return a + ".sha1 = " + parent + ".sha1 AND " + parent + ".sha1 <> ''"
	case n.rel == relModule && from == relBuild:
		return a + ".build_id = " + parent + ".id"
	case n.rel == relModule:
		return a + ".build_id = " + parent + ".build_id AND " + a + ".position = " +
			parent + ".module_position"
	case n.rel == relArtifact || n.rel == relDependency:
		return a + ".build_id = " + parent + ".build_id AND " + a + ".module_position = " +
			parent + ".position"
	case n.rel == relPromotion:
		return a + ".build_id = " + parent + ".id"
	case from == relPromotion:
		return a + ".id = " + parent + ".build_id"
	}
	// A module's run.
	return a + ".id = " + parent + ".build_id"
}

// guarded writes a criterion on the row at path, which fails on the null
// row of an outer join: comparisons with NULL fail by themselves, but a
// NOT EXISTS over a null row's properties would hold, and a dependency's
// scope is never NULL.
func (c *compiler) guarded(path string, r rel, write func()) {
	if !c.nullable[path] {
		write()
		return
	}
	c.b.WriteString("(" + c.alias[path] + "." + relKeys[r] + " IS NOT NULL AND ")
	write()
	c.b.WriteByte(')')
}

func (c *compiler) pathField(n *PathCriterion) {
	a := c.alias[n.Path]
	if n.rel == relItem {
		c.fieldOf(a, &FieldCriterion{
			Field: Field(n.Field), Op: n.Op, Text: n.Text, Number: n.Number, Time: n.Time,
			Span: n.Span,
		})
		return
	}
	if n.kind == kindTime {
		c.time(c.relColumn(a, n.rel, n.Field), n.Op, n.Time, n.Span)
		return
	}
	if n.Op == OpEq || n.Op == OpNe {
		c.text(c.relColumn(a, n.rel, n.Field), n.Op, n.Text)
		return
	}
	c.text(c.orderedRel(a, n.rel, n.Field, n.kind), n.Op, n.Text)
}

func (c *compiler) pathProperty(n *PathProperty) {
	c.property(n.rel, c.alias[n.Path], n.Criterion)
}

// orderedRel is the expression a build-domain field compares and sorts by:
// text byte by byte on PostgreSQL, as ordered explains.
func (c *compiler) orderedRel(alias string, r rel, f string, k kind) string {
	expr := c.relColumn(alias, r, f)
	if c.d == PostgreSQL && k == kindText {
		return expr + ` COLLATE "C"`
	}
	return expr
}

// relColumn is the expression of field f of a build-domain row at alias.
func (c *compiler) relColumn(alias string, r rel, f string) string {
	col := alias + "."
	switch {
	case r == relBuild && f == "repo":
		// types.BuildInfoRepo, whose encoding leaves a valid project key
		// (types.ValidateProjectKey: lowercase letters, digits and
		// hyphens) alone.
		return "(CASE WHEN " + col + "project_key = '' OR " + col +
			"project_key = 'default' THEN 'artifactory-build-info' ELSE " + col +
			"project_key || '-build-info' END)"
	case f == "created" || f == "modified" || f == "started":
		return col + f + "_at"
	case r == relPromotion && f == "user":
		return col + "ci_user"
	case r == relModule:
		return col + "module_id"
	case r == relDependency && f == "name":
		return col + "dependency_id"
	case r == relDependency && f == "scope":
		return c.scopes(col + "scopes")
	}
	return col + f
}

// scopes is a dependency's scopes, stored as a JSON array, joined with
// commas in array order as the reference evaluator joins them.
func (c *compiler) scopes(col string) string {
	if c.d == PostgreSQL {
		return "COALESCE((SELECT string_agg(s.value, ',' ORDER BY s.n) FROM " +
			"json_array_elements_text(" + col + "::json) WITH ORDINALITY AS s(value, n)), '')"
	}
	return "COALESCE((SELECT group_concat(s.value, ',' ORDER BY s.key) FROM json_each(" +
		col + ") s), '')"
}
