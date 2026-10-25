package server

import (
	"context"
	"testing"
	"time"

	"github.com/weaveplatform/artifactweave/internal/auth"
	"github.com/weaveplatform/artifactweave/pkg/repo"
	"github.com/weaveplatform/artifactweave/pkg/store/inmem"
	"github.com/weaveplatform/artifactweave/pkg/types"
)

func TestAccClaimDebianSnapshotBindsSourceAndDistribution(t *testing.T) {
	t.Parallel()
	ctx := context.Background()
	st := inmem.New()
	job := debianSnapshotRequest{
		Source: "source", Target: "target", Tag: "tag", Distribution: "stable",
	}
	p := auth.Principal{Name: "admin", Admin: true}
	if err := claimDebianSnapshot(ctx, st, job, p); err != nil {
		t.Fatal(err)
	}
	if err := claimDebianSnapshot(ctx, st, job, p); err != nil {
		t.Fatalf("same pending request should be retryable: %v", err)
	}
	otherDistribution := job
	otherDistribution.Distribution = "testing"
	if err := claimDebianSnapshot(ctx, st, otherDistribution, p); err == nil {
		t.Fatal("a different distribution claimed an existing tag")
	}
	otherSource := job
	otherSource.Source = "other"
	if err := claimDebianSnapshot(ctx, st, otherSource, p); err == nil {
		t.Fatal("a different source claimed an existing tag")
	}
	marker, err := st.Items().Get(ctx, job.Target, false, snapshotRoot(job))
	if err != nil {
		t.Fatal(err)
	}
	props, err := st.Items().Properties(ctx, marker.ID)
	if err != nil {
		t.Fatal(err)
	}
	if got := props["deb.snapshot.pending.source"]; len(got) != 1 || got[0] != job.Source {
		t.Fatalf("source claim changed: %v", got)
	}
	if got := props["deb.snapshot.pending.distribution"]; len(got) != 1 ||
		got[0] != job.Distribution {
		t.Fatalf("distribution claim changed: %v", got)
	}
}

func TestUnitDebianSnapshotRequiresValidRepositoriesAndPermissions(t *testing.T) {
	ctx := t.Context()
	job := debianSnapshotRequest{
		Source: "deb-source", Target: "deb-target", Tag: "first", Distribution: "stable",
	}
	for _, tc := range []struct {
		name   string
		source repo.Config
		target repo.Config
		user   string
		valid  bool
	}{
		{name: "missing source", target: repo.Config{Key: job.Target, Class: repo.Local, PackageType: repo.Debian}},
		{name: "missing target", source: repo.Config{Key: job.Source, Class: repo.Local, PackageType: repo.Debian}},
		{name: "wrong source format", source: repo.Config{Key: job.Source, Class: repo.Local, PackageType: repo.Generic}, target: repo.Config{Key: job.Target, Class: repo.Local, PackageType: repo.Debian}},
		{name: "wrong target format", source: repo.Config{Key: job.Source, Class: repo.Local, PackageType: repo.Debian}, target: repo.Config{Key: job.Target, Class: repo.Local, PackageType: repo.Generic}},
		{name: "nonlocal target", source: repo.Config{Key: job.Source, Class: repo.Local, PackageType: repo.Debian}, target: repo.Config{Key: job.Target, Class: repo.Virtual, PackageType: repo.Debian}},
		{name: "permission denied", source: repo.Config{Key: job.Source, Class: repo.Local, PackageType: repo.Debian}, target: repo.Config{Key: job.Target, Class: repo.Local, PackageType: repo.Debian}, user: "reader"},
		{name: "valid source and target", source: repo.Config{Key: job.Source, Class: repo.Local, PackageType: repo.Debian}, target: repo.Config{Key: job.Target, Class: repo.Local, PackageType: repo.Debian}, valid: true},
	} {
		t.Run(tc.name, func(t *testing.T) {
			st := inmem.New()
			for _, c := range []repo.Config{tc.source, tc.target} {
				if c.Key != "" {
					if err := st.Repos().Create(ctx, *repo.Defaults(&c)); err != nil {
						t.Fatal(err)
					}
				}
			}
			p := auth.Principal{Name: "admin", Admin: true}
			if tc.user != "" {
				p = auth.Principal{Name: tc.user}
			}
			acc, err := accessFor(ctx, st, p)
			if err != nil {
				t.Fatal(err)
			}
			_, _, err = snapshotRepos(ctx, st, job, p, acc)
			if (err == nil) != tc.valid {
				t.Fatalf("snapshot repository validation: valid=%t, err=%v", tc.valid, err)
			}
		})
	}
}

func TestUnitDebianSnapshotPreservesCompletedAndClaimedTags(t *testing.T) {
	for _, tc := range []struct {
		name, source, distribution string
		complete                   bool
		wantErr                    bool
	}{
		{name: "completed retry", source: "deb-source", distribution: "stable", complete: true},
		{name: "completed tag belongs to another source", source: "other-source", distribution: "stable", complete: true, wantErr: true},
		{name: "pending tag belongs to another distribution", source: "deb-source", distribution: "testing", wantErr: true},
		{name: "pending tag with absent Release", source: "deb-source", distribution: "stable", wantErr: true},
	} {
		t.Run(tc.name, func(t *testing.T) {
			ctx := t.Context()
			st := inmem.New()
			for _, key := range []string{"deb-source", "other-source", "deb-target"} {
				if err := st.Repos().Create(ctx, *repo.Defaults(&repo.Config{
					Key: key, Class: repo.Local, PackageType: repo.Debian,
				})); err != nil {
					t.Fatal(err)
				}
			}
			marker, err := st.Items().Put(ctx, types.Item{
				RepoKey: "deb-target", Name: "snapshots/first", Folder: true,
				Created: time.Now().UTC(), CreatedBy: "admin",
			})
			if err != nil {
				t.Fatal(err)
			}
			props := types.Properties{
				"deb.snapshot.pending.source":       {"deb-source"},
				"deb.snapshot.pending.distribution": {"stable"},
			}
			if tc.complete {
				props = types.Properties{
					"deb.snapshot.source":       {"deb-source"},
					"deb.snapshot.distribution": {"stable"},
				}
			}
			if err := st.Items().
				SetProperties(ctx, marker.ID, props, time.Now().UTC()); err != nil {
				t.Fatal(err)
			}
			p := auth.Principal{Name: "admin", Admin: true}
			acc, err := accessFor(ctx, st, p)
			if err != nil {
				t.Fatal(err)
			}
			job := debianSnapshotRequest{
				Source:       tc.source,
				Target:       "deb-target",
				Tag:          "first",
				Distribution: tc.distribution,
			}
			err = (artifactoryService{cfg: Config{Store: st}}).createDebianSnapshot(
				ctx, st, job, p, acc,
			)
			if (err != nil) != tc.wantErr {
				t.Fatalf("snapshot tag authority: err=%v, wantErr=%t", err, tc.wantErr)
			}
		})
	}
}
