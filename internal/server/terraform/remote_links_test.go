package terraform

import (
	"archive/tar"
	"archive/zip"
	"bytes"
	"context"
	"errors"
	"fmt"
	"io/fs"
	"net/http"
	"strings"
	"testing"

	"github.com/weaveplatform/artifactweave/internal/fakeupstream"
)

type archiveLinkFixture struct {
	name, target string
	hard         bool
}

func linkArchive(t *testing.T, kind string, links ...archiveLinkFixture) []byte {
	t.Helper()
	var out bytes.Buffer
	if kind == "zip" {
		writer := zip.NewWriter(&out)
		for _, dir := range []string{"safe/", "target/"} {
			h := &zip.FileHeader{Name: dir}
			h.SetMode(fs.ModeDir | 0o700)
			if _, err := writer.CreateHeader(h); err != nil {
				t.Fatal(err)
			}
		}
		for _, link := range links {
			h := &zip.FileHeader{Name: link.name}
			h.SetMode(fs.ModeSymlink | 0o700)
			file, err := writer.CreateHeader(h)
			if err != nil {
				t.Fatal(err)
			}
			if _, err = file.Write([]byte(link.target)); err != nil {
				t.Fatal(err)
			}
		}
		if err := writer.Close(); err != nil {
			t.Fatal(err)
		}
	} else {
		writer := tar.NewWriter(&out)
		for _, dir := range []string{"safe/", "target/"} {
			if err := writer.WriteHeader(
				&tar.Header{Name: dir, Typeflag: tar.TypeDir, Mode: 0o700},
			); err != nil {
				t.Fatal(err)
			}
		}
		for _, link := range links {
			flag := byte(tar.TypeSymlink)
			if link.hard {
				flag = tar.TypeLink
			}
			if err := writer.WriteHeader(
				&tar.Header{Name: link.name, Linkname: link.target, Typeflag: flag, Mode: 0o700},
			); err != nil {
				t.Fatal(err)
			}
		}
		if err := writer.Close(); err != nil {
			t.Fatal(err)
		}
	}
	return out.Bytes()
}

func TestAccRemoteModuleChainedLinkEscape(t *testing.T) {
	for _, kind := range []string{"zip", "tar"} {
		t.Run(kind, func(t *testing.T) {
			body := linkArchive(
				t,
				kind,
				archiveLinkFixture{"safe/first", "../target", false},
				archiveLinkFixture{"safe/second", "first/../../outside", false},
			)
			completeArchive(t, body, "module."+kind, false)
		})
	}
}

func TestAccRemoteModuleLinkChains(t *testing.T) {
	cases := []struct {
		name  string
		links []archiveLinkFixture
		valid bool
	}{
		{
			"safe sibling chain",
			[]archiveLinkFixture{
				{"safe/first", "../target", false},
				{"safe/second", "first/main.tf", false},
			},
			true,
		},
		{
			"safe parent traversal after expansion",
			[]archiveLinkFixture{
				{"safe/first", "../target", false},
				{"safe/second", "first/../target/main.tf", false},
			},
			true,
		},
		{
			"repeated finite link",
			[]archiveLinkFixture{
				{"safe/first", "../target", false},
				{"safe/second", "first/../safe/first/main.tf", false},
			},
			true,
		},
		{
			"cycle",
			[]archiveLinkFixture{{"safe/first", "second", false}, {"safe/second", "first", false}},
			false,
		},
		{"self cycle", []archiveLinkFixture{{"safe/first", "first", false}}, false},
		{
			"reversed escape chain",
			[]archiveLinkFixture{
				{"safe/second", "first/../../outside", false},
				{"safe/first", "../target", false},
			},
			false,
		},
	}
	for _, kind := range []string{"zip", "tar"} {
		for _, tc := range cases {
			t.Run(
				kind+"/"+tc.name,
				func(t *testing.T) { completeArchive(t, linkArchive(t, kind, tc.links...), "module."+kind, tc.valid) },
			)
		}
	}
	for _, tc := range []struct {
		name  string
		links []archiveLinkFixture
		valid bool
	}{
		{"safe hardlink chain", []archiveLinkFixture{{"safe/first", "target/main.tf", true}, {"safe/second", "safe/first", true}}, true},
		{"hardlink cycle", []archiveLinkFixture{{"safe/first", "safe/second", true}, {"safe/second", "safe/first", true}}, false},
		{"hardlink to symlink", []archiveLinkFixture{{"safe/first", "../target", false}, {"alias", "safe/first", true}}, false},
		{"hardlink through symlink parent", []archiveLinkFixture{{"safe/first", "../target", false}, {"alias", "safe/first/file", true}}, false},
	} {
		t.Run(
			tc.name,
			func(t *testing.T) { completeArchive(t, linkArchive(t, "tar", tc.links...), "module.tar", tc.valid) },
		)
	}
}

func TestAccRemoteModuleLinkBounds(t *testing.T) {
	links := []archiveLinkFixture{}
	for i := 0; i < moduleLinkDepth+1; i++ {
		links = append(
			links,
			archiveLinkFixture{fmt.Sprintf("safe/link%d", i), fmt.Sprintf("link%d", i+1), false},
		)
	}
	for _, kind := range []string{"zip", "tar"} {
		completeArchive(t, linkArchive(t, kind, links...), "module."+kind, false)
	}
	graph := moduleLinkGraph{}
	if err := graph.add("safe/link", strings.Repeat("a", 8193), true); err == nil {
		t.Fatal("oversized target admitted")
	}
	graph.bytes = moduleLinkBytes
	if err := graph.add("safe/link", "file", true); err == nil {
		t.Fatal("link memory bound ignored")
	}
	graph = moduleLinkGraph{}
	if err := graph.add("safe/link", "a/b/c", true); err != nil {
		t.Fatal(err)
	}
	graph.remaining = 1
	if _, err := graph.resolve(t.Context(), "safe/link", map[string]bool{}, 0, true); err == nil {
		t.Fatal("component budget ignored")
	}
	ctx, cancel := context.WithCancel(t.Context())
	cancel()
	if err := graph.validate(
		ctx,
		map[string]bool{"safe/link": true},
	); !errors.Is(
		err,
		context.Canceled,
	) {
		t.Fatal(err)
	}
}

func TestAccRemoteModuleLinkAdmissionAndRetry(t *testing.T) {
	for _, noStore := range []bool{false, true} {
		t.Run(fmt.Sprint(noStore), func(t *testing.T) {
			f := newRemoteFixture(t)
			f.modules("1.0.0")
			if noStore {
				c, _ := f.db.Repos().Get(t.Context(), "remote")
				c.StoreArtifactsLocally = new(false)
				if err := f.db.Repos().Update(t.Context(), c); err != nil {
					t.Fatal(err)
				}
			}
			body := linkArchive(
				t,
				"zip",
				archiveLinkFixture{"safe/first", "../target", false},
				archiveLinkFixture{"safe/second", "first/../../outside", false},
			)
			f.up.Set(
				"/archives/1.0.0.zip",
				fakeupstream.Resource{
					Body:   body,
					Header: http.Header{"Content-Type": {"application/zip"}},
				},
			)
			expect(t, f.do("GET", remoteModuleBase+"1.0.0/download", "reader"), 502)
			if _, err := f.db.Items().
				Get(t.Context(), "remote", true, "ns/name/aws/1.0.0.zip"); err == nil {
				t.Fatal("unsafe archive persisted")
			}
			f.modules("1.0.0")
			expect(t, f.do("GET", f.signed("1.0.0", "reader"), ""), 200)
		})
	}
}
