package artifacts_test

import (
	"net/http"
	"testing"
	"time"

	accessapi "github.com/weaveplatform/artifactweave/internal/access/api"
	"github.com/weaveplatform/artifactweave/internal/artifacts/api/repo"
	artmodel "github.com/weaveplatform/artifactweave/internal/artifacts/core/model"
)

// A read through a virtual repository is decided by its members, and a
// walk that finds nothing answers 7.x's statuses (decision 0008, R3 and R4;
// #1533): the virtual repository itself is never asked for Read, a member
// the caller may not read is passed over and remembered, and the answer is
// DownloadServiceImpl.respondResourceNotFound's for the unfound resource
// ArtifactoryVirtualDownloadStrategy.processStandard returns (7.161.26).

// virtualStatusWorld is an item world, with anonymous access as anonymous
// says, holding the local repositories a-local and b-local, the virtual
// repositories virt-ab over them, hide-ab over them with
// hideUnauthorizedResources on, and virt-up over the remote up. user may
// read b-local alone; deployer may read libs-local alone, so neither a-local
// nor b-local. Both members hold both.bin, m.bin and z/<member>.zip; a-only.bin
// is a-local's alone; up's cache holds c.bin.
func virtualStatusWorld(t *testing.T, anonymous bool) *itemWorld {
	t.Helper()
	w := newItemWorld(t, anonymous)
	for _, c := range []repo.Config{
		{Key: "a-local", Class: repo.Local},
		{Key: "b-local", Class: repo.Local},
		{Key: "virt-ab", Class: repo.Virtual, Repositories: []string{"a-local", "b-local"}},
		{
			Key: "hide-ab", Class: repo.Virtual, Repositories: []string{"a-local", "b-local"},
			HideUnauthorizedResources: ptr(true),
		},
		{Key: "virt-up", Class: repo.Virtual, Repositories: []string{"up"}},
	} {
		if err := w.db.Repos().Create(t.Context(), *repo.Defaults(&c)); err != nil {
			t.Fatal(err)
		}
	}
	grant(t, w, "user-b", "user", "b-local", accessapi.ActionRead)
	zip := string(zipBytes(t, map[string]string{"in.txt": "entry"}))
	for _, member := range []string{"a", "b"} {
		local := "/artifactory/" + member + "-local/"
		// A SHA-1 declared, so that the .sha1 file is served
		// (client-checksums).
		wantStatus(t, w.do(http.MethodPut, local+"both.bin", "admin", member,
			"X-Checksum-Sha1", sha1Hex(member)), http.StatusCreated)
		w.put(local+"z/"+member+".zip", zip)
	}
	w.put("/artifactory/a-local/a-only.bin", "a")
	w.put("/artifactory/a-local/m.bin", "a")
	w.put("/artifactory/b-local/m.bin;k=v", "b")
	w.cacheFile("up", "c.bin", "cached")
	return w
}

// forbiddenIn is RealRepoBase.assertReadPermissions' refusal of user by
// member at p, with the path of the virtual repository virt.
func forbiddenIn(member, virt, p, user string) string {
	return "Download request for repo:path '" + member + ":" + p + "' is forbidden for user: '" +
		user + "'.; Path: '" + virt + ":" + p + "'"
}

// virtualAnswer is a request through a virtual repository and its answer.
type virtualAnswer struct {
	path, as string
	status   int
	message  string
}

// wantVirtualAnswers checks each answer on GET and HEAD: the status, the
// body on a GET, and the Basic challenge on a 401 alone. A message of ""
// with 200 names no body to check.
func wantVirtualAnswers(t *testing.T, w *itemWorld, answers []virtualAnswer) {
	t.Helper()
	for _, a := range answers {
		for _, method := range []string{http.MethodGet, http.MethodHead} {
			rec := w.do(method, "/artifactory/"+a.path, a.as, "")
			body := a.message
			if a.status != http.StatusOK {
				body = errorBody(t, a.status, a.message)
			}
			if rec.Code != a.status || method == http.MethodGet && rec.Body.String() != body {
				t.Errorf("%s %s as %q: %d %s, want %d %s",
					method, a.path, a.as, rec.Code, rec.Body, a.status, body)
			}
			challenge := rec.Header().Get("WWW-Authenticate")
			if a.status == http.StatusUnauthorized && challenge != basicChallenge ||
				a.status != http.StatusUnauthorized && challenge != "" {
				t.Errorf("%s %s as %q: challenge %q", method, a.path, a.as, challenge)
			}
		}
	}
}

