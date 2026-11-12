package puppethttp_test

import (
	"encoding/json"
	"fmt"
	"strings"
	"testing"

	"github.com/weaveplatform/artifactweave/internal/artifacts/api/repo"
)

// fullManifest is a metadata.json as the PDK writes one, in its order:
// author, summary, license and source after version, then the links,
// dependencies, operatingsystem_support, requirements, tags and the PDK's
// own keys (puppetlabs-sqlserver 2.5.0's sources), with a description, a
// null, a boolean and a number whose text a decoder would change.
const fullManifest = `{
  "name": "acme-full",
  "version": "2.5.0",
  "author": "acme",
  "summary": "Full <manifest> & more",
  "license": "Apache-2.0",
  "source": "https://example.invalid/acme/full",
  "project_page": "https://example.invalid/acme/full",
  "issues_url": "https://example.invalid/acme/full/issues",
  "dependencies": [],
  "operatingsystem_support": [
    {"operatingsystem": "Windows", "operatingsystemrelease": ["2019", "2022"]}
  ],
  "requirements": [{"name": "puppet", "version_requirement": ">= 7.0.0 < 9.0.0"}],
  "description": "A module with every field",
  "tags": ["sql", "windows"],
  "pdk-version": "3.0.0",
  "template-url": "https://example.invalid/pdk-templates#3.0.0",
  "data_provider": null,
  "mirror": true,
  "weight": 1.50
}`

// fullMetadata is the metadata a release record answers for fullManifest:
// the fields 7.x writes first, in its order, then the file's others in the
// file's order, compacted, with "<", ">" and "&" as they are.
const fullMetadata = `{"name":"acme-full","version":"2.5.0",` +
	`"source":"https://example.invalid/acme/full","author":"acme","license":"Apache-2.0",` +
	`"summary":"Full <manifest> & more","description":"A module with every field",` +
	`"project_page":"https://example.invalid/acme/full",` +
	`"issues_url":"https://example.invalid/acme/full/issues","tags":["sql","windows"],` +
	`"dependencies":[],"operatingsystem_support":[{"operatingsystem":"Windows",` +
	`"operatingsystemrelease":["2019","2022"]}],"requirements":[{"name":"puppet",` +
	`"version_requirement":">= 7.0.0 < 9.0.0"}],"pdk-version":"3.0.0",` +
	`"template-url":"https://example.invalid/pdk-templates#3.0.0","data_provider":null,` +
	`"mirror":true,"weight":1.50}`

// withTags returns release record rec with tags as its top-level tags.
func withTags(rec, tags string) string {
	return strings.Replace(rec, `"tags":[],"supported"`, `"tags":`+tags+`,"supported"`, 1)
}

// A hosted release's record answers its whole metadata.json as its
// metadata, in 7.x's order, on every route that answers the record and
// through a virtual repository (#1325) [I], and its tags as its top-level
// tags, which the Forge derives from metadata.json (its v3 API
// description).
func TestUnitReleaseMetadataIsTheWholeFile(t *testing.T) {
	f := newFixture(t, repo.Local)
	rel := f.deployRelease(
		f.c,
		"acme-full",
		"2.5.0",
		manifestArchive(t, "acme-full-2.5.0", fullManifest),
	)
	release := withTags(recordJSON(rel, fullMetadata, 0), `["sql","windows"]`)
	module := fmt.Sprintf(`{"uri":"/v3/modules/acme-full","slug":"acme-full","name":"full",`+
		`"downloads":0,"created_at":"%s","updated_at":"%s","supported":false,`+
		`"endorsement":null,"module_group":"base","owner":`+hostedOwner+`,`+
		`"current_release":%s,"releases":[{"uri":"/v3/releases/acme-full-2.5.0","version":"2.5.0"}],`+
		`"homepage_url":"https://example.invalid/acme/full",`+
		`"issues_url":"https://example.invalid/acme/full/issues"}`,
		forgeDate(rel.item.Created), forgeDate(rel.item.Updated), release)
	f.create(repo.Config{
		Key: "virtual", Class: repo.Virtual, PackageType: repo.Puppet,
		Repositories: []string{f.c.Key},
	})
	for _, prefix := range []string{base, "/artifactory/api/puppet/virtual"} {
		wantBody(t, f.do("GET", prefix+"/v3/releases/acme-full-2.5.0", "admin"), release+"\n")
		wantBody(t, f.do("GET", prefix+"/v3/releases?module=acme-full", "admin"),
			listJSON("/v3/releases?module=acme-full", 20, 0, 1, release))
		wantBody(t, f.do("GET", prefix+"/v3/modules/acme-full", "admin"), module+"\n")
		wantBody(t, f.do("GET", prefix+"/v3/modules?query=acme-full", "admin"),
			listJSON("/v3/modules?query=acme-full", 20, 0, 1, module))
	}
	wantBody(
		t,
		f.do("GET", base+"/v3/releases", "admin"),
		listJSON("/v3/releases", 20, 0, 1, release),
	)
	wantBody(t, f.do("GET", base+"/v3/modules", "admin"), listJSON("/v3/modules", 20, 0, 1, module))
}

