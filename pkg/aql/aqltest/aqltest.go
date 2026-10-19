// Package aqltest holds the AQL fixtures every search backend must agree
// on (decision 0012): seed items chosen to trip the dialect traps (a GLOB
// character class, LIKE wildcards in names, a multi-byte character, a
// missing time, a remote cache, an item without statistics beside one whose
// statistics are zero), the repositories they live in, the query shapes the
// JFrog CLI generates, and the rows each query returns, in order.
//
// BuildSeeds add build runs and promotions that relate to the items by
// checksum, for the relation-path cases and the builds and
// build.promotions cases (BuildCases).
//
// The pkg/aql tests run the cases through aql.SelectItems and
// aql.SelectBuilds over Corpus; the store contract suite runs them through
// every ItemStore.Search and BuildStore.Search. Both resolve each query
// against Repositories first, as the server does.
package aqltest

import (
	"embed"
	"path"
	"strings"
	"time"

	"github.com/weaveplatform/artifactweave/pkg/aql"
	"github.com/weaveplatform/artifactweave/pkg/types"
)

// The CLI fixtures are the AQL the pinned JFrog CLI sends, captured from
// its requests by the acceptance scenario "the query shapes the fixtures
// capture" (test/acceptance/features/round_trip.feature) over a tree laid
// out like Seeds. Refresh them when the pinned jf changes:
//
//	make capture-aql
//
// copies each captured query over the fixture of the same command.
//
//go:embed testdata/*.aql
var cliFS embed.FS

// Base is the seeds' clock: item n was created n minutes after it.
var Base = time.Date(2026, 1, 1, 0, 0, 0, 0, time.UTC)

// Now is the clock the cases run at, for $before and $last.
var Now = Base.Add(time.Hour)

// Seed is one item to store, with its properties and its statistics; nil
// Stats stores no statistics record.
type Seed struct {
	Item       types.Item
	Properties types.Properties
	Stats      *types.ItemStats
}

// Seeds returns the items in insertion order; stores give them ascending
// IDs, which break ties in every ordering.
func Seeds() []Seed {
	var out []Seed
	add := func(repoKey, full string, folder bool, size int64, by string, props types.Properties) {
		n := len(out) + 1
		parent, name := path.Split(full)
		parent = strings.TrimSuffix(parent, "/")
		if parent == "" {
			parent = types.RootPath
		}
		stored, cached := types.ResolveRepoKey(repoKey)
		it := types.Item{
			RepoKey: stored, Cached: cached, Path: parent, Name: name, Folder: folder,
			Size: size, CreatedBy: by,
			Created: Base.Add(time.Duration(n) * time.Minute),
			// Modified runs backwards so sorting by it differs from ID order.
			Modified: Base.Add(time.Duration(20-n) * time.Minute),
			Updated:  Base.Add(time.Duration(n) * time.Minute),
		}
		if !folder {
			it.Actual = types.Checksums{
				SHA1: strings.Repeat(string(rune('a'+n)), 40),
				MD5:  strings.Repeat(string(rune('a'+n)), 32),
			}
		}
		out = append(out, Seed{Item: it, Properties: props})
	}
	add("libs-local", "org", true, 0, "", nil)
	add("libs-local", "org/acme", true, 0, "", nil)
	add("libs-local", "org/acme/lib", true, 0, "", nil)
	add("libs-local", "org/acme/app-1.0.jar", false, 100, "alice",
		types.Properties{"build.name": {"app"}, "release": {"1.0"}})
	add("libs-local", "org/acme/app-1.0.pom", false, 10, "bob",
		types.Properties{"build.name": {"app"}})
	add("libs-local", "org/acme/lib/util-2.0.jar", false, 300, "alice",
		types.Properties{"release": {"2.0"}, "owner": {"team-a"}})
	add("libs-local", "readme.md", false, 5, "carol", nil)
	add("generic-local", "docs", true, 0, "", nil)
	add("generic-local", "docs/guide [1].txt", false, 7, "", nil)
	// The two license items are $msp's documented example (research 22
	// §1.1): a_b.txt is item B, axb.txt item A.
	add("generic-local", "docs/a_b.txt", false, 8, "",
		types.Properties{"license": {"LGPL-2.1", "LGPL-2.2"}})
	add("generic-local", "docs/axb.txt", false, 8, "",
		types.Properties{"license": {"AGPL-V3"}})
	add("generic-local", "docs/50%.txt", false, 9, "", nil)
	add("generic-local", "docs/50abc.txt", false, 9, "", nil)
	add("generic-local", "docs/naïve.txt", false, 11, "", nil)
	add("remote-cache", "org/acme/app-1.0.jar", false, 100, "", nil)
	// generic-local's root has a record, as a property set on the root
	// leaves one (#444): a query finds it in place of the record-less root
	// the other repositories answer with (aql.RootHit). Stores create it
	// with EnsureRoot.
	root := types.RootItem("generic-local", false)
	root.Created, root.Updated = Base.Add(16*time.Minute), Base.Add(16*time.Minute)
	root.Modified = Base.Add(4 * time.Minute)
	out = append(out, Seed{Item: root, Properties: types.Properties{"team": {"docs"}}})
	// readme.md has no modification time, which SQL stores as NULL.
	out[6].Item.Modified = time.Time{}
	// util-2.0.jar has no statistics record and app-1.0.pom an empty one:
	// a never-downloaded item compares as NULL in the first case and as 0
	// or "" in the second.
	out[3].Stats = &types.ItemStats{
		DownloadCount: 5, LastDownloaded: Base.Add(30 * time.Minute), LastDownloadedBy: "alice",
	}
	out[4].Stats = &types.ItemStats{}
	out[6].Stats = &types.ItemStats{
		RemoteDownloadCount: 2, RemoteLastDownloaded: Base.Add(40 * time.Minute),
		RemoteLastDownloadedBy: "bob", RemoteOrigin: "https://up", RemotePath: "readme.md",
	}
	out[14].Stats = &types.ItemStats{
		DownloadCount: 1, LastDownloaded: Base.Add(50 * time.Minute), LastDownloadedBy: "carol",
	}
	return out
}