// TestAccVirtualReadDecidedByMembers: a user whose permissions name a
// member and not the virtual repository reads through it, as JFrog's
// Permissions page says ("Access through a virtual repository is governed
// by the permissions a user already has on its member local and remote
// repositories"). Before, the item route asked Read on the virtual
// repository first, which only an ANY target grants, and answered 403
// "Forbidden".
func TestAccVirtualReadDecidedByMembers(t *testing.T) {
	w := virtualStatusWorld(t, false)
	w.put("/artifactory/libs-local/x.bin", "libs")
	wantVirtualAnswers(t, w, []virtualAnswer{
		// deployer may read libs-local alone.
		{"virt/x.bin", "deployer", http.StatusOK, "libs"},
		// user may read b-local alone: a-local, searched first, is passed
		// over.
		{"virt-ab/both.bin", "user", http.StatusOK, "b"},
		{"virt-ab/both.bin.sha1", "user", http.StatusOK, sha1Hex("b")},
		{"virt-ab/z/b.zip!/in.txt", "user", http.StatusOK, "entry"},
		{"hide-ab/both.bin", "user", http.StatusOK, "b"},
	})
	// The root of a virtual repository is readable where any member's root
	// is (AuthorizationServiceBase.computeUserHasPermissionsOnRepositoryRoot
	// 1047-1078), and refused as before where none is.
	wantStatus(t, w.do(http.MethodGet, "/artifactory/virt-ab/", "user", ""), http.StatusOK)
	wantStatus(t, w.do(http.MethodGet, "/artifactory/virt-ab/", "deployer", ""),
		http.StatusForbidden)
	// The storage API's item info asks the members too, and answers a
	// caller who may read none of them as a miss (getVirtualRepoItem,
	// #1537).
	rec := w.do(http.MethodGet, "/artifactory/api/storage/virt-ab/both.bin", "user", "")
	wantStatus(t, rec, http.StatusOK)
	wantStatus(t, w.do(http.MethodGet, "/artifactory/api/storage/virt-ab", "user", ""),
		http.StatusOK)
	if rec := w.do(http.MethodGet, "/artifactory/api/storage/virt-ab", "deployer", ""); rec.Code !=
		http.StatusNotFound || rec.Body.String() != errorBody(t, http.StatusNotFound, "Not Found") {
		t.Errorf("virt-ab's root as deployer: %d %s", rec.Code, rec.Body)
	}
}

// TestAccVirtualMissAndRefusalStatuses: a walk that finds nothing answers
// 404 "Could not find resource" when no member refused the caller, and the
// first refusal's 403 when one did, each with the virtual repository's path
// (respondResourceNotFound 1465-1497; newUnfoundResource 468-475). A
// remote member is refused under its cache's key, where 7.x keeps a
// remote's permissions (RemoteRepoBase.checkDownloadIsAllowed 744-758). A
// checksum and an archive entry answer for the file they are read from.
// Before, a miss answered 404 "Could not find resource" alone and a caller
// without Read on the virtual repository 403 "Forbidden".
func TestAccVirtualMissAndRefusalStatuses(t *testing.T) {
	w := virtualStatusWorld(t, false)
	const notFound = "Could not find resource; Path: 'virt-ab:none.bin'"
	wantVirtualAnswers(t, w, []virtualAnswer{
		{"virt-ab/none.bin", "admin", http.StatusNotFound, notFound},
		{"virt-ab/none.bin", "reader", http.StatusNotFound, notFound},
		{"virt-ab/none.bin.sha1", "reader", http.StatusNotFound, notFound},
		{
			"virt-ab/z/none.zip!/in.txt", "reader", http.StatusNotFound,
			"Could not find resource; Path: 'virt-ab:z/none.zip'",
		},
		// a-local refuses user whether or not it holds the path, as
		// checkDownloadIsAllowed checks before getInfo looks.
		{
			"virt-ab/a-only.bin", "user", http.StatusForbidden,
			forbiddenIn("a-local", "virt-ab", "a-only.bin", "user"),
		},
		{
			"virt-ab/none.bin", "user", http.StatusForbidden,
			forbiddenIn("a-local", "virt-ab", "none.bin", "user"),
		},
		{
			"virt-ab/a-only.bin.sha1", "user", http.StatusForbidden,
			forbiddenIn("a-local", "virt-ab", "a-only.bin", "user"),
		},
		{
			"virt-ab/z/a.zip!/in.txt", "user", http.StatusForbidden,
			forbiddenIn("a-local", "virt-ab", "z/a.zip", "user"),
		},
		// Neither member readable: the first refusal is named.
		{
			"virt-ab/both.bin", "deployer", http.StatusForbidden,
			forbiddenIn("a-local", "virt-ab", "both.bin", "deployer"),
		},
		{
			"virt-up/c.bin", "deployer", http.StatusForbidden,
			forbiddenIn("up-cache", "virt-up", "c.bin", "deployer"),
		},
	})
}

