package aqltest

import (
	"fmt"
	"strings"
	"time"

	"github.com/weaveplatform/artifactweave/pkg/aql"
	"github.com/weaveplatform/artifactweave/pkg/types"
)

// BuildSeed is one build run to store after Seeds, with its promotions,
// whose BuildID the store's ID for the run replaces.
type BuildSeed struct {
	Build      types.Build
	Promotions []types.BuildPromotion
}

// sha1Of and md5Of are the checksums Seeds gives item n (1 is the first).
func sha1Of(n int) string { return strings.Repeat(string(rune('a'+n)), 40) }
func md5Of(n int) string  { return strings.Repeat(string(rune('a'+n)), 32) }

func file(name, typ string, item int) types.BuildFile {
	return types.BuildFile{
		Name: name, Type: typ, Checksums: types.Checksums{SHA1: sha1Of(item), MD5: md5Of(item)},
	}
}

func dependency(id, typ string, item int, scopes ...string) types.BuildFile {
	f := file("", typ, item)
	f.ID, f.Scopes = id, scopes
	return f
}

// BuildSeeds returns the runs in insertion order, so stores give them IDs
// from 1 and their promotions IDs from 1 in the order listed. They relate
// to Seeds by checksum and are chosen to trip the join traps: the jar is
// an artifact of app/1 and app/2 and a dependency of tool/1, so a name and
// a number met by different runs must not match; naïve.txt is an artifact
// of app/2 and tool/1; app/1 is published twice; a dependency names a
// checksum no item has; and one module has no files.
func BuildSeeds() []BuildSeed {
	at := func(minutes int) time.Time { return Base.Add(time.Duration(minutes) * time.Minute) }
	return []BuildSeed{
		{
			Build: types.Build{
				Name: "app", Number: "1", Started: at(10), Created: at(11), CreatedBy: "ci",
				URL: "https://ci/app/1", Properties: types.Properties{"os": {"linux"}},
				Modules: []types.BuildModule{{
					ID: "org.acme:app:1.0", Type: "maven",
					Properties: types.Properties{"java": {"17"}},
					Artifacts: []types.BuildFile{
						file("app-1.0.jar", "jar", 4), file("app-1.0.pom", "pom", 5),
					},
					Dependencies: []types.BuildFile{
						dependency("org.acme:util:2.0", "jar", 6, "compile"),
					},
				}},
			},
			Promotions: []types.BuildPromotion{
				{
					Created: at(50), CreatedBy: "admin", Status: "staged", Comment: "rc",
					CIUser: "jenkins", SourceRepo: "libs-local", Repo: "libs-stage",
				},
				{
					Created: at(55), CreatedBy: "admin", Status: "released", CIUser: "jenkins",
					SourceRepo: "libs-stage", Repo: "libs-release",
				},
			},
		},
		{
			Build: types.Build{
				Name: "app", Number: "2", Started: at(20), Created: at(21), CreatedBy: "ci",
				Modified: at(25), ModifiedBy: "admin", URL: "https://ci/app/2",
				Properties: types.Properties{"os": {"windows"}},
				Modules: []types.BuildModule{{
					ID: "org.acme:app:2.0", Type: "maven",
					Artifacts: []types.BuildFile{
						file("app-1.0.jar", "jar", 4), file("naïve.txt", "txt", 14),
					},
					Dependencies: []types.BuildFile{
						dependency("docs:guide:1", "txt", 9, "test", "runtime"),
						{
							ID: "gone:lib:1", Type: "jar",
							Checksums: types.Checksums{SHA1: strings.Repeat("z", 40)},
						},
					},
				}},
			},
		},
		{
			Build: types.Build{
				ProjectKey: "proj", Name: "tool", Number: "1", Started: at(30), Created: at(31),
				CreatedBy: "bob",
				Modules: []types.BuildModule{{
					ID: "tool", Type: "generic", Properties: types.Properties{"license": {"MIT"}},
					Artifacts: []types.BuildFile{
						file("a_b.txt", "txt", 10), file("axb.txt", "txt", 11),
						file("naïve.txt", "txt", 14),
					},
					Dependencies: []types.BuildFile{dependency("org.acme:app:1.0", "jar", 4)},
				}},
			},
			Promotions: []types.BuildPromotion{
				{CreatedBy: "bob", Status: "staged", SourceRepo: "tools-dev", Repo: "tools-stage"},
			},
		},
		{
			Build: types.Build{
				Name: "app", Number: "1", Started: at(40), Created: at(41), CreatedBy: "ci",
				URL: "https://ci/app/1",
				Modules: []types.BuildModule{
					{
						ID: "org.acme:app:1.0", Type: "maven",
						Artifacts: []types.BuildFile{file("app-1.0.jar", "jar", 15)},
					},
					{ID: "empty", Type: "generic"},
				},
			},
		},
	}
}

