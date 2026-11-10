package vcs_test

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/http"
	"net/url"
	"os"
	"strings"
	"testing"

	"github.com/weaveplatform/artifactweave/internal/artifacts/api/repo"
	"github.com/weaveplatform/artifactweave/internal/artifacts/core/remote/source"
	vcshttp "github.com/weaveplatform/artifactweave/internal/artifacts/ecosystem/vcs/http"
	"github.com/weaveplatform/artifactweave/internal/platform/testkit/repopath"
)

const vcsLegacyOID = "81305b86f97f1361e68f51397943eff6dd635520"

func TestAccVCSLegacyResolvesCommitAndRejectsRedirectChanges(t *testing.T) {
	for _, provider := range []repo.VCSGitProvider{repo.Stash, repo.OldStash, repo.ArtifactoryVCS} {
		t.Run(string(provider), func(t *testing.T) { testVCSLegacyProvider(t, provider) })
	}
}

func TestAccVCSPeerBoundsCombinedTagAndBranchCounts(t *testing.T) {
	tags := make([]map[string]any, vcshttp.MaxRefs)
	for i := range tags {
		tags[i] = map[string]any{
			"name":     fmt.Sprintf("v%d", i),
			"commitId": vcsLegacyOID,
			"isBranch": false,
		}
	}
	tagData, err := json.Marshal(tags)
	if err != nil {
		t.Fatal(err)
	}
	branchData := []byte(`[{"name":"main","commitId":"` + vcsLegacyOID + `","isBranch":true}]`)
	read := sourceBackend(
		func(_ context.Context, q source.Request, _ source.Permit) (source.Result, error) {
			data := tagData
			if strings.Contains(q.Resource.Variant, "branches") {
				data = branchData
			}
			return source.Result{
				Stream: &source.Stream{
					Body:           io.NopCloser(bytes.NewReader(data)),
					Status:         http.StatusOK,
					Representation: source.Representation{Size: int64(len(data))},
				},
			}, nil
		},
	)
	w := newItemWorld(
		t,
		false,
		func(c *Config) { c.RemoteSources = &source.Service{Resolve: sourceIdentity, HTTP: read} },
	)
	c := repo.Defaults(
		&repo.Config{
			Key:            "vcs-peer",
			Class:          repo.Remote,
			PackageType:    repo.VCS,
			URL:            "https://peer.example/api/vcs/source",
			VCSGitProvider: repo.ArtifactoryVCS,
		},
	)
	if err := w.db.Repos().Create(t.Context(), *c); err != nil {
		t.Fatal(err)
	}
	res := w.do(http.MethodGet, "/artifactory/api/vcs/tags/vcs-peer/team/project", "reader", "")
	wantStatus(t, res, http.StatusBadGateway)
}

