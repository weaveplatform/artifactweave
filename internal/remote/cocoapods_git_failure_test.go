package remote

import (
	"bytes"
	"errors"
	"fmt"
	"io"
	"net/http"
	"strings"
	"testing"
	"time"

	"github.com/go-git/go-git/v5/plumbing"
	"github.com/go-git/go-git/v5/plumbing/filemode"
	"github.com/go-git/go-git/v5/plumbing/object"
	"github.com/go-git/go-git/v5/storage/memory"

	"github.com/weaveplatform/artifactweave/internal/fakeupstream"
	"github.com/weaveplatform/artifactweave/internal/source"
)

// These packs have valid framing and object hashes. Their object graph is the
// hostile input, so failures must happen before the shared cache publishes it.
func TestAccCocoaPodsGitRejectsInvalidObjectGraphs(t *testing.T) {
	for _, name := range []string{
		"empty checkout", "missing commit", "missing root tree", "malformed tag",
		"excessive tag chain", "nonregular gitmodules", "oversized gitmodules",
		"too many modules", "invalid relative module URL", "nested path limit",
	} {
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
			switch name {
			case "empty checkout":
				want = cocoaPodsFailureCommit(t, s, cocoaPodsTree(t, s).Hash)
			case "missing commit":
				want = plumbing.NewHash(strings.Repeat("a", 40))
			case "missing root tree":
				want = cocoaPodsFailureCommit(t, s, plumbing.NewHash(strings.Repeat("b", 40)))
			case "malformed tag":
				want = cocoaPodsObject(
					t,
					s,
					plumbing.TagObject,
					[]byte("tag release\n\nmissing target"),
				)
			case "excessive tag chain":
				for i := range 8 {
					kind := "tag"
					if i == 0 {
						kind = "commit"
					}
					want = cocoaPodsObject(t, s, plumbing.TagObject, fmt.Appendf(nil,
						"object %s\ntype %s\ntag tag%d\n\nmessage\n", want, kind, i))
				}
			case "nested path limit":
				for range 9 {
					tree = cocoaPodsTree(t, s, object.TreeEntry{
						Name: strings.Repeat("d", 250), Mode: filemode.Dir, Hash: tree.Hash,
					})
				}
				want = cocoaPodsFailureCommit(t, s, tree.Hash)
			default:
				content := "[submodule \"dep\"]\npath = Vendor\nurl = ../dep.git\n"
				mode := filemode.Regular
				switch name {
				case "nonregular gitmodules":
					mode = filemode.Executable
				case "oversized gitmodules":
					content = strings.Repeat("# comment\n", 8000)
				case "too many modules":
					content = ""
					for i := range 17 {
						content += fmt.Sprintf(
							"[submodule \"dep%d\"]\npath = Vendor%d\nurl = ../dep.git\n",
							i,
							i,
						)
					}
				case "invalid relative module URL":
					content = "[submodule \"dep\"]\npath = Vendor\nurl = ../%zz.git\n"
				}
				modules := cocoaPodsObject(t, s, plumbing.BlobObject, []byte(content))
				tree = cocoaPodsTree(t, s,
					object.TreeEntry{Name: ".gitmodules", Mode: mode, Hash: modules},
					object.TreeEntry{Name: "Source.m", Mode: filemode.Regular, Hash: blob},
				)
				want = cocoaPodsFailureCommit(t, s, tree.Hash)
			}
			cocoaPodsFailureUpstream(t, w.up, s, want, "ofs-delta")
			result, err := w.read(q)
			var failure *source.HTTPError
			if !errors.As(err, &failure) || failure.Status != http.StatusBadGateway ||
				failure.Kind != source.FailureKindProtocol || result.Stream != nil {
				t.Fatalf("invalid graph returned result=%+v, error=%v", result, err)
			}
			cocoaPodsAuthorityNoPublication(t, w, q)
			// A failed graph must not poison the same descriptor's later repair.
			cocoaPodsAuthorityUpstream(t, w.up, "", nil)
			w.clock.Advance(time.Hour)
			result, err = w.read(q)
			if err != nil || len(w.bytes(result)) == 0 {
				t.Fatalf("repaired Git source was not readable: %v", err)
			}
		})
	}
}

