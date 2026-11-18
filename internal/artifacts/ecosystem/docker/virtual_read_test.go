package docker_test

import (
	"strings"
	"testing"
	"time"

	"github.com/weaveplatform/artifactweave/internal/artifacts/api/repo"
	"github.com/weaveplatform/artifactweave/internal/artifacts/core/model"
	"github.com/weaveplatform/artifactweave/internal/artifacts/core/virtual"
	layout "github.com/weaveplatform/artifactweave/internal/artifacts/ecosystem/docker"
)

func TestUnitLatestVirtualTag(t *testing.T) {
	base := time.Date(2026, 11, 18, 12, 0, 0, 0, time.UTC)
	candidate := func(key string, class repo.Class, modified int) layout.VirtualTagCandidate {
		return layout.VirtualTagCandidate{
			Member: virtual.Member{Repo: repo.Config{Key: key, Class: class}},
			Item:   model.Item{Modified: base.Add(time.Duration(modified) * time.Second)},
		}
	}
	for _, tc := range []struct {
		name, want string
		in         []layout.VirtualTagCandidate
	}{
		{"empty", "", nil},
		{"newest local", "second", []layout.VirtualTagCandidate{candidate("first", repo.Local, 1), candidate("second", repo.Local, 2)}},
		{"federated", "fed", []layout.VirtualTagCandidate{candidate("first", repo.Local, 1), candidate("fed", repo.Federated, 2)}},
		{"effective tie", "priority", []layout.VirtualTagCandidate{candidate("priority", repo.Local, 2), candidate("configured-first", repo.Local, 2)}},
		{"remote cache is not local", "local", []layout.VirtualTagCandidate{candidate("cache", repo.Remote, 9), candidate("local", repo.Local, 1)}},
		{"remote only", "", []layout.VirtualTagCandidate{candidate("cache", repo.Remote, 9)}},
	} {
		t.Run(tc.name, func(t *testing.T) {
			got, ok := layout.LatestVirtualTag(tc.in)
			if ok != (tc.want != "") || got.Member.Repo.Key != tc.want {
				t.Fatal(got, ok)
			}
		})
	}
}

func TestUnitMergeVirtualReferrers(t *testing.T) {
	descriptor := func(hex, kind string) layout.Descriptor {
		return layout.Descriptor{Digest: "sha256:" + strings.Repeat(hex, 64), ArtifactType: kind}
	}
	first, second, duplicate, remote := descriptor("1", "first"), descriptor("2", "second"), descriptor("1", "later"), descriptor("3", "remote")
	got := layout.MergeVirtualReferrers([]layout.VirtualReferrersPart{
		{Member: virtual.Member{Repo: repo.Config{Class: repo.Local}}, Manifests: []layout.Descriptor{first}},
		{Member: virtual.Member{Repo: repo.Config{Class: repo.Remote}}, Manifests: []layout.Descriptor{remote}},
		{Member: virtual.Member{Repo: repo.Config{Class: repo.Federated}}, Manifests: []layout.Descriptor{duplicate, second}},
	})
	if len(got) != 2 || got[0].ArtifactType != "first" || got[1].ArtifactType != "second" {
		t.Fatal(got)
	}
	if empty := layout.MergeVirtualReferrers(nil); empty == nil || len(empty) != 0 {
		t.Fatal("empty index must carry an array", empty)
	}
}