// Hits returns the seeds as stored: IDs from 1 and the depth the stores
// derive, for running the cases through aql.Select.
func Hits() []aql.Hit {
	seeds := Seeds()
	out := make([]aql.Hit, len(seeds))
	for n, s := range seeds {
		it := s.Item
		it.ID = int64(n + 1)
		_, _, it.Depth, _ = types.SplitItemPath(it.FullPath())
		out[n] = aql.Hit{Item: it, Properties: s.Properties, Stats: s.Stats}
	}
	return out
}

// Repositories configures the seeds' repositories, and virtual ones over
// them: libs-virtual nests the remote, all-virtual nests libs-virtual and a
// member nothing configures, and loop-virtual names itself.
func Repositories() aql.Repositories {
	return aql.Repositories{
		"libs-local":    {Class: aql.RepoLocal},
		"generic-local": {Class: aql.RepoLocal},
		"remote":        {Class: aql.RepoRemote},
		"libs-virtual":  {Class: aql.RepoVirtual, Members: []string{"libs-local", "remote"}},
		"all-virtual": {
			Class: aql.RepoVirtual, Members: []string{"libs-virtual", "generic-local", "missing"},
		},
		"loop-virtual": {
			Class:   aql.RepoVirtual,
			Members: []string{"loop-virtual", "generic-local"},
		},
	}
}

// rootCases are the queries that meet repository roots (#445): every
// repository that stores items has one, a folder with path and name ".",
// whether or not it has a record. A root without a record sorts before the
// stored items its sort keys tie with, the roots by key, and the window
// counts it like any row.
func rootCases() []Case {
	var out []Case
	q := func(name, query string, want ...string) {
		out = append(out, Case{Name: "root/" + name, Query: query, Want: want})
	}
	q("path dot", `items.find({"type":"any","path":"."})`,
		libsRoot, remoteRoot, "libs-local/org", readme, "generic-local/docs", genericRoot)
	q("roots alone", `items.find({"type":"folder","name":"."})`,
		libsRoot, remoteRoot, genericRoot)
	q("files leave roots out", `items.find({"name":"."})`)
	q("a remote's root is its cache's", `items.find({"repo":"remote","type":"folder"})`, remoteRoot)
	q("a virtual key reaches its members' roots",
		`items.find({"repo":"all-virtual","type":"folder","depth":0})`,
		libsRoot, remoteRoot, genericRoot)
	q("a recorded root keeps its properties", `items.find({"type":"folder","@team":"docs"})`,
		genericRoot)
	q("criteria a root fails", `items.find({"type":"folder","name":".","size":{"$gt":0}})`)
	q("roots in a sorted window",
		`items.find({"type":"folder"}).sort({"$asc":["name"]}).offset(1).limit(3)`,
		remoteRoot, genericRoot, "libs-local/org/acme")
	q("a root without a record has no times",
		`items.find({"type":"folder"}).sort({"$asc":["modified"]}).limit(3)`,
		libsRoot, remoteRoot, genericRoot)
	q("not dot excludes roots", `items.find({"type":"any","repo":"libs-local","path":{"$ne":"."}})`,
		"libs-local/org/acme", "libs-local/org/acme/lib", jar, pom, util)
	out = append(out, Case{
		Name: "root/cap counts roots", Query: `items.find({"type":"folder"})`,
		Options: aql.Options{Max: 2}, Want: []string{libsRoot, remoteRoot},
	})
	return out
}