func TestAccCocoaPodsGitUpstreamFailuresDoNotPublish(t *testing.T) {
	for _, tc := range []struct {
		name, path string
		resource   fakeupstream.Resource
	}{
		{"malformed references", "/main.git/info/refs", fakeupstream.Resource{Body: []byte("invalid pkt-line")}},
		{"truncated references", "/main.git/info/refs", fakeupstream.Resource{
			Body: []byte(strings.Repeat("x", 1024)), Fault: fakeupstream.Fault{TruncateAfter: 20},
		}},
		{"oversized references", "/main.git/info/refs", fakeupstream.Resource{Body: bytes.Repeat([]byte("x"), (8<<20)+1)}},
		{"upload forbidden", "/main.git/git-upload-pack", fakeupstream.Resource{Method: http.MethodPost, Status: http.StatusForbidden}},
		{"upload protocol error", "/main.git/git-upload-pack", fakeupstream.Resource{
			Method: http.MethodPost, Status: http.StatusOK, Body: []byte("invalid pkt-line"),
			Header: http.Header{"Content-Type": {"application/x-git-upload-pack-result"}},
		}},
	} {
		t.Run(tc.name, func(t *testing.T) {
			w, q := cocoaPodsAuthorityFixture(t)
			w.up.Set(tc.path, tc.resource)
			result, err := w.read(q)
			if err == nil || result.Stream != nil {
				t.Fatalf("failed exchange returned result=%+v, error=%v", result, err)
			}
			if tc.name == "upload forbidden" {
				var failure *source.HTTPError
				if !errors.As(err, &failure) || failure.Status != http.StatusForbidden {
					t.Fatalf("upstream denial lost its status: %v", err)
				}
			}
			cocoaPodsAuthorityNoPublication(t, w, q)
		})
	}
}

func TestAccCocoaPodsGitShallowNegotiation(t *testing.T) {
	w, q := cocoaPodsAuthorityFixture(t)
	s := memory.NewStorage()
	blob := cocoaPodsObject(t, s, plumbing.BlobObject, []byte("source"))
	tree := cocoaPodsTree(
		t,
		s,
		object.TreeEntry{Name: "Source.m", Mode: filemode.Regular, Hash: blob},
	)
	want := cocoaPodsFailureCommit(t, s, tree.Hash)
	cocoaPodsFailureUpstream(t, w.up, s, want, "shallow ofs-delta no-progress")
	result, err := w.read(q)
	if err != nil || len(w.bytes(result)) == 0 {
		t.Fatalf("shallow-capable upstream failed: %v", err)
	}
	if got := w.up.Count(http.MethodPost, "/main.git/git-upload-pack"); got != 1 {
		t.Fatalf("expected one bounded pack exchange, got %d", got)
	}
}

func cocoaPodsFailureCommit(t *testing.T, s *memory.Storage, tree plumbing.Hash) plumbing.Hash {
	t.Helper()
	return cocoaPodsObject(t, s, plumbing.CommitObject, fmt.Appendf(
		nil,
		"tree %s\nauthor Test <test@example.com> 1 +0000\ncommitter Test <test@example.com> 1 +0000\n\nfixture\n",
		tree,
	))
}

func cocoaPodsFailureUpstream(
	t *testing.T,
	up *fakeupstream.Server,
	s *memory.Storage,
	want plumbing.Hash,
	capabilities string,
	repositories ...string,
) {
	t.Helper()
	packet := func(s string) string { return fmt.Sprintf("%04x%s", len(s)+4, s) }
	advertised := packet("# service=git-upload-pack\n") + "0000" +
		packet(want.String()+" HEAD\x00"+capabilities+"\n") +
		packet(want.String()+" refs/heads/main\n") + "0000"
	var rows []cocoaPodsPackFixture
	for _, item := range s.Objects {
		r, err := item.Reader()
		if err != nil {
			t.Fatal(err)
		}
		data, err := io.ReadAll(r)
		_ = r.Close()
		if err != nil {
			t.Fatal(err)
		}
		rows = append(rows, cocoaPodsPackFixture{typ: byte(item.Type()), data: data})
	}
	prefix := "0008NAK\n"
	if strings.Contains(capabilities, "shallow") {
		prefix = packet("shallow "+want.String()+"\n") + "0000" + prefix
	}
	if len(repositories) == 0 {
		repositories = []string{"main"}
	}
	pack := cocoaPodsPackFixtureBytes(t, rows...)
	for _, name := range repositories {
		up.Set(
			"/"+name+".git/info/refs",
			fakeupstream.Resource{Body: []byte(advertised), Header: http.Header{
				"Content-Type": {"application/x-git-upload-pack-advertisement"},
			}},
		)
		up.Set("/"+name+".git/git-upload-pack", fakeupstream.Resource{
			Method: http.MethodPost, Status: http.StatusOK,
			Body:   append([]byte(prefix), pack...),
			Header: http.Header{"Content-Type": {"application/x-git-upload-pack-result"}},
		})
	}
}
