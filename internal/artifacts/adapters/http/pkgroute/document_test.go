package pkgroute_test

import (
	"context"
	"fmt"
	"io"
	"net/http"
	"strings"
	"testing"

	"github.com/weaveplatform/artifactweave/internal/artifacts/adapters/http/pkgroute"
	"github.com/weaveplatform/artifactweave/internal/artifacts/api/repo"
	"github.com/weaveplatform/artifactweave/internal/artifacts/core/remote/source"
)

// boundarySource reads a remote repository as the source boundary does
// for the download policy: it refuses a read the remote's own policy
// refuses, at the read's PolicyPath when it names one, and otherwise
// answers "remote".
type boundarySource struct{}

func (boundarySource) Item(
	context.Context,
	pkgroute.Store,
	repo.Config,
	string,
) (pkgroute.Found, error) {
	return pkgroute.Found{}, source.ErrUnsupported
}

func (boundarySource) Read(
	_ *http.Request,
	c repo.Config,
	resource source.Resource,
) (source.Result, error) {
	if err := resource.CheckPolicy(c, resource.Path); err != nil {
		return source.Result{}, fmt.Errorf("source download: %w", err)
	}
	return source.Result{Stream: &source.Stream{
		Status: http.StatusOK, Body: io.NopCloser(strings.NewReader("remote")),
		Representation: source.Representation{Size: int64(len("remote"))},
	}}, nil
}

// boundaryFixture is policyFixture with gen-remote read through
// boundarySource.
func boundaryFixture(t *testing.T) *kitFixture {
	t.Helper()
	return policyFixture(t, map[repo.Class]pkgroute.Source{
		repo.Local: pkgroute.Local{}, repo.Federated: pkgroute.Local{},
		repo.Virtual: pkgroute.Local{}, repo.Remote: boundarySource{},
	})
}

// wantChecked checks that GET path as reader answers 204.
func (f *kitFixture) wantChecked(t *testing.T, path string) {
	t.Helper()
	if rec := f.do(http.MethodGet, base+path, "reader", ""); rec.Code != http.StatusNoContent {
		t.Fatalf("GET %s: %d %s, want 204", path, rec.Code, rec.Body)
	}
}

// wantPassedOver checks that GET path as user answers 404 as a member
// passed over: kernel.ErrNotFound, no refusal IsRefusal reports, and a body
// that names no member.
func (f *kitFixture) wantPassedOver(t *testing.T, path, user, member string) {
	t.Helper()
	rec := f.do(http.MethodGet, base+path, user, "")
	if rec.Code != http.StatusNotFound || rec.Header().Get("X-Not-Found") != "true" ||
		rec.Header().Get("X-Refused") != "" || strings.Contains(rec.Body.String(), member) {
		t.Fatalf("GET %s: %d %v %s, want the member passed over", path, rec.Code, rec.Header(),
			rec.Body)
	}
}

// wantRefusedHeader checks that GET path as user answers a refusal that
// IsRefusal reports.
func (f *kitFixture) wantRefusedHeader(t *testing.T, path, user string) {
	t.Helper()
	if rec := f.do(http.MethodGet, base+path, user, ""); rec.Header().Get("X-Refused") != "true" {
		t.Fatalf("GET %s: %d %s is no refusal", path, rec.Code, rec.Body)
	}
}

// CheckBlackedOut refuses only a blacked-out repository, naming the path
// it is given, for a document computed for the request: the patterns do not
// refuse it, a virtual repository has no blackout, and a blacked-out member
// is passed over.
func TestAccDownloadPolicyBlackedOutCheck(t *testing.T) {
	f := policyFixture(t, nil)
	f.wantChecked(t, local+"/blackout/a/b.txt")
	f.patch(t, local, excludeA)
	f.wantChecked(t, local+"/blackout/a/b.txt")
	f.patch(t, local, restore)
	f.patch(t, local, blackOut)
	f.wantPolicy(t, local+"/blackout/a/b.txt", "root", http.StatusNotFound,
		fmt.Sprintf(blackedOut, "a/b.txt"))
	f.wantRefusedHeader(t, local+"/blackout/a/b.txt", "reader")
	f.wantPassedOver(t, "gen-virtual/first-blackout/a/b.txt", "reader", local)
	f.patch(t, local, restore)
	f.patch(t, "gen-virtual", blackOut)
	f.wantChecked(t, "gen-virtual/blackout/a/b.txt")
	f.wantChecked(t, "gen-virtual/first-blackout/a/b.txt")
	f.patch(t, "gen-virtual", excludeA)
	f.wantChecked(t, "gen-virtual/first-blackout/a/b.txt")
	if len(f.faults) != 0 {
		t.Fatalf("a refusal was recorded as a fault: %v", f.faults)
	}
}

