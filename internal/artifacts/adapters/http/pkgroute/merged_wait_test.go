package pkgroute_test

import (
	"fmt"
	"net/http"
	"sync/atomic"
	"testing"
	"time"

	"github.com/go-chi/chi/v5"

	"github.com/weaveplatform/artifactweave/internal/artifacts/adapters/http/pkgroute"
	"github.com/weaveplatform/artifactweave/internal/artifacts/api/repo"
	"github.com/weaveplatform/artifactweave/internal/artifacts/core/virtual"
)

func TestAccMergedWaiterBuildsItsOwnAnswerAfterTimeout(t *testing.T) {
	f := newKitFixture(t, nil)
	o := f.o
	o.Cache = virtual.NewCache(virtual.CacheOptions{})
	p := testFormat(map[repo.Class]pkgroute.Source{repo.Local: pkgroute.Local{}, repo.Virtual: pkgroute.Virtual{}})
	p.MergeWait = 10 * time.Millisecond
	started, release := make(chan struct{}), make(chan struct{})
	var builds atomic.Int32
	p.Routes = func(r chi.Router, k *pkgroute.Kit) {
		r.Get("/*", func(w http.ResponseWriter, r *http.Request) {
			e, err := k.Merged(r, "index.yaml", "index.yaml", func(*http.Request) (virtual.Entry, error) {
				n := builds.Add(1)
				if n == 1 {
					close(started)
					<-release
				}
				return virtual.Entry{Body: fmt.Appendf(nil, "build %d", n)}, nil
			})
			if err != nil {
				k.Error(w, r, err)
				return
			}
			_, _ = w.Write(e.Body)
		})
	}
	f.h = f.router(t, o, p)
	first := make(chan string, 1)
	go func() { first <- f.do("GET", base+"gen-virtual/index.yaml", "reader", "").Body.String() }()
	<-started
	second := make(chan string, 1)
	go func() { second <- f.do("GET", base+"gen-virtual/index.yaml", "reader", "").Body.String() }()
	select {
	case answer := <-second:
		if answer != "build 2" {
			t.Error(answer)
		}
	case <-time.After(time.Second):
		t.Error("waiter did not build its own answer")
	}
	close(release)
	if answer := <-first; answer != "build 1" {
		t.Fatal(answer)
	}
	if builds.Load() != 2 {
		t.Fatal(builds.Load())
	}
}
