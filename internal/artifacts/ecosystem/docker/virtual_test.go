package docker

import (
	"fmt"
	"sync"
	"testing"
	"time"

	"github.com/weaveplatform/artifactweave/internal/artifacts/api/repo"
	"github.com/weaveplatform/artifactweave/internal/artifacts/core/virtual"
)

func TestUnitDockerVirtualMembers(t *testing.T) {
	configs := virtual.Configs{
		"first":    {Key: "first", Class: repo.Local, PackageType: repo.Docker},
		"priority": {Key: "priority", Class: repo.Local, PackageType: repo.Docker, PriorityResolution: repo.Flex(true)},
		"remote":   {Key: "remote", Class: repo.Remote, PackageType: repo.Docker},
		"inner":    {Key: "inner", Class: repo.Virtual, PackageType: repo.Docker, Repositories: []string{"first", "remote", "virtual"}},
	}
	c := repo.Config{Key: "virtual", Class: repo.Virtual, PackageType: repo.Docker, Repositories: []string{"inner", "priority", "remote"}}
	members, rev, err := VirtualMembers(t.Context(), configs, c, "team/image/latest/manifest.json")
	if err != nil {
		t.Fatal(err)
	}
	want := []string{"priority:false", "first:false", "remote:false", "remote:true"}
	if len(members) != len(want) {
		t.Fatal(members)
	}
	for i, m := range members {
		if got := fmt.Sprintf("%s:%t", m.Repo.Key, m.Upstream); got != want[i] {
			t.Fatal(got, want[i])
		}
	}
	inner := configs["inner"]
	inner.IncludesPattern = new("team/**")
	configs["inner"] = inner
	_, changed, err := VirtualMembers(t.Context(), configs, c, "team/image/latest/manifest.json")
	if err != nil || changed == rev {
		t.Fatal("nested pattern change kept revision", err)
	}
	inner.ExcludesPattern = new("team/**")
	configs["inner"] = inner
	members, _, err = VirtualMembers(t.Context(), configs, c, "team/image/latest/manifest.json")
	if err != nil || len(members) != 3 {
		t.Fatal(members, err)
	}
}

func TestUnitDockerMemberLocationCache(t *testing.T) {
	now := time.Unix(100, 0)
	c := NewMemberLocations()
	c.limit = 2
	c.now = func() time.Time { return now }
	digest := func(n int) string { return fmt.Sprintf("sha256:%064x", n) }
	one, two := MemberLocation{Key: "one"}, MemberLocation{Key: "two", Upstream: true}
	c.Put("v", "manifest", "latest", "a", one)
	if len(c.entries) != 0 {
		t.Fatal("tag cached")
	}
	c.Put("v", "manifest", digest(1), "a", one)
	c.Put("v", "blob", digest(1), "a", two)
	if got, ok := c.Get("v", "manifest", digest(1), "a"); !ok || got != one {
		t.Fatal(got, ok)
	}
	c.Put("other", "blob", digest(2), "a", two)
	if _, ok := c.Get("v", "blob", digest(1), "a"); ok {
		t.Fatal("least recent entry survived capacity")
	}
	if _, ok := c.Get("v", "manifest", digest(1), "b"); ok {
		t.Fatal("configuration change retained location")
	}
	c.Put("v", "manifest", digest(1), "b", one)
	c.Put("v", "manifest", digest(1), "b", two)
	now = now.Add(24 * time.Hour)
	if _, ok := c.Get("v", "manifest", digest(1), "b"); ok {
		t.Fatal("expired entry survived")
	}
	c.Drop("other", "blob", digest(2))
	c.Drop("absent", "blob", digest(2))
	if len(c.entries) != 0 || len(c.revisions) != 0 || len(c.counts) != 0 {
		t.Fatal("empty cache retained revision metadata")
	}
	var absent *MemberLocations
	absent.Put("v", "blob", digest(1), "a", one)
	absent.Drop("v", "blob", digest(1))
	if _, ok := absent.Get("v", "blob", digest(1), "a"); ok {
		t.Fatal("nil cache hit")
	}
}

func TestUnitDockerMemberLocationsConcurrent(t *testing.T) {
	c := NewMemberLocations()
	var workers sync.WaitGroup
	for n := range 8 {
		workers.Go(func() {
			digest := fmt.Sprintf("sha256:%064x", n)
			for range 20 {
				c.Put("v", "blob", digest, "a", MemberLocation{Key: "remote", Upstream: true})
				c.Get("v", "blob", digest, "a")
				c.Drop("v", "blob", digest)
			}
		})
	}
	workers.Wait()
}
