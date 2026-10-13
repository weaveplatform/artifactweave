// Package aqltest holds the AQL fixtures every search backend must agree
// on (decision 0012): seed items chosen to trip the dialect traps (a GLOB
// character class, LIKE wildcards in names, a multi-byte character, a
// missing time, a remote cache), the query shapes the JFrog CLI generates,
// and the rows each query returns, in order.
//
// The pkg/aql tests run the cases through aql.Select; the store contract
// suite runs them through every ItemStore.Search.
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

// Seed is one item to store, with its properties.
type Seed struct {
	Item       types.Item
	Properties types.Properties
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
	add("generic-local", "docs/a_b.txt", false, 8, "", nil)
	add("generic-local", "docs/axb.txt", false, 8, "", nil)
	add("generic-local", "docs/50%.txt", false, 9, "", nil)
	add("generic-local", "docs/50abc.txt", false, 9, "", nil)
	add("generic-local", "docs/naïve.txt", false, 11, "", nil)
	add("remote-cache", "org/acme/app-1.0.jar", false, 100, "", nil)
	// readme.md has no modification time, which SQL stores as NULL.
	out[6].Item.Modified = time.Time{}
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
		out[n] = aql.Hit{Item: it, Properties: s.Properties}
	}
	return out
}

// Case is a query and the rows it returns, as "<reported repo>/<path>".
type Case struct {
	Name    string
	Query   string
	Options aql.Options
	Want    []string
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
	"download_exclusions":      {jar, util},
	// The CLI names a remote repository by its own key, never "-cache", and
	// repo equality matches the stored key outside its cache, so a download
	// from a remote finds nothing here. Whether Artifactory's AQL counts the
	// cache under the remote's key is unverified (issue #51).
	"download_remote":      {},
	"delete_any":           {"libs-local/org/acme", "libs-local/org/acme/lib", jar, pom, util},
	"copy_move_file":       {jar},
	"set_props_with_props": {jar, util},
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
		"libs-local/org", "libs-local/org/acme", "libs-local/org/acme/lib")
	q("repo ne", `items.find({"repo":{"$ne":"libs-local"}})`,
		guide, aUnder, axb, pct, abc, naive, cached)
	q("repo match sees the cache", `items.find({"repo":{"$match":"*-cache"}})`, cached)
	q("repo ordering", `items.find({"repo":{"$gt":"libs"}})`, jar, pom, util, readme, cached)
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
	out = append(out, Case{
		Name: "cap applies without a limit", Query: `items.find({"repo":"generic-local"})`,
		Options: aql.Options{Limit: 2}, Want: []string{guide, aUnder},
	})
	out = append(out, Case{
		Name: "own limit beats the cap", Query: `items.find({"repo":"generic-local"}).limit(3)`,
		Options: aql.Options{Limit: 2}, Want: []string{guide, aUnder, axb},
	})
	for n := range out {
		out[n].Options.Now = Now
	}
	return out
}