// tags that are not a list of strings stay in the metadata as the file
// gives them and leave the top-level tags empty [I].
func TestUnitReleaseTagsOfAnotherType(t *testing.T) {
	f := newFixture(t, repo.Local)
	for version, tags := range map[string]string{
		"1.0.0": `"sql"`, "1.1.0": `["sql",1]`, "1.2.0": `null`, "1.3.0": `["sql"]`,
	} {
		manifest := fmt.Sprintf(
			`{"name":"acme-odd","version":%q,"summary":"odd","tags":%s,"dependencies":[]}`,
			version,
			tags,
		)
		rel := f.deployRelease(
			f.c,
			"acme-odd",
			version,
			manifestArchive(t, "acme-odd-"+version, manifest),
		)
		metadata := fmt.Sprintf(`{"name":"acme-odd","version":%q,"source":null,"author":null,`+
			`"license":null,"summary":"odd","description":null,"project_page":null,`+
			`"issues_url":null,"tags":%s,"dependencies":[]}`, version, tags)
		want := recordJSON(rel, metadata, 0)
		if version == "1.3.0" {
			want = withTags(want, `["sql"]`)
		}
		wantBody(t, f.do("GET", base+"/v3/releases/acme-odd-"+version, "admin"), want+"\n")
	}
}

// An index entry without metadata.json's document, or with one that is not
// an object, answers as an entry that does not match its archive does.
func TestUnitStoredIndexWithoutTheDocument(t *testing.T) {
	for name, document := range map[string]json.RawMessage{
		"no document":     nil,
		"not an object":   json.RawMessage(`[1]`),
		"an empty string": json.RawMessage(`""`),
	} {
		t.Run(name, func(t *testing.T) {
			f := newFixture(t, repo.Local)
			f.deployRelease(f.c, "acme-widget", "1.0.0", linkedArchive(t, "1.0.0"))
			pkg, err := f.db.Packages().Find(t.Context(), f.c.Key, "acme-widget")
			if err != nil {
				t.Fatal(err)
			}
			v, err := f.db.Packages().Version(t.Context(), pkg.ID, "1.0.0")
			if err != nil {
				t.Fatal(err)
			}
			var entry map[string]json.RawMessage
			if err = json.Unmarshal(v.Metadata, &entry); err != nil {
				t.Fatal(err)
			}
			if entry["document"] == nil {
				t.Fatal("the index entry keeps no document")
			}
			delete(entry, "document")
			if document != nil {
				entry["document"] = document
			}
			if v.Metadata, err = json.Marshal(entry); err != nil {
				t.Fatal(err)
			}
			if _, err = f.db.Packages().PutVersion(t.Context(), v); err != nil {
				t.Fatal(err)
			}
			wantError(t, f.do("GET", base+"/v3/releases/acme-widget-1.0.0", "admin"), 502,
				"invalid stored Puppet package index")
		})
	}
}