func testVCSLegacyProvider(t *testing.T, provider repo.VCSGitProvider) {
	t.Helper()
	baseURL := "https://stash.example/context"
	if provider == repo.ArtifactoryVCS {
		baseURL += "/api/vcs/peer"
	}
	var calls []source.Request
	read := sourceBackend(
		func(ctx context.Context, q source.Request, _ source.Permit) (source.Result, error) {
			calls = append(calls, q)
			want := "https://stash.example/context/scm/team/project.git/info/refs?service=git-upload-pack"
			if provider == repo.ArtifactoryVCS {
				action := "tags"
				if strings.Contains(q.Resource.Variant, "branches") {
					action = "branches"
				}
				want = "https://stash.example/context/api/vcs/" + action + "/peer/team/project"
			}
			if q.Resource.Kind == source.ResourceKindArtifact {
				want = "https://stash.example/context/rest/api/1.0/projects/team/repos/project/archive?at=" + vcsLegacyOID + "&format=tar.gz"
				if provider == repo.OldStash {
					want = "https://stash.example/context/plugins/servlet/archive/projects/team/repos/project?at=" + vcsLegacyOID + "&format=tar.gz"
				}
				if provider == repo.ArtifactoryVCS {
					want = "https://stash.example/context/api/vcs/downloadCommit/peer/team/project/" + vcsLegacyOID + "?ext=tar.gz"
				}
				if provider == repo.ArtifactoryVCS && q.Resource.Mutable {
					want = "https://stash.example/context/api/vcs/downloadTagFile/peer/team/project/v1.0.0%21README.md?ext=zip"
					if q.Resource.Path != "team/project/refs/tags/v1.0.0/README.md" ||
						q.Resource.Variant != "vcs-peer-file-v1" {
						t.Fatal("peer selected file lost its mutable reference cache identity")
					}
				}
				if !q.Resource.NoCrossOriginAuth {
					t.Fatal("Stash archive permits cross-origin credentials")
				}
			}
			if q.Resource.ExternalURL != want {
				t.Fatalf("unexpected provider request %s; want %s", q.Resource.ExternalURL, want)
			}
			target, err := url.Parse(want)
			if err != nil || q.Resource.CheckURL(ctx, target) != nil {
				t.Fatalf("provider rejected its request: %v", err)
			}
			for _, change := range []func(*url.URL){
				func(u *url.URL) { u.Host = "other.example" },
				func(u *url.URL) { u.Path += "/other" },
				func(u *url.URL) { u.RawQuery += "&at=other" },
				func(u *url.URL) { u.Scheme = "http" },
				func(u *url.URL) { u.User = url.UserPassword("user", "password") },
				func(u *url.URL) { u.Fragment = "fragment" },
			} {
				next := *target
				change(&next)
				if !errors.Is(q.Resource.CheckURL(ctx, &next), source.ErrDenied) {
					t.Fatalf("provider accepted changed redirect %s", next.String())
				}
			}
			if provider == repo.ArtifactoryVCS && q.Resource.Kind == source.ResourceKindVersions {
				branch := strings.Contains(q.Resource.Variant, "branches")
				name := "v1.0.0"
				if branch {
					name = "main"
				}
				data, _ := json.Marshal(
					[]map[string]any{{"name": name, "commitId": vcsLegacyOID, "isBranch": branch}},
				)
				if err := q.Resource.ValidateMetadata(
					ctx,
					bytes.NewReader(data),
					int64(len(data)),
					source.MetadataResponse{},
				); err != nil {
					t.Fatal(err)
				}
				return source.Result{
					Stream: &source.Stream{
						Body:           io.NopCloser(bytes.NewReader(data)),
						Status:         http.StatusOK,
						Representation: source.Representation{Size: int64(len(data))},
					},
				}, nil
			}
			var data []byte
			if q.Resource.Kind == source.ResourceKindVersions {
				data = bytes.ReplaceAll(vcsTestRefs(), []byte(vcsTestOID), []byte(vcsLegacyOID))
				if err := q.Resource.ValidateMetadata(
					ctx,
					bytes.NewReader(data),
					int64(len(data)),
					source.MetadataResponse{},
				); err != nil {
					t.Fatal(err)
				}
			} else {
				filename := "internal/artifacts/ecosystem/vcs/http/testdata/legacy-vcs-archive.tar.gz"
				if q.Resource.Mutable {
					filename = "internal/artifacts/ecosystem/vcs/http/testdata/legacy-vcs-readme.zip"
				}
				data, err = os.ReadFile(repopath.Path(t, filename))
				if err != nil {
					t.Fatal(err)
				}
				if err := q.Resource.Validate(
					ctx,
					bytes.NewReader(data),
					int64(len(data)),
				); err != nil {
					t.Fatal(err)
				}
			}
			return source.Result{
				Stream: &source.Stream{
					Body:           io.NopCloser(bytes.NewReader(data)),
					Status:         http.StatusOK,
					Representation: source.Representation{Size: int64(len(data))},
				},
			}, nil
		},
	)
	w := newItemWorld(t, false, func(c *Config) {
		c.RemoteSources = &source.Service{Resolve: sourceIdentity, HTTP: read}
	})
	c := repo.Defaults(&repo.Config{
		Key: "vcs-legacy", Class: repo.Remote, PackageType: repo.VCS,
		URL: baseURL, VCSGitProvider: provider,
	})
	if err := w.db.Repos().Create(t.Context(), *c); err != nil {
		t.Fatal(err)
	}
	for _, path := range []string{
		"tags/vcs-legacy/team/project", "branches/vcs-legacy/team/project", "refs/vcs-legacy/team/project",
		"downloadTag/vcs-legacy/team/project/v1.0.0", "downloadBranch/vcs-legacy/team/project/main",
		"downloadCommit/vcs-legacy/team/project/" + vcsLegacyOID,
		"downloadTagFile/vcs-legacy/team/project/v1.0.0!README.md",
	} {
		t.Run(path, func(t *testing.T) {
			res := w.do(http.MethodGet, "/artifactory/api/vcs/"+path, "reader", "")
			wantStatus(t, res, http.StatusOK)
		})
	}
	before := len(calls)
	for _, tc := range []struct {
		name, path, user string
		status           int
	}{
		{"denied read", "tags/vcs-legacy/team/project", "user", http.StatusForbidden},
		{"nested owner", "tags/vcs-legacy/team/sub/project", "reader", http.StatusNotFound},
		{"invalid ref", "downloadBranch/vcs-legacy/team/project/%2e%2e", "reader", http.StatusBadRequest},
		{"invalid file", "downloadTagFile/vcs-legacy/team/project/v1.0.0!../secret", "reader", http.StatusBadRequest},
	} {
		t.Run(tc.name, func(t *testing.T) {
			res := w.do(http.MethodGet, "/artifactory/api/vcs/"+tc.path, tc.user, "")
			wantStatus(t, res, tc.status)
			if strings.Contains(res.Body.String(), "stash.example") {
				t.Fatal("denial leaked provider URL")
			}
		})
	}
	if len(calls) != before {
		t.Fatal("invalid or unauthorized VCS request reached upstream")
	}
}