// Case is a query and the rows it returns, as "<reported repo>/<path>".
type Case struct {
	Name  string
	Query string
	// Built is a query built in code, run instead of Query: the GET
	// searches' queries, which AQL text cannot always say.
	Built   *aql.Query
	Options aql.Options
	Want    []string
}

// Parse returns the case's query, Built or Query parsed, resolved against
// Repositories.
func (c Case) Parse() (*aql.Query, error) {
	q := c.Built
	if q == nil {
		var err error
		if q, err = aql.Parse(c.Query); err != nil {
			return nil, err
		}
	}
	q.Resolve(Repositories())
	return q, nil
}

// Paths renders hits the way Case.Want lists them.
func Paths(hits []aql.Hit) []string {
	out := make([]string, len(hits))
	for n, h := range hits {
		out[n] = h.Item.ReportedRepoKey() + "/" + h.Item.FullPath()
	}
	return out
}

const (
	jar    = "libs-local/org/acme/app-1.0.jar"
	pom    = "libs-local/org/acme/app-1.0.pom"
	util   = "libs-local/org/acme/lib/util-2.0.jar"
	readme = "libs-local/readme.md"
	guide  = "generic-local/docs/guide [1].txt"
	aUnder = "generic-local/docs/a_b.txt"
	axb    = "generic-local/docs/axb.txt"
	pct    = "generic-local/docs/50%.txt"
	abc    = "generic-local/docs/50abc.txt"
	naive  = "generic-local/docs/naïve.txt"
	cached = "remote-cache/org/acme/app-1.0.jar"
	// The repository roots: libs-local's and the remote cache's have no
	// record, generic-local's has one.
	libsRoot    = "libs-local/"
	remoteRoot  = "remote-cache/"
	genericRoot = "generic-local/"
)

// cliWant lists the rows of each CLI fixture, by file name. The
// *_properties fixtures are the follow-up query the CLI sends to fetch the
// properties of what a search found.
var cliWant = map[string][]string{
	"search_recursive":         {jar, util},
	"search_props_sort":        {pom, jar},
	"search_props_properties":  {jar, pom},
	"search_root_offset_limit": {pom, readme},
	"search_root_properties":   {jar, pom, util},
	// jf rt s --offset 1 keeps "property" in the include. The offset skips
	// the first item, the jar, although it has two properties: counting
	// item-property rows, as Artifactory does, would skip only one of them.
	"search_offset":       {pom, util, readme},
	"download_exclusions": {jar, util},
	// The CLI names a remote repository by its own key, never "-cache"; the
	// key matches the remote's cache (decision 0012), reported as "-cache".
	"download_remote":      {cached},
	"delete_any":           {"libs-local/org/acme", "libs-local/org/acme/lib", jar, pom, util},
	"copy_move_file":       {jar},
	"set_props_with_props": {jar, util},
	// jf rt s and jf rt dl with --build and a pattern send one query each:
	// app/1's artifacts under the pattern (BuildSeeds), matched by name and
	// number from either of its runs.
	"search_build":   {jar, pom},
	"download_build": {jar, pom},
}

