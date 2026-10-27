package vcs

import (
	"archive/tar"
	"archive/zip"
	"bytes"
	"compress/gzip"
	"context"
	"errors"
	"io"
	"strings"
	"testing"

	"github.com/weaveplatform/artifactweave/pkg/repo"
)

func tarGZ(t *testing.T, name string) []byte {
	t.Helper()
	var b bytes.Buffer
	gz := gzip.NewWriter(&b)
	tw := tar.NewWriter(gz)
	if err := tw.WriteHeader(&tar.Header{Name: name, Mode: 0o644, Size: 4}); err != nil {
		t.Fatal(err)
	}
	if _, err := tw.Write([]byte("data")); err != nil {
		t.Fatal(err)
	}
	if err := tw.Close(); err != nil {
		t.Fatal(err)
	}
	if err := gz.Close(); err != nil {
		t.Fatal(err)
	}
	return b.Bytes()
}

func TestUnitValidateArchiveAdmitsProviderTarAndZIP(t *testing.T) {
	archive := tarGZ(t, "repo-abc/README.md")
	if err := ValidateArchive(
		t.Context(),
		bytes.NewReader(archive),
		int64(len(archive)),
		"tar.gz",
	); err != nil {
		t.Fatal(err)
	}
	var b bytes.Buffer
	zw := zip.NewWriter(&b)
	w, err := zw.Create("repo-abc/README.md")
	if err != nil {
		t.Fatal(err)
	}
	if _, err = w.Write([]byte("data")); err != nil {
		t.Fatal(err)
	}
	if err = zw.Close(); err != nil {
		t.Fatal(err)
	}
	if err = ValidateArchive(
		t.Context(),
		bytes.NewReader(b.Bytes()),
		int64(b.Len()),
		"zip",
	); err != nil {
		t.Fatal(err)
	}
}

func TestUnitValidateArchiveRejectsTraversalAndCorruption(t *testing.T) {
	for name, body := range map[string][]byte{
		"parent":  tarGZ(t, "../secret"),
		"root":    tarGZ(t, "/secret"),
		"windows": tarGZ(t, `repo\\secret`),
		"crc": func() []byte {
			b := bytes.Clone(tarGZ(t, "repo/README.md"))
			b[len(b)-5] ^= 0xff
			return b
		}(),
	} {
		t.Run(name, func(t *testing.T) {
			if err := ValidateArchive(
				context.Background(),
				bytes.NewReader(body),
				int64(len(body)),
				"tar.gz",
			); err == nil {
				t.Fatal("accepted unsafe or corrupt archive")
			}
		})
	}
}

func TestUnitFileZIPSelectsExactRepositoryRelativePath(t *testing.T) {
	files := []struct{ name, body string }{
		{"repo-abc/README.md", "root"},
		{"repo-abc/vendor/pkg/README.md", "nested"},
	}
	var tarBuf bytes.Buffer
	gz := gzip.NewWriter(&tarBuf)
	tw := tar.NewWriter(gz)
	var zipBuf bytes.Buffer
	zw := zip.NewWriter(&zipBuf)
	for _, file := range files {
		if err := tw.WriteHeader(&tar.Header{
			Name: file.name, Mode: 0o644, Size: int64(len(file.body)),
		}); err != nil {
			t.Fatal(err)
		}
		if _, err := tw.Write([]byte(file.body)); err != nil {
			t.Fatal(err)
		}
		writer, err := zw.Create(file.name)
		if err != nil {
			t.Fatal(err)
		}
		if _, err := writer.Write([]byte(file.body)); err != nil {
			t.Fatal(err)
		}
	}
	if err := tw.Close(); err != nil {
		t.Fatal(err)
	}
	if err := gz.Close(); err != nil {
		t.Fatal(err)
	}
	if err := zw.Close(); err != nil {
		t.Fatal(err)
	}
	for _, archive := range []struct {
		ext  string
		data []byte
	}{
		{"tar.gz", tarBuf.Bytes()},
		{"zip", zipBuf.Bytes()},
	} {
		for _, selected := range []struct{ path, body string }{
			{"README.md", "root"},
			{"vendor/pkg/README.md", "nested"},
		} {
			t.Run(archive.ext+"/"+selected.path, func(t *testing.T) {
				data, err := FileZIP(t.Context(), archive.data, archive.ext, selected.path)
				if err != nil {
					t.Fatal(err)
				}
				out, err := zip.NewReader(bytes.NewReader(data), int64(len(data)))
				if err != nil || len(out.File) != 1 || out.File[0].Name != selected.path {
					t.Fatalf("selected ZIP mismatch: %v", err)
				}
				reader, err := out.File[0].Open()
				if err != nil {
					t.Fatal(err)
				}
				content, err := io.ReadAll(reader)
				if err != nil {
					t.Fatal(err)
				}
				if err := reader.Close(); err != nil {
					t.Fatal(err)
				}
				if string(content) != selected.body {
					t.Fatalf("selected content = %q, want %q", content, selected.body)
				}
			})
		}
	}
}