// TestAccVirtualHidesUnauthorizedResources: with the virtual repository's
// hideUnauthorizedResources on, a walk a member refused answers 404
// "Resource not found" with the path, to an anonymous caller too, which then
// gets no challenge (shouldHideUnauthorisedResponse 1499-1518; R4). Without
// it an anonymous caller a member refused gets the 401 challenge
// (shouldSendAuthRequired 1460-1463), with the text the item route sends a
// direct request's (#1572). A miss no member refused keeps its 404. Before,
// the flag was not read: the caller got 403 "Forbidden" or 401
// "Authentication is required".
func TestAccVirtualHidesUnauthorizedResources(t *testing.T) {
	w := virtualStatusWorld(t, true)
	const hidden = "Resource not found; Path: 'hide-ab:a-only.bin'"
	wantVirtualAnswers(t, w, []virtualAnswer{
		{"hide-ab/a-only.bin", "user", http.StatusNotFound, hidden},
		{"hide-ab/a-only.bin", "", http.StatusNotFound, hidden},
		{"hide-ab/a-only.bin.sha1", "", http.StatusNotFound, hidden},
		{
			"hide-ab/z/a.zip!/in.txt", "user", http.StatusNotFound,
			"Resource not found; Path: 'hide-ab:z/a.zip'",
		},
		{
			"hide-ab/none.bin", "admin", http.StatusNotFound,
			"Could not find resource; Path: 'hide-ab:none.bin'",
		},
		{"virt-ab/a-only.bin", "", http.StatusUnauthorized, "Unauthorized"},
		{"virt-ab/a-only.bin.sha1", "", http.StatusUnauthorized, "Unauthorized"},
		{"virt-ab/z/a.zip!/in.txt", "", http.StatusUnauthorized, "Unauthorized"},
	})
}

// TestAccVirtualPassesOverMemberRefusals: a member's download policy is
// checked before its Read, so a blacked-out member or one whose patterns
// reject the path is passed over silently, the caller unable to read it or
// not, and a later member serves (RealRepoBase.checkDownloadIsAllowed: its
// 404 is not the 403 processStandard remembers). So is a file whose
// properties conflict with the request's matrix parameters, as
// DbStoringRepoMixin.getInfo answers it unfound. Before, the caller who
// may read b-local alone got 403 "Forbidden", and the conflicting file
// answered 404 "Could not find resource".
func TestAccVirtualPassesOverMemberRefusals(t *testing.T) {
	w := virtualStatusWorld(t, false)
	for _, patch := range []func(*repo.Config){
		func(c *repo.Config) { c.BlackedOut = ptr(true) },
		func(c *repo.Config) { c.ExcludesPattern = ptr("**/*.bin") },
	} {
		w.patchRepo("a-local", patch)
		wantVirtualAnswers(t, w, []virtualAnswer{
			{"virt-ab/both.bin", "user", http.StatusOK, "b"},
			{"virt-ab/both.bin", "admin", http.StatusOK, "b"},
			{
				"virt-ab/a-only.bin", "user", http.StatusNotFound,
				"Could not find resource; Path: 'virt-ab:a-only.bin'",
			},
		})
		w.patchRepo("a-local", func(c *repo.Config) {
			c.BlackedOut, c.ExcludesPattern = ptr(false), ptr("")
		})
	}
	wantVirtualAnswers(t, w, []virtualAnswer{
		{"virt-ab/m.bin;k+=v", "admin", http.StatusOK, "b"},
		{
			"virt-ab/m.bin;k+=w", "admin", http.StatusNotFound,
			"Could not find resource; Path: 'virt-ab:m.bin'",
		},
	})
}

