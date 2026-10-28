package remote

import (
	"archive/tar"
	"bytes"
	"context"
	"errors"
	"fmt"
	"io"
	"net/http"
	"net/http/httptest"
	"net/url"
	"os"
	"strings"
	"testing"

	"github.com/go-git/go-git/v5/plumbing"
	"github.com/go-git/go-git/v5/plumbing/filemode"
	"github.com/go-git/go-git/v5/plumbing/object"
	"github.com/go-git/go-git/v5/plumbing/protocol/packp"
	"github.com/go-git/go-git/v5/plumbing/transport"
	"github.com/go-git/go-git/v5/storage/memory"

	"github.com/weaveplatform/artifactweave/internal/source"
	"github.com/weaveplatform/artifactweave/pkg/repo"
	"github.com/weaveplatform/artifactweave/pkg/store"
)

func TestUnitCocoaPodsGitRevisions(t *testing.T) {
	hash := plumbing.NewHash(strings.Repeat("a", 40))
	tag := plumbing.NewHash(strings.Repeat("b", 40))
	refs := packp.NewAdvRefs()
	refs.Head = &hash
	refs.References = map[string]plumbing.Hash{"refs/heads/main": hash, "refs/tags/release": tag}
	for _, tc := range []struct {
		name string
		spec source.CocoaPodsGitSource
		want plumbing.Hash
	}{
		{"head", source.CocoaPodsGitSource{}, hash},
		{"branch", source.CocoaPodsGitSource{Branch: "main"}, hash},
		{"branch tag fallback", source.CocoaPodsGitSource{Branch: "release"}, tag},
		{"tag", source.CocoaPodsGitSource{Tag: "release"}, tag},
		{"commit precedes selectors", source.CocoaPodsGitSource{Commit: hash.String(), Tag: "release"}, hash},
		{"missing tag", source.CocoaPodsGitSource{Tag: "absent"}, plumbing.ZeroHash},
	} {
		t.Run(tc.name, func(t *testing.T) {
			got, err := cocoaPodsGitWant(tc.spec, refs)
			if got != tc.want || (err != nil) != tc.want.IsZero() {
				t.Fatalf("want=%s err=%v", got, err)
			}
		})
	}
	refs.Head = nil
	if _, err := cocoaPodsGitWant(
		source.CocoaPodsGitSource{},
		refs,
	); !errors.Is(
		err,
		store.ErrNotFound,
	) {
		t.Fatal(err)
	}
}

func cocoaPodsObject(
	t *testing.T,
	s *memory.Storage,
	typ plumbing.ObjectType,
	data []byte,
) plumbing.Hash {
	t.Helper()
	o := s.NewEncodedObject()
	o.SetType(typ)
	o.SetSize(int64(len(data)))
	w, err := o.Writer()
	if err != nil {
		t.Fatal(err)
	}
	if _, err = w.Write(data); err != nil {
		t.Fatal(err)
	}
	if err = w.Close(); err != nil {
		t.Fatal(err)
	}
	hash, err := s.SetEncodedObject(o)
	if err != nil {
		t.Fatal(err)
	}
	return hash
}

func cocoaPodsTree(t *testing.T, s *memory.Storage, entries ...object.TreeEntry) *object.Tree {
	t.Helper()
	tree := &object.Tree{Entries: entries}
	o := s.NewEncodedObject()
	if err := tree.Encode(o); err != nil {
		t.Fatal(err)
	}
	hash, err := s.SetEncodedObject(o)
	if err != nil {
		t.Fatal(err)
	}
	tree, err = object.GetTree(s, hash)
	if err != nil {
		t.Fatal(err)
	}
	return tree
}