// A read that names a PolicyPath is checked at that path, the one 7.x
// keeps the artifact at, by the repository's own policy and by a virtual
// repository's patterns through a member request; BlackoutOnly leaves the
// patterns out.
func TestAccDownloadPolicyReadPolicyPath(t *testing.T) {
	f := policyFixture(t, nil)
	const policy = "?policy=policy/a/b.txt"
	f.patch(t, local, func(c *repo.Config) { c.ExcludesPattern = strPtr("policy/**") })
	f.wantPolicy(t, local+"/read-as/a/b.txt"+policy, "reader", http.StatusNotFound,
		fmt.Sprintf(excluded, "policy/a/b.txt"))
	f.wantRefusedHeader(t, local+"/read-as/a/b.txt"+policy, "reader")
	f.wantServed(t, local+"/read-as/a/b.txt", "reader", local+"/a/b.txt")
	f.wantServed(t, local+"/read-as/a/b.txt"+policy+"&blackout", "reader", local+"/a/b.txt")
	f.patch(t, local, restore)
	f.patch(t, local, excludeA)
	f.wantServed(t, local+"/read-as/a/b.txt"+policy, "reader", local+"/a/b.txt")
	f.patch(t, local, restore)
	f.patch(t, local, blackOut)
	f.wantPolicy(t, local+"/read-as/a/b.txt"+policy+"&blackout", "reader",
		http.StatusNotFound, fmt.Sprintf(blackedOut, "policy/a/b.txt"))
	f.patch(t, local, restore)
	f.patch(t, "gen-virtual", func(c *repo.Config) { c.ExcludesPattern = strPtr("policy/**") })
	f.wantPolicy(t, "gen-virtual/first-read-as/a/b.txt"+policy, "reader", http.StatusNotFound,
		"No repository found to serve the request for gen-virtual:policy/a/b.txt")
	f.wantServed(t, "gen-virtual/first-read-as/a/b.txt", "reader", local+"/a/b.txt")
	f.wantServed(t, "gen-virtual/first-read-as/a/b.txt"+policy+"&blackout", "reader",
		local+"/a/b.txt")
}

// A remote repository's own policy, which the source boundary applies to
// its reads, is checked by CheckDocument and CheckBlackedOut too, at the
// document's path. Through a virtual repository a remote member that
// refuses, whether the boundary refuses its read or the kit refuses the
// document, is passed over as a local member is, and the answer names no
// member; read directly, the refusal is the remote's own.
func TestAccDownloadPolicyRemoteDocuments(t *testing.T) {
	f := boundaryFixture(t)
	const remote = "gen-remote"
	f.patch(t, "gen-virtual", func(c *repo.Config) { c.Repositories = []string{remote, local} })
	f.wantChecked(t, remote+"/document/a/b.txt")
	rec := f.do(http.MethodGet, base+"gen-virtual/first-read-as/a/b.txt", "root", "")
	if rec.Code != http.StatusOK || rec.Body.String() != "remote" {
		t.Fatalf("remote member read: %d %s", rec.Code, rec.Body)
	}
	f.patch(t, remote, excludeA)
	const rejected = "The repository 'gen-remote' rejected the artifact 'gen-remote:a/b.txt' " +
		"due to its include/exclude pattern settings."
	f.wantPolicy(t, remote+"/document/a/b.txt", "root", http.StatusNotFound, rejected)
	f.wantRefusedHeader(t, remote+"/document/a/b.txt", "root")
	f.wantChecked(t, remote+"/blackout/a/b.txt")
	f.wantPolicy(t, remote+"/read-as/a/b.txt", "root", http.StatusNotFound, rejected)
	f.wantRefusedHeader(t, remote+"/read-as/a/b.txt", "root")
	f.wantPassedOver(t, "gen-virtual/first-read-as/a/b.txt", "root", remote)
	f.wantPassedOver(t, "gen-virtual/first-document/a/b.txt", "root", remote)
	rec = f.do(http.MethodGet, base+"gen-virtual/first-read-as/a/b.txt?policy=dir/b.txt",
		"root", "")
	if rec.Code != http.StatusOK || rec.Body.String() != "remote" {
		t.Fatalf("a policy path the remote's patterns accept: %d %s", rec.Code, rec.Body)
	}
	f.patch(t, remote, restore)
	f.patch(t, remote, blackOut)
	f.wantPolicy(
		t,
		remote+"/blackout/a/b.txt",
		"root",
		http.StatusNotFound,
		"The repository 'gen-remote' is blacked out and cannot serve artifact 'gen-remote:a/b.txt'.",
	)
	f.wantPassedOver(t, "gen-virtual/first-blackout/a/b.txt", "root", remote)
	f.wantPassedOver(t, "gen-virtual/first-read-as/a/b.txt?blackout", "root", remote)
	rec = f.do(http.MethodGet, base+"gen-virtual/merge/a/b.txt", "reader", "")
	if rec.Code != http.StatusOK || rec.Body.String() != local {
		t.Fatalf("a blacked-out remote member was merged: %d %q", rec.Code, rec.Body)
	}
	if len(f.faults) != 0 {
		t.Fatalf("a refusal was recorded as a fault: %v", f.faults)
	}
}