// TestAccVirtualPrefersAnExactMatrixMatch: a file that lacks a matrix
// parameter the request names serves it only when no later member's file
// carries it (PropertiesImpl.matchQuery's NO_MATCH; processStandard's
// closest match), and the walk looks for that file in local members and in
// remote members that synchronize properties alone
// (isNotSynchronizeProperties). Before, the first member's file answered.
func TestAccVirtualPrefersAnExactMatrixMatch(t *testing.T) {
	w := virtualStatusWorld(t, false)
	w.virtualOver("virt-up-a", "up", "a-local")
	w.cacheFile("up", "m.bin", "cached")
	cached, err := w.db.Items().Get(t.Context(), "up", true, "m.bin")
	if err == nil {
		err = w.db.Items().SetProperties(t.Context(), cached.ID,
			artmodel.Properties{"k": {"v"}}, time.Now())
	}
	if err != nil {
		t.Fatal(err)
	}
	wantVirtualAnswers(t, w, []virtualAnswer{
		// a-local's m.bin lacks k, b-local's carries k=v.
		{"virt-ab/m.bin;k=v", "admin", http.StatusOK, "b"},
		// b-local's conflicts: a-local's is the closest match.
		{"virt-ab/m.bin;k=w", "admin", http.StatusOK, "a"},
		// A blank value asks for nothing.
		{"virt-ab/m.bin;k=", "admin", http.StatusOK, "a"},
		// up does not synchronize properties, so its cache is not searched
		// once a-local's file is the closest match.
		{"virt-up-a/m.bin;k=v", "admin", http.StatusOK, "a"},
	})
	w.patchRepo("up", func(c *repo.Config) { c.SynchronizeProperties = ptr(true) })
	wantVirtualAnswers(t, w, []virtualAnswer{
		{"virt-up-a/m.bin;k=v", "admin", http.StatusOK, "cached"},
	})
	const path = "/artifactory/virt-ab/m.bin;k=w?trace"
	_, steps := traceOf(t, w, http.MethodGet, path, "admin")
	wantSteps(t, path, steps,
		"Received request",
		"Retrieving info from virtual repository 'virt-ab' type generic",
		"Searching for the resource within a-local",
		"Request includes query properties",
		"Request query properties don't match those that annotate the artifact",
		"Resource is not an exact match - keeping as closest match",
		"Searching for the resource within b-local",
		"Request includes query properties",
		"Request query properties conflict with those that annotate the artifact - "+
			"returning unfound resource",
		"Unable to find an exact matching resource - returning closest match",
		"Returning found resource from a-local:m.bin",
		"Identified requested resource as a file",
		"Responding with found resource",
		"Request succeeded",
	)
}

// TestAccVirtualWithoutMembersAnswers: a virtual repository left with no
// member to search, and one whose own patterns reject the path, answer
// getInfoFromSearchableRepositories' unfound resource (158-169), with the
// path respondResourceNotFound appends. Before, the first answered 404
// "Could not find resource" and the second had no path.
func TestAccVirtualWithoutMembersAnswers(t *testing.T) {
	w := virtualStatusWorld(t, false)
	w.patchRepo("virt-ab", func(c *repo.Config) { c.ExcludesPattern = ptr("**/*.secret") })
	wantVirtualAnswers(t, w, []virtualAnswer{
		{
			"loop/f.bin", "admin", http.StatusNotFound,
			"No repository found to serve the request for loop:f.bin; Path: 'loop:f.bin'",
		},
		{
			"loop/f.bin.sha1", "admin", http.StatusNotFound,
			"No repository found to serve the request for loop:f.bin; Path: 'loop:f.bin'",
		},
		{
			"virt-ab/x.secret", "admin", http.StatusNotFound,
			"No repository found to serve the request for virt-ab:x.secret; " +
				"Path: 'virt-ab:x.secret'",
		},
	})
}

