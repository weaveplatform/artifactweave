package debian

import (
	"errors"
	"slices"
	"strings"
	"testing"
	"time"

	"github.com/weaveplatform/artifactweave/internal/artifacts/api/repo"
	"github.com/weaveplatform/artifactweave/internal/artifacts/core/virtual"
	"github.com/weaveplatform/artifactweave/internal/kernel"
)

// TestUnitMergerKeepsTheFirstMembersRowOfEachFilename: the merge lists, in
// each index, every row its members list, a Filename two members list from
// the earlier one, and writes each index's forms, their by-hash copies and
// a Release over every index; no row at all is not found, and an index
// over the limit is refused.
func TestUnitMergerKeepsTheFirstMembersRowOfEachFilename(t *testing.T) {
	const amd, arm = "dists/stable/main/binary-amd64/Packages", "dists/stable/main/binary-arm64/Packages"
	row := func(name, file, from string) PackageRecord {
		return PackageRecord{Name: name, Version: "1.0", Filename: file,
			Raw: []byte("Package: " + name + "\nVersion: 1.0\nFilename: " + file + "\nX-From: " + from + "\n\n")}
	}
	parts := []virtual.Part[[]MemberIndex]{
		{Member: repo.Config{Key: "local"}, Doc: []MemberIndex{
			{Path: amd, Rows: []PackageRecord{row("beta", "pool/beta.deb", "local"), row("alpha", "pool/alpha.deb", "local")}},
		}},
		{Member: repo.Config{Key: "remote"}, Doc: []MemberIndex{
			{Path: amd, Rows: []PackageRecord{row("alpha", "pool/alpha.deb", "remote")}},
			{Path: arm, Rows: []PackageRecord{row("gamma", "pool/gamma.deb", "remote")}},
			{Path: "dists/stable/contrib/binary-amd64/Packages"},
		}},
	}
	m := Merger{Distribution: "stable", Formats: []string{"xz"}, Date: time.Unix(0, 0).UTC(), Limit: 1 << 20}
	files, err := m.Merge(t.Context(), parts)
	if err != nil {
		t.Fatal(err)
	}
	body := string(files[amd])
	if !strings.Contains(body, "X-From: local") || strings.Contains(body, "X-From: remote") ||
		strings.Index(body, "alpha") > strings.Index(body, "beta") {
		t.Errorf("amd64 index is\n%s\nwant alpha then beta, both from the local member", body)
	}
	for _, name := range []string{amd, amd + ".gz", amd + ".xz", arm, arm + ".gz", arm + ".xz", "dists/stable/Release"} {
		if _, ok := files[name]; !ok {
			t.Errorf("the merge wrote no %s", name)
		}
	}
	release := string(files["dists/stable/Release"])
	if !strings.Contains(release, "Architectures: amd64 arm64") || !strings.Contains(release, "Components: main") {
		t.Errorf("Release is\n%s\nwant both architectures and the main component alone", release)
	}
	var hashed []string
	for name := range files {
		if strings.Contains(name, "/by-hash/SHA256/") {
			hashed = append(hashed, name)
		}
	}
	if len(hashed) != 6 {
		t.Errorf("by-hash copies %v, want one per form", slices.Sorted(slices.Values(hashed)))
	}
	if _, err := m.Merge(t.Context(), nil); !errors.Is(err, kernel.ErrNotFound) {
		t.Errorf("merge of no member = %v, want not found", err)
	}
	m.Limit = 10
	if _, err := m.Merge(t.Context(), parts); !errors.Is(err, ErrIndexTooLarge) {
		t.Errorf("merge over the limit = %v, want ErrIndexTooLarge", err)
	}
}

// TestUnitIndexArchitecture: a binary Packages index names its
// architecture, and any other path none.
func TestUnitIndexArchitecture(t *testing.T) {
	for path, want := range map[string]string{
		"dists/stable/main/binary-arm64/Packages": "arm64",
		"dists/stable/main/source/Sources":        "",
		"dists/stable/Release":                    "",
	} {
		if got := IndexArchitecture(path); got != want {
			t.Errorf("IndexArchitecture(%q) = %q, want %q", path, got, want)
		}
	}
}
