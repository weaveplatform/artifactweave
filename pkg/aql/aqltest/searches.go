package aqltest

import (
	"strings"
	"time"

	"github.com/weaveplatform/artifactweave/pkg/aql"
)

// searchCases are the query shapes the GET searches build in code
// (internal/server/searches.go), where the backends could part ways:
// criteria without the implicit type=file, a repos parameter as an $or of
// repository keys that resolve to a remote's cache and a virtual
// repository's members, inclusive time windows,
// a statistic that is NULL or before an instant, depth with globs on path
// and name, and pages of offset and limit that include properties or
// statistics, which AQL text refuses.
func searchCases() []Case {
	file := &aql.FieldCriterion{Field: aql.FieldType, Op: aql.OpEq, Text: "file"}
	repos := func(keys ...string) aql.Or {
		out := aql.Or{}
		for _, k := range keys {
			out = append(out, &aql.FieldCriterion{Field: aql.FieldRepo, Op: aql.OpEq, Text: k})
		}
		return out
	}
	field := func(f aql.Field, op aql.Op, text string) *aql.FieldCriterion {
		return &aql.FieldCriterion{Field: f, Op: op, Text: text}
	}
	at := func(minutes int) time.Time { return Base.Add(time.Duration(minutes) * time.Minute) }
	window := func(f aql.Field, from, to int) aql.And {
		return aql.And{
			&aql.FieldCriterion{Field: f, Op: aql.OpGte, Time: at(from)},
			&aql.FieldCriterion{Field: f, Op: aql.OpLte, Time: at(to)},
		}
	}
	prop := func(key string, op aql.Op, value string) *aql.PropertyCriterion {
		return &aql.PropertyCriterion{Part: aql.PartPair, Key: key, Op: op, Value: value}
	}
	unused := aql.And{
		file,
		&aql.FieldCriterion{Field: aql.FieldCreated, Op: aql.OpLt, Time: at(20)},
		aql.Or{
			&aql.StatCriterion{Field: aql.StatDownloaded, Op: aql.OpEq, Null: true},
			&aql.StatCriterion{Field: aql.StatDownloaded, Op: aql.OpLt, Time: at(40)},
		},
	}
	page := func(q aql.Query, offset, limit int) *aql.Query {
		q.Offset, q.HasOffset, q.Limit, q.HasLimit = offset, true, limit, true
		return &q
	}
	return []Case{
		{
			Name: "search/name in a repos scope with a remote",
			Built: &aql.Query{Criteria: aql.And{
				file, repos("libs-local", "remote"),
				field(aql.FieldName, aql.OpMatch, "*app-1.0*"),
			}},
			Want: []string{jar, pom, cached},
		},
		{
			Name: "search/repos naming a virtual repository",
			Built: &aql.Query{Criteria: aql.And{
				file, repos("generic-local", "libs-virtual"),
				field(aql.FieldName, aql.OpMatch, "*.jar"),
			}},
			Want: []string{jar, util, cached},
		},
		{
			Name:  "search/repos naming nothing",
			Built: &aql.Query{Criteria: aql.And{file, repos()}},
			Want:  []string{},
		},
		{
			Name:  "search/no type includes folders",
			Built: &aql.Query{Criteria: aql.And{repos("libs-local")}},
			Want: []string{
				"libs-local/org", "libs-local/org/acme", "libs-local/org/acme/lib",
				jar, pom, util, readme,
			},
		},
		{
			Name: "search/property values as alternatives",
			Built: &aql.Query{Criteria: aql.And{aql.Or{
				prop("release", aql.OpEq, "1.0"), prop("release", aql.OpMatch, "2.*"),
			}}},
			Want: []string{jar, util},
		},
		{
			Name: "search/every property named",
			Built: &aql.Query{Criteria: aql.And{
				aql.Or{prop("build.name", aql.OpEq, "*")}, aql.Or{prop("release", aql.OpEq, "*")},
			}},
			Want: []string{jar},
		},
		{
			Name: "search/a page with properties",
			Built: page(aql.Query{
				Criteria: aql.And{aql.Or{prop("build.name", aql.OpEq, "*")}},
				Include:  []string{"property"},
			}, 1, 1),
			Want: []string{pom},
		},
		{
			Name: "search/every checksum must match",
			Built: &aql.Query{Criteria: aql.And{
				file, field(aql.FieldActualMD5, aql.OpEq, strings.Repeat("e", 32)),
				field(aql.FieldActualSHA1, aql.OpEq, strings.Repeat("f", 40)),
			}},
			Want: []string{},
		},
		{
			Name: "search/checksums of one item",
			Built: &aql.Query{Criteria: aql.And{
				file, field(aql.FieldActualMD5, aql.OpEq, strings.Repeat("e", 32)),
				field(aql.FieldActualSHA1, aql.OpEq, strings.Repeat("e", 40)),
			}},
			Want: []string{jar},
		},
		{
			Name:  "search/creation window is inclusive",
			Built: &aql.Query{Criteria: aql.And{file, window(aql.FieldCreated, 4, 6)}},
			Want:  []string{jar, pom, util},
		},
		{
			Name: "search/dates any field in its window",
			Built: &aql.Query{
				Criteria: aql.And{file, aql.Or{
					window(aql.FieldModified, 15, 16),
					aql.And{
						&aql.StatCriterion{Field: aql.StatDownloaded, Op: aql.OpGte, Time: at(50)},
						&aql.StatCriterion{Field: aql.StatDownloaded, Op: aql.OpLte, Time: at(50)},
					},
				}},
				Include: []string{"stat"},
			},
			Want: []string{jar, pom, cached},
		},
		{
			Name:  "search/usage never or long ago downloaded",
			Built: &aql.Query{Criteria: unused},
			Want:  []string{jar, pom, util, readme, guide, aUnder, axb, pct, abc, naive},
		},
		{
			Name:  "search/usage a page with statistics",
			Built: page(aql.Query{Criteria: unused, Include: []string{"stat"}}, 2, 3),
			Want:  []string{util, readme, guide},
		},
		{
			Name: "search/pattern by depth and globs",
			Built: &aql.Query{Criteria: aql.And{
				file, repos("libs-local"),
				&aql.FieldCriterion{Field: aql.FieldDepth, Op: aql.OpEq, Number: 3},
				field(aql.FieldPath, aql.OpMatch, "org/*"),
				field(aql.FieldName, aql.OpMatch, "*.jar"),
			}},
			Want: []string{jar},
		},
		{
			Name: "search/pattern at the root",
			Built: &aql.Query{Criteria: aql.And{
				file, repos("libs-local"),
				&aql.FieldCriterion{Field: aql.FieldDepth, Op: aql.OpEq, Number: 1},
				field(aql.FieldPath, aql.OpMatch, "."),
				field(aql.FieldName, aql.OpMatch, "*.md"),
			}},
			Want: []string{readme},
		},
	}
}