func TestUnitFileZIPAcceptsRootlessFile(t *testing.T) {
	data, err := FileZIP(t.Context(), tarGZ(t, "README.md"), "tar.gz", "README.md")
	if err != nil {
		t.Fatal(err)
	}
	out, err := zip.NewReader(bytes.NewReader(data), int64(len(data)))
	if err != nil || len(out.File) != 1 || out.File[0].Name != "README.md" {
		t.Fatalf("rootless selected ZIP mismatch: %v", err)
	}
}

func TestUnitVCSProjectRefAndFilePathValidation(t *testing.T) {
	for _, tc := range []struct {
		owner, name string
		valid       bool
	}{
		{"team", "project", true},
		{"team/subgroup", "project-1", true},
		{"", "project", false},
		{"team", "", false},
		{strings.Repeat("a", 513), "project", false},
		{"team", strings.Repeat("a", 129), false},
		{"a/b/c/d/e/f/g/h/i", "project", false},
		{"team/../admin", "project", false},
		{"team/.hidden", "project", false},
		{"team", "project.", false},
		{"team", "project!", false},
	} {
		if got := ValidProject(tc.owner, tc.name); got != tc.valid {
			t.Errorf("ValidProject(%q, %q) = %t, want %t", tc.owner, tc.name, got, tc.valid)
		}
	}
	for _, tc := range []struct {
		ref   string
		valid bool
	}{
		{"main", true},
		{"feature/one", true},
		{"release-1.0", true},
		{"", false},
		{"../main", false},
		{"a//b", false},
		{"a.lock", false},
		{"v1^{}", false},
		{"a@{b", false},
		{"a b", false},
	} {
		if got := ValidRef(tc.ref); got != tc.valid {
			t.Errorf("ValidRef(%q) = %t, want %t", tc.ref, got, tc.valid)
		}
	}
	for _, tc := range []struct {
		path  string
		valid bool
	}{
		{"README.md", true},
		{"src/main.go", true},
		{"", false},
		{"/README.md", false},
		{"src/", false},
		{"src//main.go", false},
		{"src/../secret", false},
		{"src\\secret", false},
		{"src/a!b", false},
		{"src/%2e%2e", false},
		{"src/\x01", false},
	} {
		if got := ValidFilePath(tc.path); got != tc.valid {
			t.Errorf("ValidFilePath(%q) = %t, want %t", tc.path, got, tc.valid)
		}
	}
}

