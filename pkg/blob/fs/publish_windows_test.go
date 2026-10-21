package fs

import (
	"errors"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"golang.org/x/sys/windows"
)

func TestPublishPreservesExistingBlob(t *testing.T) {
	for _, held := range []bool{false, true} {
		t.Run(map[bool]string{false: "closed", true: "open reader"}[held], func(t *testing.T) {
			dir := t.TempDir()
			from, to := filepath.Join(dir, "upload"), filepath.Join(dir, "blob")
			write := func(path, content string) {
				t.Helper()
				if err := os.WriteFile(path, []byte(content), 0o600); err != nil {
					t.Fatal(err)
				}
			}
			write(from, "loser")
			write(to, "winner")
			if held {
				r, err := os.Open(to)
				if err != nil {
					t.Fatal(err)
				}
				defer func() { _ = r.Close() }()
			}
			if err := publish(from, to); err == nil {
				t.Fatal("publication replaced an existing blob")
			}
			for path, want := range map[string]string{from: "loser", to: "winner"} {
				got, err := os.ReadFile(path)
				if err != nil || string(got) != want {
					t.Fatalf("%s: %q, %v", path, got, err)
				}
			}
		})
	}
}

func TestPublishRelativeAndLongPaths(t *testing.T) {
	t.Chdir(t.TempDir())
	for _, dir := range []string{"relative", strings.Repeat(`segment\`, 40)} {
		if err := os.MkdirAll(dir, 0o750); err != nil {
			t.Fatal(err)
		}
		from, to := filepath.Join(dir, "upload"), filepath.Join(dir, "blob")
		if err := os.WriteFile(from, []byte("complete"), 0o600); err != nil {
			t.Fatal(err)
		}
		if err := publish(from, to); err != nil {
			t.Fatal(err)
		}
		if got, err := os.ReadFile(to); err != nil || string(got) != "complete" {
			t.Fatalf("published %q: %q, %v", to, got, err)
		}
		if _, err := os.Stat(from); !errors.Is(err, os.ErrNotExist) {
			t.Fatalf("source remains: %v", err)
		}
		if err := publish(from, to+"-missing"); !errors.Is(err, os.ErrNotExist) {
			t.Fatalf("missing source: %v", err)
		}
	}
}

func TestPublicationPaths(t *testing.T) {
	long := strings.Repeat(`segment\`, 40) + "blob"
	for _, tc := range []struct{ path, want string }{
		{`C:\short\blob`, `C:\short\blob`},
		{`C:\` + long, `\\?\C:\` + long},
		{`\\server\share\blob`, `\\server\share\blob`},
		{`\\server\share\` + long, `\\?\UNC\server\share\` + long},
		{`\\?\C:\` + long, `\\?\C:\` + long},
		{`\\.\C:\` + long, `\\.\C:\` + long},
		{`\??\C:\` + long, `\??\C:\` + long},
	} {
		p, err := publicationPath(tc.path)
		if err != nil {
			t.Fatal(err)
		}
		if got := windows.UTF16PtrToString(p); got != tc.want {
			t.Errorf("%q: got %q, want %q", tc.path, got, tc.want)
		}
	}
	for _, paths := range [][2]string{
		{"\x00", "valid"},
		{"valid", "\x00"},
		{`\\?\C:\` + "\x00", "valid"},
		{"valid", `\\?\C:\` + "\x00"},
	} {
		var link *os.LinkError
		if err := publish(paths[0], paths[1]); !errors.As(err, &link) {
			t.Fatalf("invalid path not reported as a rename error: %v", err)
		}
	}
}
