package pkgroute_test

import (
	"bytes"
	"context"
	"errors"
	"fmt"
	"log/slog"
	"net/http"
	"strings"
	"testing"

	"github.com/go-chi/chi/v5"

	"github.com/weaveplatform/artifactweave/internal/access/accesstest"
	"github.com/weaveplatform/artifactweave/internal/artifacts/adapters/http/pkgroute"
	"github.com/weaveplatform/artifactweave/internal/artifacts/api/repo"
	"github.com/weaveplatform/artifactweave/internal/artifacts/core/format"
	"github.com/weaveplatform/artifactweave/internal/artifacts/core/port"
	"github.com/weaveplatform/artifactweave/internal/artifacts/core/remote/source"
	"github.com/weaveplatform/artifactweave/internal/kernel"
	"github.com/weaveplatform/artifactweave/internal/platform/httpkit/apierror"
	"github.com/weaveplatform/artifactweave/internal/platform/httpkit/baseurl"
)

// rootFaultStore fails the read of the repository named failKey.
type rootFaultStore struct {
	pkgroute.Store
	failKey string
}

func (s rootFaultStore) Repos() port.RepoStore {
	return rootFaultRepos{RepoStore: s.Store.Repos(), failKey: s.failKey}
}

type rootFaultRepos struct {
	port.RepoStore
	failKey string
}

func (s rootFaultRepos) Get(ctx context.Context, key string) (repo.Config, error) {
	if key == s.failKey {
		return repo.Config{}, errors.New("repository store unavailable")
	}
	return s.RepoStore.Get(ctx, key) //nolint:wrapcheck // the store's own answer
}

// memberFaults are the errors /warn/* hands WarnPassedOver for each member,
// by the X-Fault header's name.
var memberFaults = map[string]error{
	"unavailable": fmt.Errorf("read: %w", source.ErrUnavailable),
	"timeout":     context.DeadlineExceeded,
	"upstream":    &source.HTTPError{Status: http.StatusServiceUnavailable},
	"answer":      apierror.New(http.StatusBadGateway, "Bad Gateway"),
	"miss":        fmt.Errorf("member: %w", kernel.ErrNotFound),
	"missAnswer":  apierror.New(http.StatusNotFound, "Not Found"),
	"denied":      source.ErrDenied,
	"upstream404": &source.HTTPError{Status: http.StatusNotFound},
	"storage":     fmt.Errorf("blob: %w", format.ErrStorageFault),
	"none":        nil,
}

// rootFixture is walkFixture's repositories and users, served by a format
// whose /root/* answers "ok" when RequireRoot passes, and whose /warn/*
// hands WarnPassedOver each member of the walk with the X-Fault header's
// error, logging to the buffer it returns.
func rootFixture(t *testing.T, tune func(*pkgroute.Options)) (*kitFixture, *bytes.Buffer) {
	t.Helper()
	f, remote := walkFixture(t, nil)
	logs := &bytes.Buffer{}
	o := f.o
	o.Logger = slog.New(slog.NewTextHandler(logs, nil))
	if tune != nil {
		tune(&o)
	}
	sources := map[repo.Class]pkgroute.Source{
		repo.Local: pkgroute.Local{}, repo.Federated: pkgroute.Local{},
		repo.Remote: remote, repo.Virtual: pkgroute.Virtual{},
	}
	p := walkFormat(sources)
	routes := p.Routes
	p.Routes = func(r chi.Router, k *pkgroute.Kit) {
		routes(r, k)
		r.Get("/root/*", func(w http.ResponseWriter, r *http.Request) {
			if k.RequireRoot(w, r) {
				_, _ = w.Write([]byte("ok"))
			}
		})
		r.Get("/warn/*", func(w http.ResponseWriter, r *http.Request) {
			members, err := k.MergeMembers(r, "")
			if err != nil {
				k.Error(w, r, err)
				return
			}
			for _, mr := range members {
				k.WarnPassedOver(mr, memberFaults[r.Header.Get("X-Fault")])
			}
		})
	}
	r := chi.NewRouter()
	r.Use(baseurl.Middleware(""))
	r.Use(accesstest.Over(t, f.db).Middleware(t, true, func(r *http.Request) kernel.Principal {
		name := r.Header.Get("X-Test-User")
		return kernel.Principal{Name: name, Admin: name == "root"}
	}))
	pkgroute.Mount(r, o, p)
	f.h = r
	return f, logs
}

// TestAccRequireRootOfAVirtualRepositoryAsksItsMembers: the root of a
// virtual repository is readable to a caller who may read the root of any
// member, nested ones included, below it or not, and to an administrator;
// a caller who may read no member is refused. A repository that is not
// virtual asks its own root.
func TestAccRequireRootOfAVirtualRepositoryAsksItsMembers(t *testing.T) {
	f, _ := rootFixture(t, nil)
	for _, tc := range []struct {
		path, user string
		status     int
	}{
		{"outer/root/x", "merger", http.StatusOK},
		{"outer/root/x", "partial", http.StatusOK},
		{"outer/root/x", "txt", http.StatusOK},
		{"inner/root/x", "partial", http.StatusOK},
		{"outer/root/x", "root", http.StatusOK},
		{"outer/root/x", "nobody", http.StatusForbidden},
		{"local-a/root/x", "partial", http.StatusOK},
		{"local-b/root/x", "partial", http.StatusForbidden},
	} {
		if rec := f.do(http.MethodGet, base+tc.path, tc.user, ""); rec.Code != tc.status {
			t.Errorf("GET %s as %s: %d %s, want %d", tc.path, tc.user, rec.Code, rec.Body, tc.status)
		}
	}

	failing, _ := rootFixture(t, func(o *pkgroute.Options) {
		o.Store = rootFaultStore{Store: o.Store, failKey: "local-b"}
	})
	if rec := failing.do(http.MethodGet, base+"outer/root/x", "partial", ""); rec.Code !=
		http.StatusInternalServerError {
		t.Fatalf("member lookup fault: %d %s", rec.Code, rec.Body)
	}
	if rec := failing.do(http.MethodGet, base+"outer/root/x", "root", ""); rec.Code != http.StatusOK {
		t.Fatalf("administrator past a member lookup fault: %d %s", rec.Code, rec.Body)
	}
}

// TestAccWarnPassedOverLogsMemberFaultsOnly: a member the walk passes over
// for a fault (an upstream's 5xx, one it cannot reach or that timed out, or
// the 5xx SourceError answers for one) is logged as a warning naming the
// member and the virtual repository; a miss, a refusal and an error the walk
// does not pass over are not.
func TestAccWarnPassedOverLogsMemberFaultsOnly(t *testing.T) {
	f, logs := rootFixture(t, nil)
	for name := range memberFaults {
		logs.Reset()
		if rec := f.do(http.MethodGet, base+"outer/warn/x", "root", "", "X-Fault", name); rec.Code !=
			http.StatusOK {
			t.Fatalf("%s: %d %s", name, rec.Code, rec.Body)
		}
		got := logs.String()
		switch name {
		case "unavailable", "timeout", "upstream", "answer":
			for _, want := range []string{
				"level=WARN", "member=local-a", "member=local-b", "member=remote-a", "virtual=outer",
			} {
				if !strings.Contains(got, want) {
					t.Errorf("%s: log %q lacks %q", name, got, want)
				}
			}
		default:
			if got != "" {
				t.Errorf("%s: logged %q", name, got)
			}
		}
	}
}