// Cases returns the CLI fixtures followed by the cases for each operator
// and dialect trap.
func Cases() []Case {
	var out []Case
	entries, _ := cliFS.ReadDir("testdata")
	for _, e := range entries {
		b, _ := cliFS.ReadFile("testdata/" + e.Name())
		name := strings.TrimSuffix(e.Name(), ".aql")
		out = append(out, Case{Name: "cli/" + name, Query: string(b), Want: cliWant[name]})
	}
	q := func(name, query string, want ...string) {
		out = append(out, Case{Name: name, Query: query, Want: want})
	}
	q("glob character class is literal",
		`items.find({"repo":"generic-local","name":{"$match":"guide [1]*"}})`, guide)
	q("like underscore is literal", `items.find({"name":{"$match":"a_b*"}})`, aUnder)
	q("like percent is literal", `items.find({"name":{"$match":"50%*"}})`, pct)
	q("question mark is one character", `items.find({"name":{"$match":"na?ve.txt"}})`, naive)
	q("match is case-sensitive", `items.find({"name":{"$match":"*.JAR"}})`)
	q("nmatch", `items.find({"repo":"generic-local","name":{"$nmatch":"a*"}})`,
		guide, pct, abc, naive)
	q("any property key", `items.find({"@*":"team-a"})`, util)
	q("property ne holds without the key",
		`items.find({"repo":"libs-local","@build.name":{"$ne":"app"}})`, util, readme)
	q("property nmatch", `items.find({"repo":"libs-local","@release":{"$nmatch":"1.*"}})`,
		pom, util, readme)
	q("property gt", `items.find({"@release":{"$gt":"1.5"}})`, util)
	q("size range", `items.find({"size":{"$gte":100,"$lt":300}})`, jar, cached)
	q("depth", `items.find({"depth":3})`, jar, pom, cached)
	q("folders", `items.find({"type":"folder","repo":"libs-local"})`,
		libsRoot, "libs-local/org", "libs-local/org/acme", "libs-local/org/acme/lib")
	q("repo ne", `items.find({"repo":{"$ne":"libs-local"}})`,
		guide, aUnder, axb, pct, abc, naive, cached)
	q("repo match sees the cache", `items.find({"repo":{"$match":"*-cache"}})`, cached)
	q("repo ordering", `items.find({"repo":{"$gt":"libs"}})`, jar, pom, util, readme, cached)
	// Repository keys resolve against Repositories (decision 0012).
	q("remote key is its cache", `items.find({"repo":"remote"})`, cached)
	q("cache alias", `items.find({"repo":"remote-cache"})`, cached)
	q("virtual key is its members", `items.find({"repo":"libs-virtual"})`,
		jar, pom, util, readme, cached)
	q("nested virtual", `items.find({"repo":"all-virtual"})`,
		jar, pom, util, readme, guide, aUnder, axb, pct, abc, naive, cached)
	q("virtual cycle", `items.find({"repo":"loop-virtual"})`,
		guide, aUnder, axb, pct, abc, naive)
	q("remote ne", `items.find({"repo":{"$ne":"remote"}})`,
		jar, pom, util, readme, guide, aUnder, axb, pct, abc, naive)
	q("virtual ne", `items.find({"repo":{"$ne":"libs-virtual"}})`,
		guide, aUnder, axb, pct, abc, naive)
	q("match a remote key", `items.find({"repo":{"$match":"remot?"}})`, cached)
	q("match a virtual key", `items.find({"repo":{"$match":"libs-v*"}})`,
		jar, pom, util, readme, cached)
	q("nmatch a remote key", `items.find({"repo":{"$nmatch":"remot?"}})`,
		jar, pom, util, readme, guide, aUnder, axb, pct, abc, naive)
	q("nmatch a virtual key", `items.find({"repo":{"$nmatch":"libs-*"}})`,
		guide, aUnder, axb, pct, abc, naive)
	q("remote key with other criteria",
		`items.find({"$or":[{"$and":[{"repo":"remote","name":"app-1.0.jar"}]},`+
			`{"$and":[{"repo":"libs-local","name":"readme.md"}]}]})`,
		readme, cached)
	q("created after a partial time", `items.find({"created":{"$gt":"2026-01-01T00:12"}})`,
		abc, naive, cached)
	q("created before an offset time",
		`items.find({"created":{"$lt":"2026-01-01T00:05:00.000+00:00"}})`, jar)
	q("missing time never compares",
		`items.find({"repo":"libs-local","modified":{"$ne":"2000"}})`, jar, pom, util)
	q("last", `items.find({"created":{"$last":"50minutes"}})`, axb, pct, abc, naive, cached)
	q("before", `items.find({"created":{"$before":"55minutes"}})`, jar)
	q("missing time sorts first ascending",
		`items.find({"repo":"libs-local"}).sort({"$asc":["modified"]})`, readme, util, pom, jar)
	q("missing time sorts last descending",
		`items.find({"repo":"libs-local"}).sort({"$desc":["modified"]})`, jar, pom, util, readme)
	q("sort on two fields, byte order",
		`items.find({"repo":"generic-local"}).sort({"$desc":["size","name"]})`,
		naive, abc, pct, axb, aUnder, guide)
	q("sort by reported repo", `items.find({"name":"app-1.0.jar"}).sort({"$desc":["repo"]})`,
		cached, jar)
	q("checksum", `items.find({"actual_sha1":"`+strings.Repeat("e", 40)+`"})`, jar)
	q("text ordering", `items.find({"created_by":{"$gte":"bob"}})`, pom, readme)
	q("limit zero", `items.find().limit(0)`)
	q("no criteria means files", `items.find()`,
		jar, pom, util, readme, guide, aUnder, axb, pct, abc, naive, cached)
	q("offset without limit", `items.find({"repo":"generic-local"}).offset(4)`, abc, naive)
	q("or joins the members of its objects",
		`items.find({"repo":"libs-local","$or":[{"name":"readme.md","size":300}]})`, util, readme)
	q("or of nothing", `items.find({"$or":[]})`)
	q("and of nothing", `items.find({"$and":[],"repo":"remote-cache"})`, cached)
	q("property include keeps the defaults",
		`items.find({"repo":"libs-local"}).include("property")`, jar, pom, util, readme)
	q("key include filters items", `items.find().include("repo","path","name","@release")`,
		jar, util)
	// "@" and "@*" include every property, as "property.*" does, and filter
	// nothing (#506). "@" beside a sort is aql-query-output's "Sorting in
	// AQL" example.
	q("every-key include", `items.find({"repo":"libs-local"}).include("@")`,
		jar, pom, util, readme)
	q("every-key include with a star",
		`items.find({"repo":"libs-local"}).include("repo","path","name","@*")`,
		jar, pom, util, readme)
	q("every-key include beside a sort",
		`items.find({"repo":"libs-local","name":{"$match":"*.jar"}}).include("@")`+
			`.sort({"$desc":["name"]})`,
		util, jar)
	q("every-key include beside a key include", `items.find().include("@*","@owner")`, util)
	// Sort, offset and limit beside a property or statistic include apply to
	// the items (decision 0012). The two jars have two properties each, held
	// inside one row each, so they neither shift the offset nor use up the
	// limit, and the sort orders the items.
	q("offset beside a property include counts items",
		`items.find({"repo":"libs-local"}).include("property").offset(1)`, pom, util, readme)
	q("limit beside a property include counts items",
		`items.find({"repo":"libs-local"}).include("property").limit(2)`, jar, pom)
	q("sort beside a property include orders items",
		`items.find({"repo":"libs-local"}).include("name","created","property")`+
			`.sort({"$desc":["created"]})`,
		readme, util, pom, jar)
	q("window beside a key include",
		`items.find().include("repo","path","name","@release").sort({"$desc":["name"]}).limit(1)`,
		util)
	q("window beside a stat include",
		`items.find({"repo":"libs-local"}).include("stat")`+
			`.sort({"$asc":["size"]}).offset(1).limit(2)`,
		pom, jar)
	q("two key includes need both keys", `items.find().include("@release","@owner")`, util)
	// $msp, research 22 §1.1: without it each criterion may be met by a
	// different value of the key, with it by one value.
	q("criteria without msp are independent",
		`items.find({"repo":"generic-local","@license":{"$match":"*GPL*","$nmatch":"LGPL-2.1*"}})`,
		axb)
	q("msp needs one property to meet every criterion",
		`items.find({"repo":"generic-local","$msp":[{"@license":{"$match":"*GPL*"}},`+
			`{"@license":{"$nmatch":"LGPL-2.1*"}}]})`,
		aUnder, axb)
	q("property key and value are independent",
		`items.find({"property.key":"release","property.value":"team-a"})`, util)
	q("msp ties a key to its value",
		`items.find({"$msp":[{"property.key":"release","property.value":"team-a"}]})`)
	q("msp with an ordering operator",
		`items.find({"$msp":[{"property.key":"release","property.value":{"$gt":"1.5"}}]})`, util)
	q("msp negatives test the one property",
		`items.find({"$msp":[{"@*":{"$nmatch":"*GPL*"}},{"property.key":{"$ne":"build.name"}}]})`,
		jar, util)
	q("msp value nmatch", `items.find({"$msp":[{"property.value":{"$nmatch":"*.*"}}]})`,
		jar, pom, util, axb)
	q("msp negative of any is nothing", `items.find({"$msp":[{"@license":{"$ne":"*"}}]})`)
	q("property key ne holds without the key",
		`items.find({"repo":"libs-local","property.key":{"$ne":"release"}})`, pom, readme)
	q("property key any", `items.find({"repo":"libs-local","property.key":"*"})`,
		jar, pom, util)
	q("property value match", `items.find({"property.value":{"$match":"LGPL*"}})`, aUnder)
	q("property key ordering", `items.find({"property.key":{"$lt":"c"}})`, jar, pom)
	q("stat include", `items.find({"repo":"libs-local"}).include("repo","path","name","stat")`,
		jar, pom, util, readme)
	q("stat field include", `items.find({"name":"app-1.0.jar"}).include("stat.downloads")`,
		jar, cached)
	q("stat number as a string", `items.find({"stat.downloads":{"$gt":"0"}})`, jar, cached)
	q("stat null is no statistics", `items.find({"repo":"libs-local","stat.downloads":null})`,
		util)
	q("stat null time", `items.find({"repo":"libs-local","stat.downloaded":{"$eq":null}})`,
		pom, util, readme)
	q("stat ne null", `items.find({"stat.downloads":{"$ne":null}})`, jar, pom, readme, cached)
	q("stat zero is not null", `items.find({"stat.downloads":0})`, pom, readme)
	q("stat text ne skips items without statistics",
		`items.find({"stat.downloaded_by":{"$ne":"alice"}})`, pom, readme, cached)
	q("stat text nmatch skips items without statistics",
		`items.find({"repo":"libs-local","stat.remote_path":{"$nmatch":"*.md"}})`, jar, pom)
	q("stat text match", `items.find({"stat.remote_origin":{"$match":"https://*"}})`, readme)
	q("stat text ordering", `items.find({"stat.downloaded_by":{"$gt":"b"}})`, cached)
	q("stat remote count", `items.find({"stat.remote_downloads":{"$lte":1}})`, jar, pom, cached)
	q("stat time", `items.find({"stat.downloaded":{"$gte":"2026-01-01T00:30"}})`, jar, cached)
	q("stat last", `items.find({"stat.downloaded":{"$last":"20minutes"}})`, cached)
	q("stat before", `items.find({"stat.remote_downloaded":{"$before":"10minutes"}})`, readme)
	// The actions select items as find does (research 22 §1.5); the server
	// then deletes or updates the rows the store returns.
	q("delete selects files", `items.delete({"repo":"libs-local"}).dryRun("false")`,
		jar, pom, util, readme)
	q("delete of folders", `items.delete({"repo":"libs-local","type":"folder"})`,
		libsRoot, "libs-local/org", "libs-local/org/acme", "libs-local/org/acme/lib")
	q("delete through a remote key", `items.delete({"repo":"remote"}).dryRun(true)`, cached)
	q("update needs one of the keys",
		`properties.update({"item.repo":"libs-local"}).keys("release","owner").newValue("x")`,
		jar, util)
	q("update with item criteria under or",
		`properties.update({"$or":[{"item.name":"app-1.0.pom"},{"item.path":"org/acme/lib"}]})`+
			`.keys("build.name","owner").newValue("v").dryRun("false")`,
		pom, util)
	q("update through a virtual key",
		`properties.update({"item.repo":"all-virtual","item.name":{"$match":"*.txt"}})`+
			`.keys("license").newValue("MIT")`,
		aUnder, axb)
	q("update without criteria", `properties.update().keys("owner").newValue("v")`, util)
	out = append(out, Case{
		Name:    "cap applies to an action",
		Query:   `items.delete({"repo":"generic-local"})`,
		Options: aql.Options{Max: 2}, Want: []string{guide, aUnder},
	})
	out = append(out, Case{
		Name: "cap applies without a limit", Query: `items.find({"repo":"generic-local"})`,
		Options: aql.Options{Max: 2}, Want: []string{guide, aUnder},
	})
	out = append(out, Case{
		Name:    "cap trims an own limit above it",
		Query:   `items.find({"repo":"generic-local"}).limit(3)`,
		Options: aql.Options{Max: 2}, Want: []string{guide, aUnder},
	})
	out = append(out, Case{
		Name: "own limit below the cap", Query: `items.find({"repo":"generic-local"}).limit(1)`,
		Options: aql.Options{Max: 2}, Want: []string{guide},
	})
	out = append(out, rootCases()...)
	out = append(out, searchCases()...)
	out = append(out, pathCases()...)
	for n := range out {
		out[n].Options.Now = Now
	}
	return out
}
