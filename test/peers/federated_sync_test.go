package peers_test

import (
	"encoding/json"
	"net/http"
	"slices"
	"strings"
	"testing"
	"time"

	"github.com/weaveplatform/artifactweave/test/peers"
	"github.com/weaveplatform/artifactweave/test/server/support"
)

func TestAccFederatedSyncSQLite(t *testing.T) {
	t.Parallel()
	testFederatedSync(t, peers.SQLite)
}

func TestAccFederatedSyncPostgres(t *testing.T) {
	t.Parallel()
	testFederatedSync(t, peers.Postgres)
}

func TestAccFederatedFormatsSQLite(t *testing.T) {
	t.Parallel()
	testFederatedFormats(t, peers.SQLite)
}

func TestAccFederatedFormatsPostgres(t *testing.T) {
	t.Parallel()
	testFederatedFormats(t, peers.Postgres)
}

// quiet is how long a scenario waits to see that something does not
// arrive: well past a push to a reachable member, which lands in well
// under a second.
const quiet = 3 * time.Second

// federation pairs three instances for federated-repo/* and returns them.
func federation(t *testing.T, driver peers.Driver) (*peers.Cluster, []*peers.Instance) {
	t.Helper()
	c := peers.Start(t, peers.Options{Instances: 3, Driver: driver})
	all := c.Instances
	pair(t, all[0], all[1])
	pair(t, all[0], all[2])
	pair(t, all[1], all[2])
	return c, all
}

// federate creates key on the first instance with the others as members,
// and waits until every member holds it.
func federate(t *testing.T, all []*peers.Instance, key, packageType string) {
	t.Helper()
	members := make([]string, 0, len(all)-1)
	for _, in := range all[1:] {
		members = append(members, `{"url":"`+member(in, key)+`","enabled":true}`)
	}
	create := `{"rclass":"federated","packageType":"` + packageType + `","members":[` + strings.Join(members, ",") + `]}`
	if status, body := all[0].Do(t, http.MethodPut, "/artifactory/api/repositories/"+key, create); status != http.StatusOK {
		t.Fatalf("create %s on %s: %d %s", key, all[0].Name, status, body)
	}
	for _, in := range all {
		await(t, in, key, "every member", func(r federatedRepo) bool { return len(r.Members) == len(all) })
	}
}

// eventually polls cond on in until it holds, within syncWait.
func eventually(t *testing.T, in *peers.Instance, what string, cond func() (bool, string)) {
	t.Helper()
	deadline := time.Now().Add(syncWait)
	for {
		ok, last := cond()
		if ok {
			return
		}
		if time.Now().After(deadline) {
			t.Fatalf("%s never had %s: last %s", in.Name, what, last)
		}
		time.Sleep(250 * time.Millisecond)
	}
}

// file reads in's file at repository path p: its content, or ok false
// while there is none.
func file(t *testing.T, in *peers.Instance, p string) (string, bool) {
	t.Helper()
	status, body := in.Do(t, http.MethodGet, "/artifactory/"+p, "")
	return body, status == http.StatusOK
}

// awaitFile waits until every instance in ins serves p with want, or
// serves nothing at p when want is empty.
func awaitFile(t *testing.T, ins []*peers.Instance, p, want string) {
	t.Helper()
	for _, in := range ins {
		eventually(t, in, p+" as "+want, func() (bool, string) {
			got, ok := file(t, in, p)
			if want == "" {
				return !ok, got
			}
			return ok && got == want, got
		})
	}
}

// absent checks that no instance of ins received p within quiet.
func absent(t *testing.T, ins []*peers.Instance, p string) {
	t.Helper()
	time.Sleep(quiet)
	for _, in := range ins {
		if got, ok := file(t, in, p); ok {
			t.Fatalf("%s received %s while it should not have: %q", in.Name, p, got)
		}
	}
}

func properties(t *testing.T, in *peers.Instance, p string) map[string][]string {
	t.Helper()
	status, body := in.Do(t, http.MethodGet, "/artifactory/api/storage/"+p+"?properties", "")
	var got struct {
		Properties map[string][]string `json:"properties"`
	}
	if status == http.StatusOK {
		_ = json.Unmarshal([]byte(body), &got)
	}
	return got.Properties
}

func do(t *testing.T, in *peers.Instance, method, path, body string, want int) {
	t.Helper()
	if status, got := in.Do(t, method, path, body); status != want {
		t.Fatalf("%s %s on %s: %d %s, want %d", method, path, in.Name, status, got, want)
	}
}