func TestUnitCocoaPodsGitTreeSafety(t *testing.T) {
	for _, tc := range []struct {
		name, path string
		mode       filemode.FileMode
		missing    bool
	}{
		{"symlink", "link", filemode.Symlink, false},
		{"git config", ".GiT", filemode.Regular, false},
		{"traversal", "..", filemode.Regular, false},
		{"path separator", "a/b", filemode.Regular, false},
		{"windows drive", "c:evil", filemode.Regular, false},
		{"missing blob", "missing", filemode.Regular, true},
		{"missing tree", "dir", filemode.Dir, true},
		{"unconfigured gitlink", "module", filemode.Submodule, false},
	} {
		t.Run(tc.name, func(t *testing.T) {
			s := memory.NewStorage()
			hash := cocoaPodsObject(t, s, plumbing.BlobObject, []byte("bytes"))
			if tc.missing {
				hash = plumbing.ZeroHash
			}
			tree := cocoaPodsTree(t, s, object.TreeEntry{Name: tc.path, Mode: tc.mode, Hash: hash})
			g := &cocoaPodsCheckout{seen: map[string]bool{}, archive: tar.NewWriter(io.Discard)}
			err := g.tree(
				t.Context(),
				s,
				tree,
				source.CocoaPodsGitSource{Submodules: true},
				"",
				"",
				nil,
				0,
				0,
			)
			if !errors.Is(err, source.ErrInvalidResult) {
				t.Fatalf("unsafe tree accepted: %v", err)
			}
		})
	}
	s := memory.NewStorage()
	hash := cocoaPodsObject(t, s, plumbing.BlobObject, []byte("bytes"))
	entry := object.TreeEntry{Name: "executable", Mode: filemode.Executable, Hash: hash}
	tree := cocoaPodsTree(t, s, entry)
	var buf bytes.Buffer
	tw := tar.NewWriter(&buf)
	g := &cocoaPodsCheckout{seen: map[string]bool{}, archive: tw}
	if err := g.tree(
		t.Context(),
		s,
		tree,
		source.CocoaPodsGitSource{},
		"",
		"",
		nil,
		0,
		0,
	); err != nil {
		t.Fatal(err)
	}
	if err := tw.Close(); err != nil {
		t.Fatal(err)
	}
	h, err := tar.NewReader(&buf).Next()
	if err != nil || h.Mode != 0o755 {
		t.Fatalf("executable header %v %v", h, err)
	}
	if err := g.file(s, entry, "executable"); !errors.Is(err, source.ErrInvalidResult) {
		t.Fatal("duplicate file accepted")
	}
	for _, path := range []string{"", "a\\b", "a\x00b", "a//b", "a/./b", strings.Repeat("x", 2049)} {
		if cocoaPodsGitPath(path) {
			t.Fatalf("unsafe path %q", path)
		}
	}
	ctx, cancel := context.WithCancel(t.Context())
	cancel()
	if err := g.tree(
		ctx,
		s,
		tree,
		source.CocoaPodsGitSource{},
		"",
		"",
		nil,
		0,
		0,
	); !errors.Is(
		err,
		context.Canceled,
	) {
		t.Fatal(err)
	}
	if err := g.tree(
		t.Context(),
		s,
		tree,
		source.CocoaPodsGitSource{},
		"",
		"",
		nil,
		0,
		65,
	); !errors.Is(
		err,
		source.ErrInvalidResult,
	) {
		t.Fatal(err)
	}
	for _, files := range []int{0, cocoaPodsGitObjects} {
		g := &cocoaPodsCheckout{
			files:    files,
			expanded: cocoaPodsGitBytes,
			seen:     map[string]bool{},
			archive:  tar.NewWriter(io.Discard),
		}
		if g.file(s, entry, "x") == nil {
			t.Fatal("aggregate archive budget ignored")
		}
	}
}