func TestUnitVCSProviderURLsStayWithinConfiguredOrigin(t *testing.T) {
	const oid = "0123456789abcdef0123456789abcdef01234567"
	for _, tc := range []struct {
		provider repo.VCSGitProvider
		expected string
	}{
		{"", "/git/team/sub/project/archive/" + oid + ".tar.gz"},
		{repo.GitHub, "/git/team/sub/project/archive/" + oid + ".tar.gz"},
		{repo.GitHubEnterprise, "/git/team/sub/project/archive/" + oid + ".tar.gz"},
		{repo.GitLab, "/git/team/sub/project/-/archive/" + oid + "/project-" + oid + ".tar.gz"},
		{repo.Bitbucket, "/git/team/sub/project/get/" + oid + ".tar.gz"},
	} {
		c := repo.Config{URL: "https://git.example/git", VCSGitProvider: tc.provider}
		refs, err := GitRefsURL(c, "team/sub", "project")
		if err != nil ||
			refs.String() != "https://git.example/git/team/sub/project.git/info/refs?service=git-upload-pack" {
			t.Fatalf("%q refs URL = %v, %v", tc.provider, refs, err)
		}
		archive, err := ArchiveURL(c, "team/sub", "project", oid, "tar.gz")
		if err != nil || archive.String() != "https://git.example"+tc.expected {
			t.Fatalf("%q archive URL = %v, %v; want %s", tc.provider, archive, err, tc.expected)
		}
	}
	c := repo.Config{
		URL: "https://git.example/git", VCSGitProvider: repo.CustomVCS,
		VCSGitDownloadURL: "archive/{0}/{1}/{2}.{3}",
	}
	u, err := ArchiveURL(c, "team/sub", "project", oid, "zip")
	if err != nil || u.String() != "https://git.example/git/archive/team/sub/project/"+oid+".zip" {
		t.Fatalf("custom archive URL = %v, %v", u, err)
	}
}

func TestUnitVCSProviderURLsRejectInvalidConfiguration(t *testing.T) {
	for _, raw := range []string{
		"file:///tmp/git", "https://user:pass@git.example", "https://git.example/?token=secret",
		"https://git.example/#fragment", "https://git.example/%2e%2e", "https://",
	} {
		c := repo.Config{URL: raw}
		if _, err := GitRefsURL(c, "team", "project"); !errors.Is(err, ErrInvalidPath) {
			t.Errorf("refs accepted base URL %q: %v", raw, err)
		}
		if _, err := ArchiveURL(
			c,
			"team",
			"project",
			"main",
			"zip",
		); !errors.Is(
			err,
			ErrInvalidPath,
		) {
			t.Errorf("archive accepted base URL %q: %v", raw, err)
		}
	}
	for _, template := range []string{
		"", "/archive/{2}.zip", "../archive/{2}.zip", "archive//{2}.zip",
		"archive/{2}?token=secret", "archive/{2}#fragment", "archive/{2}%2fsecret", "archive/{2}\\secret", "archive/{missing}.zip",
	} {
		c := repo.Config{
			URL:               "https://git.example",
			VCSGitProvider:    repo.CustomVCS,
			VCSGitDownloadURL: template,
		}
		if _, err := ArchiveURL(
			c,
			"team",
			"project",
			"main",
			"zip",
		); !errors.Is(
			err,
			ErrInvalidPath,
		) {
			t.Errorf("accepted custom template %q: %v", template, err)
		}
	}
	for _, c := range []repo.Config{
		{URL: "https://git.example", VCSGitProvider: "unsupported"},
		{URL: "https://git.example", VCSGitProvider: repo.GitHub},
	} {
		ref := "main"
		if c.VCSGitProvider == repo.GitHub {
			ref = "../secret"
		}
		if _, err := ArchiveURL(c, "team", "project", ref, "zip"); !errors.Is(err, ErrInvalidPath) {
			t.Errorf("accepted provider %q ref %q: %v", c.VCSGitProvider, ref, err)
		}
	}
}

func vcsZIP(t *testing.T, names ...string) []byte {
	t.Helper()
	var b bytes.Buffer
	w := zip.NewWriter(&b)
	for _, name := range names {
		f, err := w.Create(name)
		if err != nil {
			t.Fatal(err)
		}
		if !strings.HasSuffix(name, "/") {
			if _, err := f.Write([]byte("data")); err != nil {
				t.Fatal(err)
			}
		}
	}
	if err := w.Close(); err != nil {
		t.Fatal(err)
	}
	return b.Bytes()
}

