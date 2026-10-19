package server

import (
	"testing"

	"github.com/weaveplatform/artifactweave/internal/service/item"
	"github.com/weaveplatform/artifactweave/pkg/blob"
	"github.com/weaveplatform/artifactweave/pkg/store"
)

// The npm operations that change a package after its publish: npm
// dist-tag (#289), whose packument update for npm deprecate is
// npmPublish's, and npm unpublish (#290).
const (
	npmTagList   = "GET " + npmBase + "{repoKey}/-/package/{package}/dist-tags"
	npmTagAdd    = "PUT " + npmBase + "{repoKey}/-/package/{package}/dist-tags/{tag}"
	npmTagRemove = "DELETE " + npmBase + "{repoKey}/-/package/{package}/dist-tags/{tag}"
	// npmUnpublishVersions is the PUT of the packument less the versions to
	// remove; npmUnpublishTarball the tarball DELETE that follows it, and
	// npmUnpublish the DELETE of the whole package.
	npmUnpublishVersions = "PUT " + npmBase + "{repoKey}/{package}/-rev/{rev}"
	npmUnpublishTarball  = "DELETE " + npmBase + "{repoKey}/{package}/-/{tarball}/-rev/{rev}"
	npmUnpublish         = "DELETE " + npmBase + "{repoKey}/{package}/-rev/{rev}"
)

// npmLifecycleStatusPart is the status table's share for those operations.
// Its rows run after npm's own, on packages of their own.
var npmLifecycleStatusPart = statusPart{
	handwritten: map[string]bool{
		npmTagList: true, npmTagAdd: true, npmTagRemove: true,
		npmUnpublishVersions: true, npmUnpublishTarball: true, npmUnpublish: true,
	},
	rows: func() []statusCase {
		local := npmBase + "npm-local/"
		tags := local + "-/package/status-tags/dist-tags"
		gone := local + "status-gone/-/status-gone-"
		return []statusCase{
			{npmTagList, tags, "admin", "", 200},
			{npmTagList, local + "-/package/@acme%2fscoped/dist-tags", "admin", "", 200},
			{npmTagList, local + "-/package/absent/dist-tags", "admin", "", 404},
			{npmTagList, local + "-/package/status-tags", "admin", "", 404},
			{npmTagList, tags, "user", "", 403},
			{npmTagList, tags, "anonymous", "", 401},
			{npmTagList, tags, "", "", 401},
			{npmTagList, npmBase + "npm-remote/-/package/status-tags/dist-tags", "admin", "", 501},
			{npmTagAdd, tags + "/stable", "admin", `"1.0.0"`, 200},
			{npmTagAdd, tags + "/latest", "admin", `"1.0.0"`, 200},
			// npm refuses a tag that reads as a version range, and so does
			// the registry.
			{npmTagAdd, tags + "/1.x", "admin", `"1.0.0"`, 400},
			{npmTagAdd, tags + "/stable", "admin", `1.0.0`, 400},
			{npmTagAdd, tags + "/stable", "admin", `"9.9.9"`, 404},
			{npmTagAdd, local + "-/package/absent/dist-tags/stable", "admin", `"1.0.0"`, 404},
			{npmTagAdd, tags, "admin", `{}`, 405},
			{npmTagAdd, tags + "/stable", "user", `"1.0.0"`, 403},
			{npmTagAdd, tags + "/stable", "anonymous", `"1.0.0"`, 401},
			{npmTagAdd, tags + "/stable", "", `"1.0.0"`, 401},
			{npmTagRemove, tags + "/stable", "admin", "", 200},
			{npmTagRemove, tags + "/stable", "admin", "", 404},
			// latest taken off returns to the version that outranks the
			// others.
			{npmTagRemove, tags + "/latest", "admin", "", 200},
			{npmTagRemove, tags + "/latest", "user", "", 403},
			{npmTagRemove, tags + "/latest", "anonymous", "", 401},

			// npm deprecate's packument update.
			{npmPublish, local + "status-tags", "admin",
				`{"versions":{"1.0.0":{"deprecated":"use 2.0.0"}}}`, 200},
			{npmPublish, local + "status-tags", "admin", `{"versions":[]}`, 400},
			{npmPublish, local + "absent", "admin", `{"versions":{}}`, 404},
			{npmPublish, local + "status-tags", "user", `{"versions":{}}`, 403},

			{npmUnpublishVersions, local + "status-gone/-rev/1-0", "admin",
				`{"versions":{"1.0.0":{},"2.0.0":{}}}`, 200},
			{npmUnpublishVersions, local + "status-gone/-rev/1-0", "admin", `{"name":"x"}`, 400},
			{npmUnpublishVersions, local + "absent/-rev/1-0", "admin", `{"versions":{}}`, 404},
			{npmUnpublishVersions, local + "status-gone/-rev/1-0", "user", `{"versions":{}}`, 403},
			{npmUnpublishVersions, local + "status-gone/-rev/1-0", "anonymous",
				`{"versions":{}}`, 401},
			// The PUT removed 1.1.0, so npm's DELETE after it finds it gone.
			{npmUnpublishTarball, gone + "1.1.0.tgz/-rev/1-0", "admin", "", 404},
			{npmUnpublishTarball, gone + "2.0.0.tgz/-rev/1-0", "user", "", 403},
			{npmUnpublishTarball, gone + "2.0.0.tgz/-rev/1-0", "anonymous", "", 401},
			{npmUnpublishTarball, gone + "2.0.0.tgz/-rev/1-0", "admin", "", 200},
			{npmUnpublish, local + "status-gone/-rev/1-0", "user", "", 403},
			{npmUnpublish, local + "status-gone/-rev/1-0", "", "", 401},
			{npmUnpublish, local + "status-gone/-rev/1-0", "admin", "", 200},
			{npmUnpublish, local + "status-gone/-rev/1-0", "admin", "", 404},
			{npmUnpublish, local + "status-gone", "admin", "", 405},
			{npmUnpublish, npmBase + "npm-remote/status-gone/-rev/1-0", "admin", "", 501},
		}
	},
	seed: func(t *testing.T, db store.Store, blobs blob.Store) {
		t.Helper()
		items := &item.Service{Store: db, Blobs: blobs}
		deployNPM(t, items, "npm-local", "status-tags", "1.0.0", nil)
		deployNPM(t, items, "npm-local", "status-tags", "2.0.0", nil)
		for _, v := range []string{"1.0.0", "1.1.0", "2.0.0"} {
			deployNPM(t, items, "npm-local", "status-gone", v, nil)
		}
	},
}
