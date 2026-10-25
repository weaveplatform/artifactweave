package gems

import (
	"bytes"
	"compress/gzip"
	"context"
	"crypto/sha256"
	"encoding/hex"
	"errors"
	"io"
	"net/http"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"

	"github.com/weaveplatform/artifactweave/internal/format"
	"github.com/weaveplatform/artifactweave/pkg/blob"
	"github.com/weaveplatform/artifactweave/pkg/repo"
	"github.com/weaveplatform/artifactweave/pkg/store/inmem"
	"github.com/weaveplatform/artifactweave/pkg/types"
)

var errMissingGemBlob = errors.New("missing gem blob")

type gemBytes struct{ *bytes.Reader }

func (*gemBytes) Close() error { return nil }

type testGemBlobs struct {
	sha  string
	body []byte
}

func (b testGemBlobs) Open(_ context.Context, sha string) (io.ReadSeekCloser, blob.Info, error) {
	if sha != b.sha {
		return nil, blob.Info{}, errMissingGemBlob
	}
	return &gemBytes{bytes.NewReader(b.body)}, blob.Info{Size: int64(len(b.body))}, nil
}

func (b testGemBlobs) Stat(_ context.Context, sha string) (blob.Info, error) {
	if sha != b.sha {
		return blob.Info{}, errMissingGemBlob
	}
	return blob.Info{Size: int64(len(b.body))}, nil
}

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

func TestUnitCompactNamesDeduplicatesAcrossVersions(t *testing.T) {
	packages := []Package{
		{Name: "zeta", Version: "2.0.0"},
		{Name: "rake", Version: "12.3.1"},
		{Name: "zeta", Version: "1.0.0"},
	}
	if got, want := string(CompactNames(packages)), "---\nrake\nzeta\n"; got != want {
		t.Fatalf("compact names list must be sorted and unique: got %q, want %q", got, want)
	}
}

func TestUnitCompactInfoRejectsIncompleteOrUnsafeRequirements(t *testing.T) {
	sha := strings.Repeat("a", 64)
	for name, body := range map[string]string{
		"missing checksum":              "---\n1.0.0 |ruby:>= 3.0\n",
		"invalid checksum hex":          "---\n1.0.0 |checksum:" + strings.Repeat("z", 64) + "\n",
		"invalid dependency name":       "---\n1.0.0 bad/name:>= 1|checksum:" + sha + "\n",
		"missing dependency separator":  "---\n1.0.0 baddependency|checksum:" + sha + "\n",
		"missing requirement separator": "---\n1.0.0 |checksum:" + sha + ",ruby>=3\n",
		"noncanonical platform":         "---\n1.0.0-.. |checksum:" + sha + "\n",
	} {
		t.Run(name, func(t *testing.T) {
			if _, err := ParseCompactInfo("sample", []byte(body)); err == nil {
				t.Fatalf("accepted unsafe compact info %q", body)
			}
		})
	}
}