func TestUnitVCSArchiveRejectsUnsafeZIPEntriesAndBadEnvelope(t *testing.T) {
	for _, tc := range []struct {
		name string
		data []byte
		size int64
		ext  string
	}{
		{"empty", nil, 0, "zip"},
		{"unsupported format", vcsZIP(t, "repo/README.md"), 1, "rar"},
		{"size mismatch", vcsZIP(t, "repo/README.md"), 1, "zip"},
		{"empty zip", vcsZIP(t), int64(len(vcsZIP(t))), "zip"},
		{"traversal", vcsZIP(t, "../secret"), int64(len(vcsZIP(t, "../secret"))), "zip"},
		{"absolute", vcsZIP(t, "/secret"), int64(len(vcsZIP(t, "/secret"))), "zip"},
		{"bad zip", []byte("invalid ZIP"), int64(len("invalid ZIP")), "zip"},
	} {
		t.Run(tc.name, func(t *testing.T) {
			if err := ValidateArchive(
				t.Context(),
				bytes.NewReader(tc.data),
				tc.size,
				tc.ext,
			); !errors.Is(
				err,
				ErrInvalidArchive,
			) {
				t.Fatalf("unsafe archive accepted: %v", err)
			}
		})
	}
	data := vcsZIP(t, "repo/README.md")
	ctx, cancel := context.WithCancel(t.Context())
	cancel()
	if err := ValidateArchive(
		ctx,
		bytes.NewReader(data),
		int64(len(data)),
		"zip",
	); !errors.Is(
		err,
		context.Canceled,
	) {
		t.Fatalf("canceled ZIP validation = %v", err)
	}
	tarData := tarGZ(t, "repo/README.md")
	if err := ValidateArchive(
		ctx,
		bytes.NewReader(tarData),
		int64(len(tarData)),
		"tar.gz",
	); !errors.Is(
		err,
		context.Canceled,
	) {
		t.Fatalf("canceled TAR validation = %v", err)
	}
}

func TestUnitVCSFileZIPRejectsMissingDuplicateAndCorruptSelections(t *testing.T) {
	zipData := vcsZIP(t, "repo/README.md", "repo/src/main.go")
	tarData := tarGZ(t, "repo/README.md")
	for _, tc := range []struct {
		name, ext, file string
		data            []byte
		expected        error
	}{
		{"missing zip", "zip", "missing.go", zipData, ErrInvalidPath},
		{"missing tar", "tar.gz", "missing.go", tarData, ErrInvalidPath},
		{"duplicate zip", "zip", "README.md", vcsZIP(t, "repo/README.md", "repo/README.md"), ErrInvalidArchive},
		{"invalid zip", "zip", "README.md", []byte("not ZIP"), ErrInvalidArchive},
		{"invalid tar", "tar.gz", "README.md", []byte("not gzip"), ErrInvalidArchive},
		{"invalid selector", "zip", "../secret", zipData, ErrInvalidArchive},
		{"invalid extension", "rar", "README.md", zipData, ErrInvalidArchive},
		{"empty input", "zip", "README.md", nil, ErrInvalidArchive},
	} {
		t.Run(tc.name, func(t *testing.T) {
			if _, err := FileZIP(
				t.Context(),
				tc.data,
				tc.ext,
				tc.file,
			); !errors.Is(
				err,
				tc.expected,
			) {
				t.Fatalf("FileZIP = %v, want %v", err, tc.expected)
			}
		})
	}
	ctx, cancel := context.WithCancel(t.Context())
	cancel()
	for _, tc := range []struct {
		ext  string
		data []byte
	}{{"zip", zipData}, {"tar.gz", tarData}} {
		if _, err := FileZIP(ctx, tc.data, tc.ext, "README.md"); !errors.Is(err, context.Canceled) {
			t.Errorf("canceled %s selection = %v", tc.ext, err)
		}
	}
}