// membersBody is a federated repository update naming every instance of
// all as a member of key, enabled unless disabled names it.
func membersBody(all []*peers.Instance, key string, disabled *peers.Instance) string {
	ms := make([]string, 0, len(all))
	for _, in := range all {
		ms = append(ms, `{"url":"`+member(in, key)+`","enabled":`+map[bool]string{true: "false", false: "true"}[in == disabled]+`}`)
	}
	return `{"members":[` + strings.Join(ms, ",") + `]}`
}

// testFederatedSync federates a generic repository across three instances
// (#1931, decision 0022): deploys, property changes, moves, copies and
// deletes made on any member reach the others; concurrent writes to one
// path, and a delete concurrent with a change, settle the same on every
// member by the conflict rule; a partitioned member catches up once the
// partition heals; and a disabled member receives nothing until it is
// enabled again.
func testFederatedSync(t *testing.T, driver peers.Driver) {
	c, all := federation(t, driver)
	a, b, d := all[0], all[1], all[2]
	federate(t, all, "fed", "generic")

	// Each member's changes reach the others.
	do(t, a, http.MethodPut, "/artifactory/fed/a/one.txt", "one", http.StatusCreated)
	do(t, b, http.MethodPut, "/artifactory/fed/b/two.txt", "two", http.StatusCreated)
	do(t, d, http.MethodPut, "/artifactory/fed/d/three.txt", "three", http.StatusCreated)
	awaitFile(t, all, "fed/a/one.txt", "one")
	awaitFile(t, all, "fed/b/two.txt", "two")
	awaitFile(t, all, "fed/d/three.txt", "three")

	do(t, b, http.MethodPut, "/artifactory/api/storage/fed/a/one.txt?properties=color=blue", "", http.StatusNoContent)
	for _, in := range all {
		eventually(t, in, "a/one.txt's property", func() (bool, string) {
			got := properties(t, in, "fed/a/one.txt")
			return slices.Equal(got["color"], []string{"blue"}), strings.Join(got["color"], ",")
		})
	}

	do(t, d, http.MethodPost, "/artifactory/api/move/fed/b/two.txt?to=/fed/moved/two.txt", "", http.StatusOK)
	awaitFile(t, all, "fed/moved/two.txt", "two")
	awaitFile(t, all, "fed/b/two.txt", "")

	do(t, a, http.MethodPost, "/artifactory/api/copy/fed/d/three.txt?to=/fed/copied/three.txt", "", http.StatusOK)
	awaitFile(t, all, "fed/copied/three.txt", "three")
	awaitFile(t, all, "fed/d/three.txt", "three")

	do(t, b, http.MethodDelete, "/artifactory/fed/a/one.txt", "", http.StatusNoContent)
	awaitFile(t, all, "fed/a/one.txt", "")

	// Concurrent writes while no member reaches another: the later write
	// wins on every member, and so does a delete later than a change.
	c.Partition(0, 1)
	c.Partition(0, 2)
	c.Partition(1, 2)
	do(t, a, http.MethodPut, "/artifactory/fed/conflict.txt", "from a", http.StatusCreated)
	do(t, a, http.MethodPut, "/artifactory/api/storage/fed/copied/three.txt?properties=late=change", "", http.StatusNoContent)
	time.Sleep(10 * time.Millisecond)
	do(t, b, http.MethodPut, "/artifactory/fed/conflict.txt", "from b", http.StatusCreated)
	do(t, d, http.MethodDelete, "/artifactory/fed/copied/three.txt", "", http.StatusNoContent)
	c.Heal(0, 1)
	c.Heal(0, 2)
	c.Heal(1, 2)
	awaitFile(t, all, "fed/conflict.txt", "from b")
	awaitFile(t, all, "fed/copied/three.txt", "")

	// A partitioned member catches up after the partition heals.
	c.Partition(2, 0)
	c.Partition(2, 1)
	do(t, a, http.MethodPut, "/artifactory/fed/late/a.txt", "late a", http.StatusCreated)
	do(t, b, http.MethodPut, "/artifactory/fed/late/b.txt", "late b", http.StatusCreated)
	awaitFile(t, []*peers.Instance{a, b}, "fed/late/a.txt", "late a")
	awaitFile(t, []*peers.Instance{a, b}, "fed/late/b.txt", "late b")
	absent(t, []*peers.Instance{d}, "fed/late/a.txt")
	c.Heal(2, 0)
	c.Heal(2, 1)
	awaitFile(t, []*peers.Instance{d}, "fed/late/a.txt", "late a")
	awaitFile(t, []*peers.Instance{d}, "fed/late/b.txt", "late b")

	// A disabled member receives nothing until it is enabled again; the
	// others go on exchanging changes meanwhile.
	do(t, a, http.MethodPost, "/artifactory/api/repositories/fed", membersBody(all, "fed", d), http.StatusOK)
	for _, in := range all {
		await(t, in, "fed", "the disabled member", func(r federatedRepo) bool {
			for _, m := range r.Members {
				if m.URL == member(d, "fed") {
					return !m.Enabled
				}
			}
			return false
		})
	}
	do(t, a, http.MethodPut, "/artifactory/fed/paused/a.txt", "paused a", http.StatusCreated)
	do(t, b, http.MethodPut, "/artifactory/fed/paused/b.txt", "paused b", http.StatusCreated)
	awaitFile(t, []*peers.Instance{b}, "fed/paused/a.txt", "paused a")
	awaitFile(t, []*peers.Instance{a}, "fed/paused/b.txt", "paused b")
	absent(t, []*peers.Instance{d}, "fed/paused/a.txt")
	absent(t, []*peers.Instance{d}, "fed/paused/b.txt")
	do(t, a, http.MethodPost, "/artifactory/api/repositories/fed", membersBody(all, "fed", nil), http.StatusOK)
	awaitFile(t, []*peers.Instance{d}, "fed/paused/a.txt", "paused a")
	awaitFile(t, []*peers.Instance{d}, "fed/paused/b.txt", "paused b")
}