func TestUnitCocoaPodsGitModulesAndObjectIdentity(t *testing.T) {
	spec := source.CocoaPodsGitSource{URL: "https://git.example/org/main.git", Submodules: true}
	for _, tc := range []struct {
		name, contents, want string
		invalid              bool
	}{
		{"relative", "[submodule \"dep\"]\npath = Vendor\nurl = ../dep.git\n", "https://git.example/org/dep.git", false},
		{"absolute", "[submodule \"dep\"]\npath = Vendor\nurl = https://other.example/dep.git\n", "https://other.example/dep.git", false},
		{"local", "[submodule \"dep\"]\npath = Vendor\nurl = /etc/passwd\n", "", true},
		{"command", "[submodule \"dep\"]\npath = Vendor\nurl = ext::command\n", "", true},
		{"escape", "[submodule \"dep\"]\npath = ../escape\nurl = ../dep.git\n", "", true},
		{"missing URL", "[submodule \"dep\"]\npath = Vendor\n", "", true},
		{"missing path", "[submodule \"dep\"]\nurl = ../dep.git\n", "", true},
		{"unsafe name", "[submodule \"../dep\"]\npath = Vendor\nurl = ../dep.git\n", "", true},
		{"duplicate path", "[submodule \"first\"]\npath = Vendor\nurl = ../first.git\n[submodule \"second\"]\npath = Vendor\nurl = ../second.git\n", "", true},
		{"syntax", "[bad", "", true},
	} {
		t.Run(tc.name, func(t *testing.T) {
			s := memory.NewStorage()
			hash := cocoaPodsObject(t, s, plumbing.BlobObject, []byte(tc.contents))
			tree := cocoaPodsTree(
				t,
				s,
				object.TreeEntry{Name: ".gitmodules", Mode: filemode.Regular, Hash: hash},
			)
			modules, err := cocoaPodsGitModules(tree, spec)
			if (err != nil) != tc.invalid || !tc.invalid && modules["Vendor"] != tc.want {
				t.Fatalf("modules=%v err=%v", modules, err)
			}
		})
	}
	s := memory.NewStorage()
	empty := cocoaPodsTree(t, s)
	if _, err := cocoaPodsGitModules(empty, spec); err != nil {
		t.Fatal(err)
	}
	spec.Submodules = false
	if _, err := cocoaPodsGitModules(empty, spec); err != nil {
		t.Fatal(err)
	}
	if _, err := cocoaPodsGitCommit(
		s,
		plumbing.ZeroHash,
	); !errors.Is(
		err,
		source.ErrInvalidResult,
	) {
		t.Fatal(err)
	}
	blob := cocoaPodsObject(t, s, plumbing.BlobObject, []byte("not commit"))
	if _, err := cocoaPodsGitCommit(s, blob); !errors.Is(err, source.ErrInvalidResult) {
		t.Fatal(err)
	}
	bad := cocoaPodsObject(t, s, plumbing.CommitObject, []byte("bad"))
	if _, err := cocoaPodsGitCommit(s, bad); !errors.Is(err, source.ErrInvalidResult) {
		t.Fatal(err)
	}
}