// Corpus returns Hits and the build seeds as stored, with IDs from 1, for
// running the cases through aql.SelectItems and aql.SelectBuilds.
func Corpus() *aql.Corpus {
	c := &aql.Corpus{Items: Hits()}
	for n, s := range BuildSeeds() {
		b := s.Build
		b.ID = int64(n + 1)
		c.Builds = append(c.Builds, b)
		for _, p := range s.Promotions {
			p.ID, p.BuildID = int64(len(c.Promotions)+1), b.ID
			c.Promotions = append(c.Promotions, p)
		}
	}
	return c
}

// BuildRows renders builds hits as "<name>/<number>#<run ID>", and
// promotions hits with " <status>" after their run.
func BuildRows(hits []aql.BuildHit) []string {
	out := make([]string, len(hits))
	for n, h := range hits {
		out[n] = fmt.Sprintf("%s/%s#%d", h.Build.Name, h.Build.Number, h.Build.ID)
		if h.Promotion != nil {
			out[n] += " " + h.Promotion.Status
		}
	}
	return out
}

// pathCases are items queries over BuildSeeds through relation paths. The
// "cli build" cases are the other shapes jfrog-client-go sends for --build
// (artifactory/services/utils/aqlquerybuilder.go: buildClauseItems,
// createAqlQueryForBuild and createPropsQuery): without a pattern, for
// dependencies, for checksums only, by properties and for several builds.
// They are written by hand because the capture scenario runs jf rt s and
// jf rt dl with a pattern only; those two are the search_build and
// download_build fixtures.
func pathCases() []Case {
	var out []Case
	q := func(name, query string, want ...string) {
		out = append(out, Case{Name: name, Query: query, Want: want})
	}
	app1 := `{"$and":[{"artifact.module.build.name":"app","artifact.module.build.number":"1"}]}`
	tool1 := `{"$and":[{"artifact.module.build.name":"tool","artifact.module.build.number":"1"}]}`
	include := `.include("name","repo","path","actual_md5","actual_sha1","sha256","size",` +
		`"type","modified","created","property")`
	q("cli build artifacts", `items.find({"$or":[`+app1+`]})`+include, jar, pom, cached)
	q("cli build dependencies",
		`items.find({"$or":[{"$and":[{"dependency.module.build.name":"app",`+
			`"dependency.module.build.number":"2"}]}]})`+include,
		guide)
	q("cli build checksums",
		`items.find({"$or":[`+app1+`]}).include("name","repo","path","actual_sha1")`,
		jar, pom, cached)
	q("cli build properties",
		`items.find({"$and":[{"$or":[`+app1+`]},{"$or":[{"@build.name":"app"}]}]})`+
			`.include("name","repo","path","actual_md5","actual_sha1","sha256","size","property")`,
		jar, pom)
	q("cli aggregated builds", `items.find({"$or":[`+app1+`,`+tool1+`]})`,
		jar, pom, aUnder, axb, naive, cached)
	q("name and number from one run",
		`items.find({"artifact.module.build.name":"tool","artifact.module.build.number":"2"})`)
	q("name and number of another run",
		`items.find({"artifact.module.build.name":"app","artifact.module.build.number":"2"})`,
		jar, naive)
	q("artifact name", `items.find({"artifact.name":"app-1.0.pom"})`, pom)
	q("artifact ne needs an artifact", `items.find({"artifact.type":{"$ne":"txt"}})`,
		jar, pom, cached)
	q("module name", `items.find({"artifact.module.name":"tool"})`, aUnder, axb, naive)
	q("build property", `items.find({"artifact.module.build.@os":"linux"})`, jar, pom)
	q("build property ne needs a build",
		`items.find({"repo":"generic-local","artifact.module.build.@os":{"$ne":"linux"}})`,
		aUnder, axb, naive)
	q("module property", `items.find({"dependency.module.@license":"MIT"})`, jar)
	q("dependency scope", `items.find({"dependency.scope":"compile"})`, util)
	q("dependency scopes join in order", `items.find({"dependency.scope":"test,runtime"})`,
		guide)
	q("dependency name is its id", `items.find({"dependency.name":"org.acme:util:2.0"})`, util)
	q("an item alternative keeps items without builds",
		`items.find({"$or":[{"name":"readme.md"},{"artifact.module.build.name":"tool"}]})`,
		readme, aUnder, axb, naive)
	q("artifact or dependency",
		`items.find({"$or":[{"artifact.module.build.name":"tool"},`+
			`{"dependency.module.build.name":"tool"}]})`,
		jar, aUnder, axb, naive)
	q("build ne needs a build", `items.find({"artifact.module.build.name":{"$ne":"app"}})`,
		aUnder, axb, naive)
	q("promotion status", `items.find({"artifact.module.build.promotion.status":"released"})`,
		jar, pom)
	q("build started",
		`items.find({"artifact.module.build.started":{"$gte":"2026-01-01T00:30"}})`,
		aUnder, axb, naive, cached)
	q("build missing time never compares",
		`items.find({"artifact.module.build.modified":{"$ne":"2000"}})`, jar, naive)
	q("build created last",
		`items.find({"artifact.module.build.created":{"$last":"30minutes"}})`,
		aUnder, axb, naive, cached)
	q("dependency item", `items.find({"dependency.item.name":"app-1.0.jar"})`, jar)
	q("build msp",
		`items.find({"$msp":[{"artifact.module.build.@os":"linux"},`+
			`{"artifact.module.build.property.key":"os"}]})`,
		jar, pom)
	q("build match and ordering",
		`items.find({"artifact.module.build.name":{"$match":"a*"},`+
			`"artifact.module.build.number":{"$gt":"1"}})`,
		jar, naive)
	q("build repo", `items.find({"artifact.module.build.repo":"proj-build-info"})`,
		aUnder, axb, naive)
	q("related item repo is the reported key",
		`items.find({"artifact.module.artifact.item.repo":"remote-cache"})`, cached)
	q("related item property",
		`items.find({"dependency.module.artifact.item.@license":{"$match":"*GPL*"}})`, jar)
	return out
}

