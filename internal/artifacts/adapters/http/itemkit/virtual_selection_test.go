package itemkit

import (
	"context"
	"errors"
	"io"
	"strings"
	"testing"
	"time"

	"github.com/weaveplatform/artifactweave/internal/access/api/accessapitest"
	"github.com/weaveplatform/artifactweave/internal/app/adapters/inmem"
	"github.com/weaveplatform/artifactweave/internal/artifacts/api/repo"
	"github.com/weaveplatform/artifactweave/internal/artifacts/core/format"
	"github.com/weaveplatform/artifactweave/internal/artifacts/core/model"
	"github.com/weaveplatform/artifactweave/internal/artifacts/core/remote/source"
	"github.com/weaveplatform/artifactweave/internal/artifacts/core/virtual"
)

type selectedBody struct {
	io.Reader
	closed bool
}

func (b *selectedBody) Close() error { b.closed = true; return nil }

var errSelectionProbe = errors.New("selection failed")

func TestUnitVirtualSelectionClosesUnusedBodies(t *testing.T) {
	for _, failure := range []string{"", "compare", "lookup", "remote"} {
		t.Run(failure, func(t *testing.T) {
			bodies := []*selectedBody{{Reader: strings.NewReader("one")}, {Reader: strings.NewReader("two")}, {Reader: strings.NewReader("three")}}
			members := []virtual.Member{}
			for _, key := range []string{"one", "two", "three"} {
				members = append(members, virtual.Member{Repo: repo.Config{Key: key, Class: repo.Remote}, Upstream: true})
			}
			dl := newDownload("file", false, model.Matrix{}, nil)
			reads := 0
			used := 0
			dl.upstream = &upstreamStep{read: func(_ context.Context, _ *Download, c repo.Config, _ *virtual.Resolution) (Found, memberAnswer, error) {
				i := reads
				reads++
				if failure == "lookup" && i == 2 {
					return Found{}, passedOver, errSelectionProbe
				}
				read := &remoteRead{
					resource: source.Resource{Path: "file"},
					result: source.Result{
						Stream: &source.Stream{Body: bodies[i], Representation: source.Representation{Modified: time.Unix(int64(i), 0)}},
						Used:   func(context.Context) error { used++; return nil },
					},
				}
				if failure == "remote" && i == 2 {
					read.err = errSelectionProbe
				}
				return Found{Repo: c, remote: read}, exactMatch, nil
			}}
			policy := &format.VirtualSelection{
				Skip: func(repo.Config) []string { return nil },
				Choose: func(_ context.Context, current format.VirtualCandidate, kept *format.VirtualCandidate) (format.VirtualChoice, error) {
					if failure == "compare" && current.Repo.Key == "three" {
						return format.VirtualChoice{}, errSelectionProbe
					}
					return format.VirtualChoice{Take: kept == nil || current.Repo.Key == "two"}, nil
				},
				Returning: func(format.VirtualCandidate) string { return "selected" },
			}
			got, err := lookupSelected(t.Context(), inmem.New(), members, "file", Access{Decider: &accessapitest.Decider{Admin: true}}, dl, policy)
			if failure == "" {
				if err != nil || got.Repo.Key != "two" || !bodies[0].closed || bodies[1].closed || !bodies[2].closed {
					t.Fatal(got.Repo.Key, err, bodies)
				}
				closeCandidate(got)
			} else {
				if !errors.Is(err, errSelectionProbe) || !bodies[0].closed || !bodies[1].closed {
					t.Fatal(err, bodies)
				}
				if failure != "lookup" && !bodies[2].closed {
					t.Fatal("last body leaked")
				}
			}
			if used != 0 {
				t.Fatal("candidate counted before delivery", used)
			}
		})
	}
}
