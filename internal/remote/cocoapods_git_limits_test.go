package remote

import (
	"bytes"
	"context"
	"errors"
	"fmt"
	"net/http"
	"sort"
	"strings"
	"testing"

	"github.com/go-git/go-git/v5/plumbing"
	"github.com/go-git/go-git/v5/plumbing/filemode"
	"github.com/go-git/go-git/v5/plumbing/object"
	"github.com/go-git/go-git/v5/storage/memory"

	"github.com/weaveplatform/artifactweave/internal/fakeupstream"
	"github.com/weaveplatform/artifactweave/internal/source"
	"github.com/weaveplatform/artifactweave/pkg/store"
)

func TestAccCocoaPodsGitFailureBoundaries(t *testing.T) {
	for _, name := range []string{"missing branch", "pool shutdown", "redirect"} {
		t.Run(name, func(t *testing.T) {
			w, q := cocoaPodsAuthorityFixture(t)
			switch name {
			case "missing branch":
				q.Resource.CocoaPodsGit.Branch = "absent"
				q.Resource.Variant = q.Resource.CocoaPodsGit.Variant()
			case "pool shutdown":
				w.p.Pool.Close()
			case "redirect":
				w.up.Set(
					"/main.git/info/refs",
					fakeupstream.Resource{Redirect: w.up.URL + "/moved"},
				)
				w.up.Set("/moved", fakeupstream.Resource{Body: []byte("must not be requested")})
			}
			result, err := w.read(q)
			if err == nil || result.Stream != nil {
				t.Fatalf("invalid fetch returned result=%+v, error=%v", result, err)
			}
			if name == "missing branch" && !errors.Is(err, store.ErrNotFound) {
				t.Fatalf("missing selector lost not-found semantics: %v", err)
			}
			if name == "redirect" {
				var failure *source.HTTPError
				if !errors.As(err, &failure) || failure.Status != http.StatusBadGateway {
					t.Fatalf("unsafe upstream redirect did not fail: %v", err)
				}
			}
			if w.up.Count(http.MethodPost, "") != 0 || w.up.Count("", "/moved") != 0 ||
				name == "pool shutdown" && w.up.Count("", "") != 0 {
				t.Fatalf("failed fetch made an unauthorized request: %+v", w.up.Requests())
			}
			cocoaPodsAuthorityNoPublication(t, w, q)
		})
	}
}

func TestAccCocoaPodsGitSubmoduleResourceLimits(t *testing.T) {
	for _, name := range []string{"depth", "repository count"} {
		t.Run(name, func(t *testing.T) {
			w, q := cocoaPodsAuthorityFixture(t)
			s := memory.NewStorage()
			blob := cocoaPodsObject(t, s, plumbing.BlobObject, []byte("source"))
			tree := cocoaPodsTree(
				t,
				s,
				object.TreeEntry{Name: "Source.m", Mode: filemode.Regular, Hash: blob},
			)
			want := cocoaPodsFailureCommit(t, s, tree.Hash)
			wrap := func(children int) {
				var entries []object.TreeEntry
				var modules strings.Builder
				for i := range children {
					path := fmt.Sprintf("Vendor%d", i)
					fmt.Fprintf(
						&modules,
						"[submodule %q]\npath = %s\nurl = ../dep.git\n",
						path,
						path,
					)
					entries = append(
						entries,
						object.TreeEntry{Name: path, Mode: filemode.Submodule, Hash: want},
					)
				}
				config := cocoaPodsObject(t, s, plumbing.BlobObject, []byte(modules.String()))
				entries = append(
					entries,
					object.TreeEntry{Name: ".gitmodules", Mode: filemode.Regular, Hash: config},
				)
				sort.Sort(object.TreeEntrySorter(entries))
				want = cocoaPodsFailureCommit(t, s, cocoaPodsTree(t, s, entries...).Hash)
			}
			childFetches := 8
			if name == "depth" {
				for range 9 {
					wrap(1)
				}
			} else {
				wrap(16) // The root plus sixteen children exceeds the total limit.
				childFetches = 15
			}
			cocoaPodsFailureUpstream(t, w.up, s, want, "ofs-delta", "main", "dep")
			result, err := w.read(q)
			var failure *source.HTTPError
			if !errors.As(err, &failure) || failure.Status != http.StatusBadGateway ||
				result.Stream != nil {
				t.Fatalf("unbounded submodule graph returned result=%+v, error=%v", result, err)
			}
			if got := w.up.Count(http.MethodPost, "/dep.git/git-upload-pack"); got != childFetches {
				t.Fatalf("resource limit allowed %d child fetches, want %d", got, childFetches)
			}
			cocoaPodsAuthorityNoPublication(t, w, q)
		})
	}
}

func TestUnitCocoaPodsPackReadersStopAfterCancellation(t *testing.T) {
	ctx, cancel := context.WithCancel(t.Context())
	defer cancel()
	reader := cocoaPodsPackReader{ctx: ctx, Reader: bytes.NewReader([]byte("PACKcontents"))}
	var header [4]byte
	if _, err := reader.Read(header[:]); err != nil || string(header[:]) != "PACK" {
		t.Fatalf("initial read: %q, %v", header, err)
	}
	delta := cocoaPodsDeltaHeader{ctx: ctx, delta: true}
	if _, err := delta.Write([]byte{5, 6}); err != nil {
		t.Fatal(err)
	}
	cancel()
	if n, err := reader.Read(
		header[:],
	); n != 0 || !errors.Is(err, context.Canceled) ||
		reader.Len() != 8 {
		t.Fatalf(
			"canceled pack consumed more bytes: n=%d err=%v remaining=%d",
			n,
			err,
			reader.Len(),
		)
	}
	if n, err := delta.Write(
		[]byte{7},
	); n != 0 || !errors.Is(err, context.Canceled) ||
		delta.n != 2 {
		t.Fatalf("canceled delta accepted bytes: n=%d err=%v captured=%d", n, err, delta.n)
	}
	if err := cocoaPodsPackError(ctx, source.ErrInvalidResult); !errors.Is(err, context.Canceled) {
		t.Fatalf("canceled parse was misclassified as corrupt content: %v", err)
	}
}