// BuildCases returns the builds and build.promotions queries over
// BuildSeeds, with the rows each returns as BuildRows renders them.
func BuildCases() []Case {
	const (
		app1     = "app/1#1"
		app2     = "app/2#2"
		tool     = "tool/1#3"
		rerun    = "app/1#4"
		staged   = "app/1#1 staged"
		released = "app/1#1 released"
		tooled   = "tool/1#3 staged"
	)
	var out []Case
	q := func(name, query string, want ...string) {
		out = append(out, Case{Name: name, Query: query, Want: want})
	}
	q("all builds", `builds.find()`, app1, app2, tool, rerun)
	q("build name", `builds.find({"name":"app"})`, app1, app2, rerun)
	q("build number sorted by start",
		`builds.find({"number":"1"}).sort({"$desc":["started"]})`, rerun, tool, app1)
	q("build repo of a project", `builds.find({"repo":"proj-build-info"})`, tool)
	q("build repo and number",
		`builds.find({"repo":"artifactory-build-info","number":{"$gt":"1"}})`, app2)
	q("build property", `builds.find({"@os":"linux"})`, app1)
	q("build property ne holds without the key", `builds.find({"@os":{"$ne":"linux"}})`,
		app2, tool, rerun)
	q("produced an item", `builds.find({"module.artifact.item.name":"app-1.0.jar"})`,
		app1, app2, rerun)
	q("produced an item in a repository",
		`builds.find({"module.artifact.item.repo":"libs-local"})`, app1, app2)
	q("consumed an item", `builds.find({"module.dependency.item.name":"guide [1].txt"})`, app2)
	q("produced an item with a property",
		`builds.find({"module.artifact.item.@build.name":"app"})`, app1, app2)
	// research 22 §1.1's example: builds using a dependency that is not
	// Apache-licensed; a dependency no item has reaches the null item.
	q("dependency item property nmatch",
		`builds.find({"module.dependency.item.@license":{"$nmatch":"Apache-*"}})`,
		app1, app2, tool)
	q("module name", `builds.find({"module.name":{"$match":"org.acme:*"}})`, app1, app2, rerun)
	q("module without files", `builds.find({"module.name":"empty"})`, rerun)
	q("module property", `builds.find({"module.@java":"17"})`, app1)
	q("promotion status", `builds.find({"promotion.status":"staged"})`, app1, tool)
	q("created before", `builds.find({"created":{"$before":"25minutes"}})`, app1, app2, tool)
	q("started last", `builds.find({"started":{"$last":"25minutes"}})`, rerun)
	q("missing time never compares", `builds.find({"modified":{"$ne":"2000"}})`, app2)
	q("url match", `builds.find({"url":{"$match":"https://ci/*"}})`, app1, app2, rerun)
	q("text ordering", `builds.find({"created_by":{"$gt":"bob"}})`, app1, app2, rerun)
	q("sort on two fields", `builds.find().sort({"$asc":["name","number"]})`,
		app1, rerun, app2, tool)
	q("missing time sorts first ascending", `builds.find().sort({"$asc":["modified"]})`,
		app1, tool, rerun, app2)
	q("offset and limit", `builds.find().sort({"$desc":["created"]}).offset(1).limit(2)`,
		tool, app2)
	q("build msp", `builds.find({"$msp":[{"@os":"linux"},{"property.key":"os"}]})`, app1)
	q("a build alternative keeps builds without the path",
		`builds.find({"$or":[{"name":"tool"},{"module.dependency.name":"org.acme:util:2.0"}]})`,
		app1, tool)
	q("dependency scope", `builds.find({"module.dependency.scope":"test,runtime"})`, app2)
	q("artifact type", `builds.find({"module.artifact.type":"pom"})`, app1)
	q("artifact ne needs an artifact",
		`builds.find({"module.artifact.name":{"$ne":"app-1.0.jar"}})`, app1, app2, tool)
	q("include keeps the rows", `builds.find({"name":"tool"}).include("name","number","repo")`,
		tool)
	q("all promotions", `build.promotions.find()`, staged, released, tooled)
	q("promotion status of promotions", `build.promotions.find({"status":"staged"})`,
		staged, tooled)
	q("promoted build", `build.promotions.find({"build.name":"app"})`, staged, released)
	q("promotion created", `build.promotions.find({"created":{"$gt":"2026-01-01T00:52"}})`,
		released)
	q("promotion user and repo",
		`build.promotions.find({"user":"jenkins","repo":{"$match":"libs-*"}})`,
		staged, released)
	q("promotion missing time sorts last descending",
		`build.promotions.find().sort({"$desc":["created"]})`, released, staged, tooled)
	q("promotion of a build that produced an item",
		`build.promotions.find({"build.module.artifact.item.name":"a_b.txt"})`, tooled)
	q("promotion of a build with a property", `build.promotions.find({"build.@os":"linux"})`,
		staged, released)
	q("empty comment", `build.promotions.find({"comment":""})`, released, tooled)
	q("promotion build repo", `build.promotions.find({"build.repo":"proj-build-info"})`, tooled)
	q("promotion sort and limit",
		`build.promotions.find().sort({"$asc":["status"]}).limit(1)`, released)
	q("promotion and build criteria",
		`build.promotions.find({"created_by":"admin","build.started":{"$lt":"2026-01-01T00:15"}})`,
		staged, released)
	// One case per operator the cases above leave out, on each domain.
	q("started gte", `builds.find({"started":{"$gte":"2026-01-01T00:30"}})`, tool, rerun)
	q("number lt", `builds.find({"number":{"$lt":"2"}})`, app1, tool, rerun)
	q("created lte", `builds.find({"created":{"$lte":"2026-01-01T00:21"}})`, app1, app2)
	q("explicit eq", `builds.find({"created_by":{"$eq":"bob"}})`, tool)
	q("and of criteria", `builds.find({"$and":[{"name":"app"},{"number":"1"}]})`, app1, rerun)
	q("promotion status ne", `build.promotions.find({"status":{"$ne":"staged"}})`, released)
	q("promotion repo nmatch", `build.promotions.find({"repo":{"$nmatch":"libs-*"}})`, tooled)
	q("promotion created gte",
		`build.promotions.find({"created":{"$gte":"2026-01-01T00:55"}})`, released)
	q("promotion created lte",
		`build.promotions.find({"created":{"$lte":"2026-01-01T00:50"}})`, staged)
	q("promotion created last", `build.promotions.find({"created":{"$last":"8minutes"}})`,
		released)
	q("promotion created before", `build.promotions.find({"created":{"$before":"8minutes"}})`,
		staged)
	q("promotion explicit eq", `build.promotions.find({"created_by":{"$eq":"bob"}})`, tooled)
	q("promotion alternatives",
		`build.promotions.find({"$or":[{"status":"released"},{"build.name":"tool"}]})`,
		released, tooled)
	q("promotion msp",
		`build.promotions.find({"$msp":[{"build.@os":"linux"},{"build.property.key":"os"}]})`,
		staged, released)
	q("promotion include keeps the rows",
		`build.promotions.find({"status":"released"}).include("status","comment")`, released)
	out = append(out, Case{
		Name: "cap applies to builds", Query: `builds.find()`, Options: aql.Options{Max: 2},
		Want: []string{app1, app2},
	})
	for n := range out {
		out[n].Options.Now = Now
	}
	return out
}