// TestAccVirtualFolderRequestsKeepTheirAnswers: a folder request is 7.x's
// directory listing, whose answers are #1537's, so it keeps 404 "Could not
// find resource" and the refusal httpkit.Denied gives; only who may read
// it changes, the members deciding.
func TestAccVirtualFolderRequestsKeepTheirAnswers(t *testing.T) {
	w := virtualStatusWorld(t, false)
	w.put("/artifactory/b-local/dir/f.bin", "b")
	wantVirtualAnswers(t, w, []virtualAnswer{
		{"virt-ab/none/", "admin", http.StatusNotFound, "Could not find resource"},
		{"virt-ab/dir/", "deployer", http.StatusForbidden, "Forbidden"},
		{"loop/dir/", "admin", http.StatusNotFound, "Could not find resource"},
	})
	wantStatus(t, w.do(http.MethodGet, "/artifactory/virt-ab/dir/", "user", ""), http.StatusOK)
}

// TestAccVirtualTraceNamesTheRefusal: ?trace logs each member's refusal as
// RealRepoBase.createUnfoundRepoResource does ("Download denied (<reason>)
// - returning un-found resource"), names the member whose Read refusal is
// kept, as processStandard logs the first ("Request is forbidden by
// <key>"), and ends on the walk's answer.
func TestAccVirtualTraceNamesTheRefusal(t *testing.T) {
	w := virtualStatusWorld(t, false)
	denied := func(member, p, user string) string {
		return "Download denied (Download request for repo:path '" + member + ":" + p +
			"' is forbidden for user: '" + user + "'.) - returning un-found resource"
	}
	const path = "/artifactory/virt-ab/a-only.bin?trace"
	_, steps := traceOf(t, w, http.MethodGet, path, "user")
	wantSteps(t, path, steps,
		"Received request",
		"Retrieving info from virtual repository 'virt-ab' type generic",
		"Searching for the resource within a-local",
		denied("a-local", "a-only.bin", "user"),
		"Request is forbidden by a-local",
		"Searching for the resource within b-local",
		"Unable to find resource in b-local:a-only.bin",
		"Sending a response with the status '403' and the message '"+
			forbiddenIn("a-local", "virt-ab", "a-only.bin", "user")+"'",
	)
	// The second refusal is logged, and the first kept.
	const both = "/artifactory/virt-ab/both.bin?trace"
	_, steps = traceOf(t, w, http.MethodGet, both, "deployer")
	wantSteps(t, both, steps,
		"Received request",
		"Retrieving info from virtual repository 'virt-ab' type generic",
		"Searching for the resource within a-local",
		denied("a-local", "both.bin", "deployer"),
		"Request is forbidden by a-local",
		"Searching for the resource within b-local",
		denied("b-local", "both.bin", "deployer"),
		"Sending a response with the status '403' and the message '"+
			forbiddenIn("a-local", "virt-ab", "both.bin", "deployer")+"'",
	)
	// A policy refusal is logged and passed over, and names no member.
	w.patchRepo("a-local", func(c *repo.Config) { c.BlackedOut = ptr(true) })
	_, steps = traceOf(t, w, http.MethodGet, both, "user")
	wantSteps(t, both, steps,
		"Received request",
		"Retrieving info from virtual repository 'virt-ab' type generic",
		"Searching for the resource within a-local",
		"Download denied (The repository 'a-local' is blacked out and cannot serve artifact "+
			"'a-local:both.bin'.) - returning un-found resource",
		"Searching for the resource within b-local",
		"Returning found resource from b-local:both.bin",
		"Identified requested resource as a file",
		"Responding with found resource",
		"Request succeeded",
	)
}
