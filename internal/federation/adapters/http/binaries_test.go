package federationhttp_test

import (
	"context"
	"errors"
	"net/http/httptest"
	"strings"
	"testing"
	"time"

	federationhttp "github.com/weaveplatform/artifactweave/internal/federation/adapters/http"
	"github.com/weaveplatform/artifactweave/internal/federation/core/federated"
	"github.com/weaveplatform/artifactweave/internal/kernel"
	artgen "github.com/weaveplatform/artifactweave/internal/platform/gen/artifactory"
	"github.com/weaveplatform/artifactweave/internal/platform/httpkit/apierror"
)

type binaryService struct {
	err         error
	key, sha    string
	limit, page int
	read        bool
}

func (b *binaryService) List(_ context.Context, key string, limit, page int) ([]federated.FailedBinary, bool, error) {
	b.key, b.limit, b.page = key, limit, page
	return []federated.FailedBinary{{RepoKey: key, SHA1: strings.Repeat("a", 40), Path: "a/b", Created: time.UnixMilli(123), Failures: 2}}, true, b.err
}

func (b *binaryService) Delete(_ context.Context, key, sha string) (int, error) {
	b.key, b.sha = key, sha
	return 2, b.err
}

func (b *binaryService) Replay(_ context.Context, key, sha string) (int, error) {
	b.key, b.sha = key, sha
	return 3, b.err
}

func (b *binaryService) Counts(_ context.Context, key string, read bool) (federated.BinaryCounts, error) {
	b.key, b.read = key, read
	return federated.BinaryCounts{FailedTasks: 4}, b.err
}

func binaryCalls(a federationhttp.FederatedAPI, ctx context.Context) []func() error {
	return []func() error{
		func() error {
			_, e := a.GetListOfFailedBinaryTasks(ctx, artgen.GetListOfFailedBinaryTasksRequestObject{})
			return e
		},
		func() error {
			_, e := a.DeleteFailedBinaryTasks(ctx, artgen.DeleteFailedBinaryTasksRequestObject{})
			return e
		},
		func() error {
			_, e := a.ReplayFailedBinaryTasks(ctx, artgen.ReplayFailedBinaryTasksRequestObject{})
			return e
		},
		func() error {
			_, e := a.GetFederatedBinaryTasks(ctx, artgen.GetFederatedBinaryTasksRequestObject{})
			return e
		},
		func() error {
			_, e := a.GetFederatedRepositoryBinaryState(ctx, artgen.GetFederatedRepositoryBinaryStateRequestObject{})
			return e
		},
	}
}

func TestUnitBinaryAdapterMappingAndPagination(t *testing.T) {
	b := &binaryService{}
	a := federationhttp.FederatedAPI{Binaries: b}
	ctx := t.Context()
	res, err := a.GetListOfFailedBinaryTasks(ctx, artgen.GetListOfFailedBinaryTasksRequestObject{Params: artgen.GetListOfFailedBinaryTasksParams{RepoKey: "fed"}})
	if err != nil {
		t.Fatal(err)
	}
	rr := httptest.NewRecorder()
	if err := res.VisitGetListOfFailedBinaryTasksResponse(rr); err != nil {
		t.Fatal(err)
	}
	if b.key != "fed" || b.limit != 1000 || b.page != 1 || !strings.Contains(rr.Header().Get("Link"), "page=2") || !strings.Contains(rr.Body.String(), `"path":"a/b"`) {
		t.Fatalf("mapping: %+v %s %s", b, rr.Header(), rr.Body.String())
	}
	n, p := 2, 3
	if _, err := a.GetListOfFailedBinaryTasks(
		ctx,
		artgen.GetListOfFailedBinaryTasksRequestObject{Params: artgen.GetListOfFailedBinaryTasksParams{RepoKey: "fed", Limit: &n, Page: &p}},
	); err != nil || b.limit != 2 ||
		b.page != 3 {
		t.Fatalf("paging %+v %v", b, err)
	}
	sha := strings.Repeat("1", 40)
	d, err := a.DeleteFailedBinaryTasks(ctx, artgen.DeleteFailedBinaryTasksRequestObject{Params: artgen.DeleteFailedBinaryTasksParams{RepoKey: "fed", Sha1: &sha}})
	if err != nil || *d.(artgen.DeleteFailedBinaryTasks200JSONResponse).DeletedTasks != 2 || b.sha != sha {
		t.Fatalf("delete %+v %v", d, err)
	}
	r, err := a.ReplayFailedBinaryTasks(ctx, artgen.ReplayFailedBinaryTasksRequestObject{Params: artgen.ReplayFailedBinaryTasksParams{RepoKey: "fed", Sha1: &sha}})
	if err != nil || *r.(artgen.ReplayFailedBinaryTasks200JSONResponse).ReplayTasks != 3 {
		t.Fatalf("replay %+v %v", r, err)
	}
	c, err := a.GetFederatedBinaryTasks(ctx, artgen.GetFederatedBinaryTasksRequestObject{RepoKey: "fed"})
	if err != nil || b.read || c.(artgen.GetFederatedBinaryTasks200JSONResponse).FailedTasks != 4 {
		t.Fatalf("tasks %+v %v", c, err)
	}
	c2, err := a.GetFederatedRepositoryBinaryState(ctx, artgen.GetFederatedRepositoryBinaryStateRequestObject{RepoKey: "fed"})
	if err != nil || !b.read || c2.(artgen.GetFederatedRepositoryBinaryState200JSONResponse).FailedTasks != 4 {
		t.Fatalf("state %+v %v", c2, err)
	}
}

func TestUnitBinaryAdapterRefusalsAndStubs(t *testing.T) {
	for _, call := range binaryCalls(federationhttp.FederatedAPI{}, t.Context()) {
		if err := call(); !errors.Is(err, apierror.ErrNotImplemented) {
			t.Fatal(err)
		}
	}
	for _, tc := range []struct {
		err    error
		status int
	}{{kernel.ErrForbidden, 0}, {kernel.ErrUnauthenticated, 0}, {&federated.RefusalError{Message: "bad digest"}, 400}, {federated.ErrNoRepository, 404}, {federated.ErrNoStreams, 501}, {errors.New("private connection detail"), 500}} {
		for _, call := range binaryCalls(federationhttp.FederatedAPI{Binaries: &binaryService{err: tc.err}}, t.Context()) {
			err := call()
			if tc.status == 0 {
				if !errors.Is(err, tc.err) {
					t.Fatal(err)
				}
			} else {
				requireStatus(t, err, tc.status)
			}
		}
	}
}