// testFederatedFormats federates Maven, npm and Docker repositories across
// three instances (#1931): each member recomputes the format's metadata
// itself from the files it receives, so maven-metadata.xml, the npm
// package document and the Docker registry's manifest and blob answers
// show the same versions on every member.
func testFederatedFormats(t *testing.T, driver peers.Driver) {
	_, all := federation(t, driver)
	a, b, d := all[0], all[1], all[2]
	federate(t, all, "fed-maven", "maven")
	federate(t, all, "fed-npm", "npm")
	federate(t, all, "fed-docker", "docker")

	// Maven: the members compute maven-metadata.xml for a version deployed
	// on one of them.
	const pom = `<project><modelVersion>4.0.0</modelVersion><groupId>com.example</groupId>` +
		`<artifactId>lib</artifactId><version>1.0</version></project>`
	do(t, b, http.MethodPut, "/artifactory/fed-maven/com/example/lib/1.0/lib-1.0.pom", pom, http.StatusCreated)
	do(t, b, http.MethodPut, "/artifactory/fed-maven/com/example/lib/1.0/lib-1.0.jar", "jar", http.StatusCreated)
	for _, in := range all {
		eventually(t, in, "maven-metadata.xml listing 1.0", func() (bool, string) {
			got, ok := file(t, in, "fed-maven/com/example/lib/maven-metadata.xml")
			return ok && strings.Contains(got, "<version>1.0</version>"), got
		})
	}

	// npm: a package published on one member resolves on the others.
	do(t, d, http.MethodPut, "/artifactory/api/npm/fed-npm/fedpkg", support.NPMPublishBody("fedpkg", "1.2.3"), http.StatusCreated)
	for _, in := range all {
		eventually(t, in, "fedpkg 1.2.3", func() (bool, string) {
			status, body := in.Do(t, http.MethodGet, "/artifactory/api/npm/fed-npm/fedpkg", "")
			var doc struct {
				DistTags map[string]string `json:"dist-tags"`
				Versions map[string]any    `json:"versions"`
			}
			ok := status == http.StatusOK && json.Unmarshal([]byte(body), &doc) == nil
			return ok && doc.DistTags["latest"] == "1.2.3" && doc.Versions["1.2.3"] != nil, body
		})
	}

	// Docker: an image pushed to one member pulls from the others.
	for _, blob := range []string{support.RegistryConfig, "registry layer"} {
		do(t, a, http.MethodPost, "/v2/fed-docker/app/blobs/uploads/?digest="+support.SHA256Digest(blob), blob, http.StatusCreated)
	}
	manifest := support.RegistryManifest("federated")
	if status, _, body := a.DoHeader(t, http.MethodPut, "/v2/fed-docker/app/manifests/v1", manifest,
		http.Header{"Content-Type": {"application/vnd.oci.image.manifest.v1+json"}}); status != http.StatusCreated {
		t.Fatalf("push the manifest: %d %s", status, body)
	}
	accept := http.Header{"Accept": {"application/vnd.oci.image.manifest.v1+json"}}
	for _, in := range all[1:] {
		eventually(t, in, "the pushed manifest", func() (bool, string) {
			status, header, body := in.DoHeader(t, http.MethodGet, "/v2/fed-docker/app/manifests/v1", "", accept)
			return status == http.StatusOK && body == manifest &&
				header.Get("Docker-Content-Digest") == support.SHA256Digest(manifest), body
		})
		eventually(t, in, "the image's layer", func() (bool, string) {
			status, _, body := in.DoHeader(t, http.MethodGet, "/v2/fed-docker/app/blobs/"+support.RegistryLayer, "", nil)
			return status == http.StatusOK && body == "registry layer", body
		})
	}
}