func TestAccCocoaPodsGitDestinationAndTransport(t *testing.T) {
	enabled := true
	c := repo.Config{
		URL:                          "https://origin.example",
		ExternalDependenciesEnabled:  &enabled,
		ExternalDependenciesPatterns: []string{"https://git.example/approved/**"},
	}
	if err := cocoaPodsGitAllowed(c, "https://git.example/approved/main.git"); err != nil {
		t.Fatal(err)
	}
	for _, raw := range []string{"http://git.example/approved/main.git", "https://git.example/other/main.git", "file:///etc/passwd"} {
		if cocoaPodsGitAllowed(c, raw) == nil {
			t.Fatal("unsafe destination admitted")
		}
	}
	c.ExternalDependenciesEnabled = nil
	if cocoaPodsGitAllowed(c, "https://git.example/approved/main.git") == nil {
		t.Fatal("opt-in missing")
	}
	for _, err := range []error{transport.ErrRepositoryNotFound, transport.ErrEmptyRemoteRepository} {
		if !errors.Is(cocoaPodsGitError(err), store.ErrNotFound) {
			t.Fatal(err)
		}
	}
	for _, err := range []error{source.ErrDenied, source.ErrSuperseded, source.ErrInvalidResult, context.Canceled, context.DeadlineExceeded, store.ErrNotFound} {
		if !errors.Is(cocoaPodsGitError(err), err) {
			t.Fatal(err)
		}
		for _, wrapped := range []error{
			plumbing.NewUnexpectedError(err),
			plumbing.NewPermanentError(err),
			fmt.Errorf("request: %w", plumbing.NewUnexpectedError(plumbing.NewPermanentError(err))),
		} {
			if !errors.Is(cocoaPodsGitError(wrapped), err) {
				t.Fatalf("Git wrapper lost policy failure: %v", wrapped)
			}
		}
	}
	if !errors.Is(cocoaPodsGitError(io.ErrUnexpectedEOF), source.ErrUnavailable) {
		t.Fatal("unknown error leaked")
	}
	status := http.StatusOK
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(status)
		_, _ = w.Write([]byte("response"))
	}))
	defer srv.Close()
	c = repo.Config{
		Key:                          "pods",
		Class:                        repo.Remote,
		PackageType:                  repo.CocoaPods,
		URL:                          srv.URL,
		ExternalDependenciesEnabled:  &enabled,
		ExternalDependenciesPatterns: []string{srv.URL + "/**"},
	}
	client, err := NewClient(
		c,
		Options{CheckURL: func(context.Context, *url.URL) error { return nil }},
	)
	if err != nil {
		t.Fatal(err)
	}
	defer client.Close()
	g := &cocoaPodsCheckout{policy: &Policy{}, client: client, request: source.Request{Repo: c}}
	transport := cocoaPodsGitTransport{checkout: g, repository: srv.URL + "/main.git"}
	for _, tc := range []struct {
		name, method, path, body string
		wantErr                  bool
	}{
		{"refs", "GET", "/main.git/info/refs?service=git-upload-pack", "", false},
		{"upload", "POST", "/main.git/git-upload-pack", "want\n", false},
		{"wrong path", "GET", "/other.git/info/refs", "", true},
		{"wrong verb", "DELETE", "/main.git/info/refs", "", true},
		{"body bound", "POST", "/main.git/git-upload-pack", strings.Repeat("x", 4097), true},
	} {
		t.Run(tc.name, func(t *testing.T) {
			req, err := http.NewRequestWithContext(
				t.Context(),
				tc.method,
				srv.URL+tc.path,
				strings.NewReader(tc.body),
			)
			if err != nil {
				t.Fatal(err)
			}
			response, err := transport.RoundTrip(req)
			if response != nil {
				_ = response.Body.Close()
			}
			if (err != nil) != tc.wantErr {
				t.Fatal(err)
			}
		})
	}
	status = 503
	req, _ := http.NewRequestWithContext(
		t.Context(),
		"GET",
		srv.URL+"/main.git/info/refs?service=git-upload-pack",
		nil,
	)
	if _, err := transport.RoundTrip(req); err == nil {
		t.Fatal("upstream failure ignored")
	}
	writer := &cocoaPodsLimitedWriter{writer: io.Discard, remaining: 1}
	if _, err := writer.Write([]byte("xx")); !errors.Is(err, source.ErrInvalidResult) {
		t.Fatal(err)
	}
	if _, err := writer.Write([]byte("x")); err != nil {
		t.Fatal(err)
	}
}

func TestUnitCocoaPodsGitNativePackFixture(t *testing.T) {
	pack, err := os.ReadFile("../../test/fixtures/cocoapods-git/root.pack")
	if err != nil {
		t.Fatal(err)
	}
	s, err := decodeCocoaPodsPack(t.Context(), pack, cocoaPodsGitBytes, cocoaPodsGitObjects)
	if err != nil {
		t.Fatal(err)
	}
	for hash, o := range s.Objects {
		if o.Type() == plumbing.TagObject {
			commit, err := cocoaPodsGitCommit(s, hash)
			if err != nil || commit.TreeHash.IsZero() {
				t.Fatalf("annotated tag %v %v", commit, err)
			}
		}
	}
}
