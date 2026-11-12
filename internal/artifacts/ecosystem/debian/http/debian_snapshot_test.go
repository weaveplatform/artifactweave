package debianhttp

import (
	"context"
	"encoding/json"
	"errors"
	"net/http"
	"testing"
	"time"

	"github.com/weaveplatform/artifactweave/internal/access/accesstest"
	"github.com/weaveplatform/artifactweave/internal/access/api"
	"github.com/weaveplatform/artifactweave/internal/app/adapters/inmem"
	"github.com/weaveplatform/artifactweave/internal/artifacts/adapters/http/itemkit"
	"github.com/weaveplatform/artifactweave/internal/artifacts/api/repo"
	artmodel "github.com/weaveplatform/artifactweave/internal/artifacts/core/model"
	"github.com/weaveplatform/artifactweave/internal/kernel"
	artgen "github.com/weaveplatform/artifactweave/internal/platform/gen/artifactory"
	"github.com/weaveplatform/artifactweave/internal/platform/work"
)

func TestAccClaimDebianSnapshotBindsSourceAndDistribution(t *testing.T) {
	t.Parallel()
	ctx := context.Background()
	st := inmem.New()
	job := debianSnapshotRequest{
		Source: "source", Target: "target", Tag: "tag", Distribution: "stable",
	}
	p := kernel.Principal{Name: "admin", Admin: true}
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
			p := kernel.Principal{Name: "admin", Admin: true}
			if tc.user != "" {
				p = kernel.Principal{Name: tc.user}
			}
			acc, err := itemkit.AccessFor(ctx, authorizerOver(t, st), p, nil)
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
		expectedErr                bool
	}{
		{name: "completed retry", source: "deb-source", distribution: "stable", complete: true},
		{name: "completed tag belongs to another source", source: "other-source", distribution: "stable", complete: true, expectedErr: true},
		{name: "pending tag belongs to another distribution", source: "deb-source", distribution: "testing", expectedErr: true},
		{name: "pending tag with absent Release", source: "deb-source", distribution: "stable", expectedErr: true},
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
			marker, err := st.Items().Put(ctx, artmodel.Item{
				RepoKey: "deb-target", Name: "snapshots/first", Folder: true,
				Created: time.Now().UTC(), CreatedBy: "admin",
			})
			if err != nil {
				t.Fatal(err)
			}
			props := artmodel.Properties{
				"deb.snapshot.pending.source":       {"deb-source"},
				"deb.snapshot.pending.distribution": {"stable"},
			}
			if tc.complete {
				props = artmodel.Properties{
					"deb.snapshot.source":       {"deb-source"},
					"deb.snapshot.distribution": {"stable"},
				}
			}
			if err := st.Items().
				SetProperties(ctx, marker.ID, props, time.Now().UTC()); err != nil {
				t.Fatal(err)
			}
			p := kernel.Principal{Name: "admin", Admin: true}
			acc, err := itemkit.AccessFor(ctx, authorizerOver(t, st), p, nil)
			if err != nil {
				t.Fatal(err)
			}
			job := debianSnapshotRequest{
				Source:       tc.source,
				Target:       "deb-target",
				Tag:          "first",
				Distribution: tc.distribution,
			}
			err = DebianAPI{newKit(Config{Store: st})}.createDebianSnapshot(
				ctx, st, job, p, acc,
			)
			if (err != nil) != tc.expectedErr {
				t.Fatalf("snapshot tag authority: err=%v, wantErr=%t", err, tc.expectedErr)
			}
		})
	}
}

