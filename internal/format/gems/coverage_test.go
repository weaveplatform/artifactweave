package gems

import (
	"bytes"
	"compress/gzip"
	"io"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"
)

func TestUnitCompactIndexKeepsOnlyLiveVersions(t *testing.T) {
	const digest = "0123456789abcdef0123456789abcdef"
	doc := "created_at: 2026-10-24T00:00:00Z\n---\nrake 1.0.0,-1.0.0,2.0.0 " + digest + "\n"
	entries, err := ParseCompactVersions([]byte(doc))
	if err != nil {
		t.Fatal(err)
	}
	if entries["rake"].Versions["1.0.0"] || !entries["rake"].Versions["2.0.0"] {
		t.Fatalf("yanked or live version misclassified: %+v", entries["rake"])
	}
	// RubyGems.org appends later rows for the same gem during a month. The
	// newest info MD5 wins while version additions and yanks accumulate.
	const newerDigest = "fedcba9876543210fedcba9876543210"
	appendOnly := "created_at: 2026-10-24T00:00:00Z\n---\nrake 1.0.0 " + digest +
		"\nrake 2.0.0,-1.0.0 " + newerDigest + "\n"
	entries, err = ParseCompactVersions([]byte(appendOnly))
	if err != nil {
		t.Fatal(err)
	}
	if entries["rake"].MD5 != newerDigest || entries["rake"].Versions["1.0.0"] ||
		!entries["rake"].Versions["2.0.0"] {
		t.Fatalf("append-only compact updates were lost: %+v", entries["rake"])
	}

	for name, body := range map[string]string{
		"missing newline":   "---\nrake 2.0.0 " + digest,
		"invalid digest":    "---\nrake 2.0.0 " + strings.Repeat("z", 32) + "\n",
		"invalid version":   "---\nrake 2..0 " + digest + "\n",
		"missing delimiter": "rake 2.0.0 " + digest + "\n",
	} {
		t.Run(name, func(t *testing.T) {
			if _, err := ParseCompactVersions([]byte(body)); err == nil {
				t.Fatalf("accepted invalid compact versions document %q", body)
			}
		})
	}
}

func TestUnitCompactInfoPreservesDependenciesAndPlatform(t *testing.T) {
	p := Package{
		Name: "sample", Version: "2.1.0", Platform: "x86_64-linux",
		SHA256:       strings.Repeat("a", 64),
		Dependencies: []Dependency{{Name: "rake", Requirement: ">= 12.0, < 14.0"}},
		Ruby:         ">= 2.7", RubyGems: ">= 3.0",
	}
	info := CompactInfo("sample", []Package{p})
	got, err := ParseCompactInfo("sample", info)
	if err != nil {
		t.Fatal(err)
	}
	if len(got) != 1 || got[0].Path != "gems/sample-2.1.0-x86_64-linux.gem" ||
		got[0].SHA256 != p.SHA256 || got[0].Ruby != p.Ruby || got[0].RubyGems != p.RubyGems ||
		len(got[0].Dependencies) != 1 || got[0].Dependencies[0] != p.Dependencies[0] {
		t.Fatalf("compact metadata lost install requirements: %+v", got)
	}
	versionEntries, err := ParseCompactVersions(CompactVersions([]Package{p}))
	if err != nil {
		t.Fatal(err)
	}
	if versionEntries["sample"].MD5 != InfoMD5(info) ||
		!versionEntries["sample"].Versions[VersionToken(p)] {
		t.Fatalf("compact versions do not bind the platform info: %+v", versionEntries)
	}
}

func TestUnitCompactInfoRejectsIncompleteOrUnsafeRequirements(t *testing.T) {
	sha := strings.Repeat("a", 64)
	for name, body := range map[string]string{
		"missing checksum": "---\n1.0.0 |ruby:>= 3.0\n",
		"invalid checksum hex": "---\n1.0.0 |checksum:" + strings.Repeat("z", 64) + "\n",
		"invalid dependency name": "---\n1.0.0 bad/name:>= 1|checksum:" + sha + "\n",
		"missing dependency separator": "---\n1.0.0 baddependency|checksum:" + sha + "\n",
		"missing requirement separator": "---\n1.0.0 |checksum:" + sha + ",ruby>=3\n",
		"noncanonical platform": "---\n1.0.0-.. |checksum:" + sha + "\n",
	} {
		t.Run(name, func(t *testing.T) {
			if _, err := ParseCompactInfo("sample", []byte(body)); err == nil {
				t.Fatalf("accepted unsafe compact info %q", body)
			}
		})
	}
}

func TestUnitRubyVersionOrderingMatchesGemVersion(t *testing.T) {
	for _, tc := range []struct {
		a, b string
		want int
	}{
		{"1.0.0", "1.0", 0},
		{"1.0.0.pre", "1.0.0", -1},
		{"1.0.0.rc1", "1.0.0.rc2", -1},
		{"2.0.0", "10.0.0", -1},
		{"1.0.0.a", "1.0.0.1", -1},
		{"1.0.0-rc1", "1.0.0.rc1", -1},
	} {
		got := CompareVersions(tc.a, tc.b)
		if got != tc.want {
			t.Errorf(
				"Gem::Version ordering %q versus %q: got %d, want %d",
				tc.a,
				tc.b,
				got,
				tc.want,
			)
		}
	}
}

func TestAccRubyLatestSpecsRetainsStableWhenPrereleaseIsNewer(t *testing.T) {
	if _, err := exec.LookPath("ruby"); err != nil {
		t.Skip("Ruby client unavailable")
	}
	packages := []Package{
		{Name: "sample", Version: "1.0.0", Platform: "ruby"},
		{Name: "sample", Version: "1.1.0.pre", Platform: "ruby"},
	}
	data, err := Specs(packages, true, false)
	if err != nil {
		t.Fatal(err)
	}
	z, err := gzip.NewReader(bytes.NewReader(data))
	if err != nil {
		t.Fatal(err)
	}
	raw, err := io.ReadAll(z)
	if err != nil {
		t.Fatal(err)
	}
	if err := z.Close(); err != nil {
		t.Fatal(err)
	}
	path := filepath.Join(t.TempDir(), "latest.marshal")
	if err := os.WriteFile(path, raw, 0o600); err != nil {
		t.Fatal(err)
	}
	cmd := exec.Command(
		"ruby",
		"-rrubygems",
		"-e",
		`rows=Marshal.load(File.binread(ARGV[0])); raise rows.inspect unless rows.length==1 && rows[0][0]=='sample' && rows[0][1].to_s=='1.0.0'`,
		path,
	)
	if output, err := cmd.CombinedOutput(); err != nil {
		t.Fatalf("Ruby rejected latest stable spec: %v: %s", err, output)
	}
}