func TestUnitGemAdmissionProtectsGeneratedIndexesAndArchiveIdentity(t *testing.T) {
	content, err := os.ReadFile("testdata/rake-12.3.1.gem")
	if err != nil {
		t.Fatal(err)
	}
	digest := sha256.Sum256(content)
	sha := hex.EncodeToString(digest[:])
	c := repo.Config{Key: "gems-local", Class: repo.Local, PackageType: repo.Gems}
	env := format.Env{Blobs: testGemBlobs{sha: sha, body: content}}
	h := Handler{}
	write := format.Write{
		Op:        format.OpDeploy,
		Path:      "gems/rake-12.3.1.gem",
		Checksums: types.Checksums{SHA256: sha},
	}

	if !Serves(repo.Gems) || Serves(repo.NPM) {
		t.Fatal("RubyGems handler claimed the wrong package type")
	}
	inspected, err := h.Inspect(t.Context(), env, c, write)
	if err != nil {
		t.Fatal(err)
	}
	env.Inspection = inspected
	if _, err := h.Admit(t.Context(), env, c, write); err != nil {
		t.Fatalf("valid pinned gem was refused: %v", err)
	}
	if h.Classify(c, "names") != format.ClassDerived ||
		h.Classify(c, "info/rake") != format.ClassDerived ||
		h.Classify(c, write.Path) != format.ClassUnique {
		t.Fatal("generated index classification differs from gem archive")
	}

	for _, tc := range []struct {
		name   string
		write  format.Write
		status int
	}{
		{"generated compact index", format.Write{Op: format.OpDeploy, Path: "versions"}, http.StatusConflict},
		{"generated quick spec", format.Write{Op: format.OpDeploy, Path: "quick/Marshal.4.8/rake-12.3.1.gemspec.rz"}, http.StatusConflict},
		{"unsafe archive path", format.Write{Op: format.OpDeploy, Path: "gems/../escape.gem"}, http.StatusBadRequest},
		{"archive identity mismatch", format.Write{Op: format.OpDeploy, Path: "gems/rake-12.3.2.gem", Checksums: types.Checksums{SHA256: sha}}, http.StatusBadRequest},
	} {
		t.Run(tc.name, func(t *testing.T) {
			_, err := h.Admit(t.Context(), env, c, tc.write)
			var rejected *format.Rejection
			if !errors.As(err, &rejected) || rejected.Status != tc.status {
				t.Fatalf("admission = %v, want HTTP %d rejection", err, tc.status)
			}
		})
	}

	missing := write
	missing.Checksums.SHA256 = ""
	if _, err := h.Inspect(t.Context(), env, c, missing); err == nil {
		t.Fatal("gem without content passed preflight inspection")
	}
	checksum := format.Write{Op: format.OpChecksum, Path: write.Path}
	if _, err := h.Inspect(t.Context(), env, c, checksum); err != nil {
		t.Fatalf("checksum sidecar was inspected as an archive: %v", err)
	}
	if _, err := h.Admit(t.Context(), env, c, checksum); err != nil {
		t.Fatalf("checksum sidecar was refused: %v", err)
	}
	for _, item := range []types.Item{{Folder: true}, {Name: "README.txt"}} {
		if err := h.Deployed(t.Context(), env, c, item); err != nil {
			t.Fatalf("non-gem item changed the native index: %v", err)
		}
	}
	if err := h.Deployed(t.Context(), env, c, types.Item{
		Name: write.Path, Actual: types.Checksums{SHA256: strings.Repeat("f", 64)},
	}); err == nil {
		t.Fatal("missing stored gem content was indexed")
	}
}

func TestAccGemReindexRebuildsNativePackageLinkFromStoredArchive(t *testing.T) {
	content, err := os.ReadFile("testdata/rake-12.3.1.gem")
	if err != nil {
		t.Fatal(err)
	}
	digest := sha256.Sum256(content)
	sha := hex.EncodeToString(digest[:])
	db := inmem.New()
	c := *repo.Defaults(&repo.Config{Key: "gems-local", Class: repo.Local, PackageType: repo.Gems})
	if err := db.Repos().Create(t.Context(), c); err != nil {
		t.Fatal(err)
	}
	item, err := db.Items().Put(t.Context(), types.Item{
		RepoKey: c.Key, Name: "gems/rake-12.3.1.gem", Size: int64(len(content)),
		Actual: types.Checksums{SHA256: sha},
	})
	if err != nil {
		t.Fatal(err)
	}
	env := format.Env{Store: db, Blobs: testGemBlobs{sha: sha, body: content}}
	h := Handler{}
	if err := h.Deployed(t.Context(), env, c, item); err != nil {
		t.Fatal(err)
	}
	indexed, err := db.Packages().Find(t.Context(), c.Key, "rake")
	if err != nil {
		t.Fatal(err)
	}
	version, err := db.Packages().Version(t.Context(), indexed.ID, "12.3.1")
	if err != nil {
		t.Fatal(err)
	}
	files, err := db.Packages().Files(t.Context(), version.ID)
	if err != nil || len(files) != 1 || files[0] != item.ID {
		t.Fatalf("native package link is not the stored archive: %v, %v", files, err)
	}
	if err := db.Packages().Delete(t.Context(), indexed.ID); err != nil {
		t.Fatal(err)
	}
	if err := h.Reindex(t.Context(), env, c); err != nil {
		t.Fatal(err)
	}
	indexed, err = db.Packages().Find(t.Context(), c.Key, "rake")
	if err != nil {
		t.Fatalf("reindex did not restore package: %v", err)
	}
	version, err = db.Packages().Version(t.Context(), indexed.ID, "12.3.1")
	if err != nil {
		t.Fatal(err)
	}
	files, err = db.Packages().Files(t.Context(), version.ID)
	if err != nil || len(files) != 1 || files[0] != item.ID {
		t.Fatalf("reindex changed the archive identity: %v, %v", files, err)
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