// The queued snapshot rechecks its caller's credential and runs with the
// decider that Authorize returns for the caller as it stands (decision
// 0033): a caller granted Read on the source and Write and Manage on the
// target gets through to the snapshot, which an earlier attempt completed,
// and a caller without that grant is refused.
func TestUnitDebianSnapshotWorkUsesCurrentPermissions(t *testing.T) {
	ctx := t.Context()
	st := inmem.New()
	cheapSeed(t, st)
	for _, key := range []string{"deb-source", "deb-target"} {
		if err := st.Repos().Create(ctx, *repo.Defaults(&repo.Config{
			Key: key, Class: repo.Local, PackageType: repo.Debian,
		})); err != nil {
			t.Fatal(err)
		}
	}
	for _, name := range []string{"dev", "other"} {
		if err := st.Users().
			Create(ctx, accesstest.User{Name: name, PasswordHash: cheapHash}); err != nil {
			t.Fatal(err)
		}
	}
	if err := st.Permissions().Create(ctx, accesstest.Permission{
		Name: "snapshots",
		Resources: map[api.ResourceType]accesstest.Resource{api.ResourceArtifact: {
			Actions: accesstest.Grants{Users: map[string]api.Actions{
				"dev": api.ActionRead | api.ActionWrite | api.ActionManage,
			}},
			Targets: map[string]accesstest.Target{
				"deb-source": {Include: []string{"**"}},
				"deb-target": {Include: []string{"**"}},
			},
		}},
	}); err != nil {
		t.Fatal(err)
	}
	marker, err := st.Items().Put(ctx, artmodel.Item{
		RepoKey: "deb-target", Name: "snapshots/first", Folder: true,
		Created: time.Now().UTC(), CreatedBy: "dev",
	})
	if err != nil {
		t.Fatal(err)
	}
	if err := st.Items().SetProperties(ctx, marker.ID, artmodel.Properties{
		"deb.snapshot.source":       {"deb-source"},
		"deb.snapshot.distribution": {"stable"},
	}, time.Now().UTC()); err != nil {
		t.Fatal(err)
	}
	accessAPI := accesstest.Over(t, st).API()
	a := accessAPI.Authenticator
	q, err := work.New(st, work.Options{})
	if err != nil {
		t.Fatal(err)
	}
	if err := RegisterDebianSnapshotWork(
		q,
		newKit(Config{Store: st, Access: accessAPI}),
	); err != nil {
		t.Fatal(err)
	}
	run := func(user string) error {
		t.Helper()
		p, err := a.Basic(ctx, user, "pw")
		if err != nil {
			t.Fatal(err)
		}
		payload, err := json.Marshal(debianSnapshotRequest{
			Source: "deb-source", Target: "deb-target", Tag: "first", Distribution: "stable",
			Credential: p.CredentialReference(),
		})
		if err != nil {
			t.Fatal(err)
		}
		const key = "deb-target/first"
		if err := q.Enqueue(ctx, debianSnapshotKind, key, payload); err != nil {
			t.Fatal(err)
		}
		return q.EnsureFresh(ctx, debianSnapshotKind, key)
	}
	if err := run("dev"); err != nil {
		t.Fatalf("a caller with the grant: %v", err)
	}
	if err := run("other"); !errors.Is(err, kernel.ErrForbidden) {
		t.Fatalf("a caller without the grant: %v", err)
	}
}

// Without an authenticator a queued snapshot cannot recheck its caller's
// credential, so the job is refused for good rather than retried.
func TestUnitDebianSnapshotWorkWithoutAuthenticator(t *testing.T) {
	ctx := t.Context()
	st := inmem.New()
	q, err := work.New(st, work.Options{})
	if err != nil {
		t.Fatal(err)
	}
	if err := RegisterDebianSnapshotWork(
		q,
		newKit(withAccess(t, Config{Store: st})),
	); err != nil {
		t.Fatal(err)
	}
	payload, err := json.Marshal(debianSnapshotRequest{
		Source: "deb-source", Target: "deb-target", Tag: "first", Distribution: "stable",
		Credential: kernel.Principal{Name: "dev", Password: true}.CredentialReference(),
	})
	if err != nil {
		t.Fatal(err)
	}
	const key = "deb-target/first"
	if err := q.Enqueue(ctx, debianSnapshotKind, key, payload); err != nil {
		t.Fatal(err)
	}
	err = q.EnsureFresh(ctx, debianSnapshotKind, key)
	if !errors.Is(err, work.ErrPermanent) ||
		err.Error() != "work: permanent refusal: debian snapshot authentication unavailable" {
		t.Fatalf("a snapshot without an authenticator: %v", err)
	}
}

// TestUnitDebianSnapshotWorkAnswersTheSourcesPolicy: a snapshot run through
// the work queue, as the server runs one, hands the source's policy refusal
// back through EnsureFresh unchanged, so the request answers the policy's
// 404 and message naming the source's Release.
func TestUnitDebianSnapshotWorkAnswersTheSourcesPolicy(t *testing.T) {
	ctx := t.Context()
	st := inmem.New()
	cheapSeed(t, st)
	for _, c := range []repo.Config{
		{Key: "deb-source", Class: repo.Local, PackageType: repo.Debian, BlackedOut: new(true)},
		{Key: "deb-target", Class: repo.Local, PackageType: repo.Debian},
	} {
		if err := st.Repos().Create(ctx, *repo.Defaults(&c)); err != nil {
			t.Fatal(err)
		}
	}
	accessAPI := accesstest.Over(t, st).API()
	q, err := work.New(st, work.Options{})
	if err != nil {
		t.Fatal(err)
	}
	k := itemkit.New(itemkit.Deps{Store: st, Access: accessAPI, Work: q})
	if err := RegisterDebianSnapshotWork(q, k); err != nil {
		t.Fatal(err)
	}
	p, err := accessAPI.Authenticator.Basic(ctx, "admin", "pw")
	if err != nil {
		t.Fatal(err)
	}
	p, d, err := accessAPI.Authorizer.Authorize(ctx, p, p.Anonymous)
	if err != nil {
		t.Fatal(err)
	}
	_, err = DebianAPI{k}.CreateDebianSnapshot(api.WithCaller(ctx, p, d),
		artgen.CreateDebianSnapshotRequestObject{
			RepoKey: "deb-source",
			Body: &artgen.CreateDebianSnapshotJSONRequestBody{
				Tag: "first", TargetRepo: "deb-target", Distribution: "stable",
			},
		})
	wantAPIError(t, err, http.StatusNotFound, "The repository 'deb-source' is blacked out "+
		"and cannot serve artifact 'deb-source:dists/stable/Release'.")
}
