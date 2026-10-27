package pypi_test

import (
	"archive/zip"
	"bytes"
	"context"
	"errors"
	"io"
	"os"
	"strings"
	"testing"

	"github.com/weaveplatform/artifactweave/internal/format/pypi"
	"github.com/weaveplatform/artifactweave/pkg/blob"
)

func read(t *testing.T, name string, body []byte) (*pypi.Dist, error) {
	t.Helper()
	return pypi.ReadDist(bytes.NewReader(body), int64(len(body)), name)
}

func mustRead(t *testing.T, name string, body []byte) *pypi.Dist {
	t.Helper()
	d, err := read(t, name, body)
	if err != nil {
		t.Fatalf("%s: %v", name, err)
	}
	return d
}

func notDist(t *testing.T, what, name string, body []byte) {
	t.Helper()
	if d, err := read(t, name, body); !errors.Is(err, pypi.ErrNotDist) {
		t.Errorf("%s: %+v %v", what, d, err)
	}
}

// Each type's core metadata is read from where the type keeps it: a
// wheel's METADATA, an sdist's PKG-INFO in each of its containers, and an
// egg's EGG-INFO/PKG-INFO. Only a wheel's is core metadata PEP 658 serves.
func TestUnitReadDist(t *testing.T) {
	m := meta("Jfrog.PyPI", "1.0", "Summary:  A summary ", "Requires-Python: >=3.9")
	bz2, err := os.ReadFile("testdata/demo_pkg-1.0.tar.bz2")
	if err != nil {
		t.Fatal(err)
	}
	for _, c := range []struct {
		name string
		body []byte
		meta string
		core bool
	}{
		{"jfrog_pypi-1.0-py3-none-any.whl", wheel("jfrog.pypi", "1.0", m), m, true},
		{"jfrog_pypi-1.0.tar.gz", sdist("jfrog.pypi", "1.0", m), m, false},
		{"jfrog_pypi-1.0.tgz", sdist("jfrog.pypi", "1.0", m), m, false},
		{"jfrog_pypi-1.0.tar", tarOf(false, entry{"jfrog_pypi-1.0/PKG-INFO", m}), m, false},
		{"jfrog_pypi-1.0.zip", zipOf(entry{"jfrog_pypi-1.0/PKG-INFO", m}), m, false},
		{"jfrog_pypi-1.0-py3.8.egg", zipOf(entry{"EGG-INFO/PKG-INFO", m}), m, false},
	} {
		d := mustRead(t, c.name, c.body)
		if d.Name != "Jfrog.PyPI" || d.Version != "1.0" || d.Summary != "A summary" ||
			d.RequiresPython != ">=3.9" || d.Metadata != c.meta || d.Core != c.core {
			t.Errorf("%s: %+v", c.name, d)
		}
	}
	d := mustRead(t, "demo_pkg-1.0.tar.bz2", bz2)
	if d.Name != "Demo.Pkg" || d.Version != "1.0" || d.Summary != "A bzip2 sdist" || d.Core {
		t.Errorf("tar.bz2: %+v", d)
	}
}

// An sdist's PKG-INFO is read at the root, else in the single top-level
// folder, the root preferred; deeper ones, such as the .egg-info's, and a
// folder's beside other folders are not read.
func TestUnitSdistLayouts(t *testing.T) {
	root, folder := meta("root", "1.0"), meta("folder", "1.0")
	for _, c := range []struct {
		what     string
		entries  []entry
		expected string
	}{
		{"root only", []entry{{"PKG-INFO", root}}, "root"},
		{"folder only", []entry{{"x-1.0/", ""}, {"x-1.0/PKG-INFO", folder}}, "folder"},
		{"root after folder", []entry{{"x-1.0/PKG-INFO", folder}, {"PKG-INFO", root}}, "root"},
		{"root before folder", []entry{{"PKG-INFO", root}, {"x-1.0/PKG-INFO", folder}}, "root"},
		{"root files beside the folder", []entry{
			{"pax_global_header", ""}, {"x-1.0/PKG-INFO", folder},
		}, "folder"},
	} {
		for _, body := range [][]byte{tarOf(true, c.entries...), zipOf(c.entries...)} {
			if d := mustRead(t, "x-1.0.tar.gz", body); d.Name != c.expected {
				t.Errorf("%s: %s", c.what, d.Name)
			}
		}
	}
	for what, entries := range map[string][]entry{
		"two folders":            {{"x-1.0/PKG-INFO", folder}, {"y-1.0/README", ""}},
		"two folders, one empty": {{"x-1.0/PKG-INFO", folder}, {"y/", ""}},
		"only deeper":            {{"x-1.0/x.egg-info/PKG-INFO", folder}},
		"none":                   {{"x-1.0/setup.py", ""}},
		"a folder named PKG-INFO": {
			{"PKG-INFO/", ""},
		},
	} {
		notDist(t, what, "x-1.0.tar.gz", tarOf(true, entries...))
	}
}

// A wheel's METADATA is the one in its only top-level .dist-info folder,
// or the one named for its distribution and version among several; a
// METADATA anywhere else is not read.
func TestUnitWheelLayouts(t *testing.T) {
	good, other := meta("good", "1.0"), meta("other", "2.0")
	d := mustRead(t, "Good-1.0-py3-none-any.whl", zipOf(
		entry{"other-2.0.dist-info/METADATA", other},
		entry{"good-1.0.dist-info/METADATA", good},
	))
	if d.Name != "good" {
		t.Errorf("several: %+v", d)
	}
	d = mustRead(t, "good-1.0.0-py3-none-any.whl",
		zipOf(entry{"good-1.0.dist-info/METADATA", good}))
	if d.Name != "good" {
		t.Errorf("one, named otherwise: %+v", d)
	}
	for what, body := range map[string][]byte{
		"several, none named for it": zipOf(
			entry{"a-1.0.dist-info/METADATA", good}, entry{"b-1.0.dist-info/METADATA", good}),
		"nested":            zipOf(entry{"pkg/good-1.0.dist-info/METADATA", good}),
		"at the root":       zipOf(entry{"METADATA", good}),
		"not a dist-info":   zipOf(entry{"good-1.0.data/METADATA", good}),
		"a tar, not a zip":  tarOf(true, entry{"good-1.0.dist-info/METADATA", good}),
		"no archive at all": []byte("just bytes"),
	} {
		notDist(t, what, "good-1.0-py3-none-any.whl", body)
	}
	notDist(
		t,
		"an egg that is no zip",
		"good-1.0.egg",
		tarOf(false, entry{"EGG-INFO/PKG-INFO", good}),
	)
	notDist(
		t,
		"an egg without PKG-INFO",
		"good-1.0.egg",
		zipOf(entry{"EGG-INFO/top_level.txt", ""}),
	)
	notDist(t, "a bzip2 sdist that is not bzip2", "good-1.0.tar.bz2", sdist("good", "1.0", good))
	notDist(t, "a bzip2 sdist too short", "good-1.0.tar.bz2", []byte("BZ"))
	notDist(t, "an sdist that is no archive", "good-1.0.tar.gz", []byte("just bytes"))
	notDist(t, "no distribution type", "good-1.0.exe", wheel("good", "1.0", good))
}

// The metadata must name a valid project and a version the index can hold.
func TestUnitMetadataNamesAndVersions(t *testing.T) {
	for what, m := range map[string]string{
		"no name":             "Metadata-Version: 2.1\nVersion: 1.0\n",
		"no version":          "Metadata-Version: 2.1\nName: x\n",
		"an invalid name":     meta("x y", "1.0"),
		"a version with /":    meta("x", "1.0/../.."),
		"a version of ..":     meta("x", ".."),
		"a version too long":  meta("x", strings.Repeat("1", 257)),
		"a control character": meta("x", "1.0\x01"),
	} {
		notDist(t, what, "x-1.0.zip", zipOf(entry{"PKG-INFO", m}))
	}
	d := mustRead(t, "x-1.0.zip", zipOf(entry{"PKG-INFO", meta("x", "2004d")}))
	if d.Version != "2004d" {
		t.Errorf("a legacy version: %+v", d)
	}
}

// A METADATA past MaxMetadata leaves a wheel indexed by its header block,
// without core metadata; a header block that does not end within it
// leaves the file unindexed.
func TestUnitMetadataSizeLimit(t *testing.T) {
	big := meta("big", "1.0") + strings.Repeat("x", pypi.MaxMetadata)
	d := mustRead(t, wheelName("big", "1.0"), wheel("big", "1.0", big))
	if d.Name != "big" || d.Core ||
		d.Metadata != "Metadata-Version: 2.1\nName: big\nVersion: 1.0\n" {
		t.Errorf("a big METADATA: %+v", d.Name)
	}
	exact := meta("exact", "1.0")
	exact += strings.Repeat("x", pypi.MaxMetadata-len(exact))
	if d := mustRead(t, wheelName("exact", "1.0"), wheel("exact", "1.0", exact)); !d.Core ||
		len(d.Metadata) != pypi.MaxMetadata {
		t.Errorf("a METADATA of exactly the limit: %v %d", d.Core, len(d.Metadata))
	}
	header := "Metadata-Version: 2.1\nName: big\nVersion: 1.0\nSummary: " +
		strings.Repeat("x", pypi.MaxMetadata) + "\n\nbody\n"
	notDist(t, "a header past the limit", wheelName("big", "1.0"), wheel("big", "1.0", header))
	cut := "Metadata-Version: 2.1\nName: big\nVersion: 1.0\n" +
		strings.Repeat("Classifier: x\n", pypi.MaxMetadata/14+1)
	notDist(t, "a header cut at a line's end", wheelName("big", "1.0"), wheel("big", "1.0", cut))
}

// A byte order mark is skipped and kept; text that is not UTF-8 is stored
// with replacement characters, so it is no core metadata to serve.
func TestUnitMetadataEncoding(t *testing.T) {
	bom := "\xef\xbb\xbf" + meta("bom", "1.0")
	d := mustRead(t, wheelName("bom", "1.0"), wheel("bom", "1.0", bom))
	if d.Name != "bom" || d.Metadata != bom || !d.Core {
		t.Errorf("a byte order mark: %+v", d)
	}
	latin := meta("latin", "1.0", "Author: Jos\xe9")
	d = mustRead(t, wheelName("latin", "1.0"), wheel("latin", "1.0", latin))
	if d.Core || !strings.Contains(d.Metadata, "Jos�") {
		t.Errorf("not UTF-8: %+v", d)
	}
}

// Only the header block holds fields, read as Python's email parser reads
// them: continuation lines, names in any case, values repeated, and the
// body starting at the first empty line or the first line that is no
// field.
func TestUnitParseFields(t *testing.T) {
	f := pypi.ParseFields("\xef\xbb\xbfMetadata-Version: 2.1\r\nNAME: x\r\nSummary: one\r\n" +
		"  two\r\nClassifier: A\nClassifier: B\nkey with space: no\nName: y\n")
	if f.Get("name") != "x" || f.Get("Summary") != "one\n  two" ||
		strings.Join(f["classifier"], ",") != "A,B" || f.Get("Missing") != "" {
		t.Errorf("%#v", f)
	}
	if f := pypi.ParseFields(" continued\nName: x\n\nName: y\n"); f.Get("name") != "x" {
		t.Errorf("a leading continuation: %#v", f)
	}
	if f := pypi.ParseFields("Name: x\nno colon here\nVersion: 1\n"); f.Get("version") != "" {
		t.Errorf("a line that is no field ends the block: %#v", f)
	}
	if f := pypi.ParseFields("Name: x\nVersion: 1"); f.Get("version") != "1" {
		t.Errorf("a last line without its end: %#v", f)
	}
}

// failing is a reader whose content fails to read part way: a blob store
// failing mid-read.
type failing struct {
	body []byte
	at   int64
}

var errRead = errors.New("blob store down")

func (f failing) ReadAt(p []byte, off int64) (int, error) {
	if off+int64(len(p)) > f.at {
		return 0, errRead
	}
	return copy(p, f.body[off:]), nil
}

// A failure to read the content is an error, not content that is no
// distribution, so the file is never silently left unindexed.
func TestUnitReadFailure(t *testing.T) {
	body := wheel("x", "1.0", meta("x", "1.0"))
	_, err := pypi.ReadDist(
		failing{body, int64(len(body)) / 2},
		int64(len(body)),
		wheelName("x", "1.0"),
	)
	if !errors.Is(err, errRead) || errors.Is(err, pypi.ErrNotDist) {
		t.Fatalf("%v", err)
	}
}

// blobs serves one content, or fails to open it.
type blobs struct {
	body []byte
	err  error
}

func (b blobs) Open(context.Context, string) (io.ReadSeekCloser, blob.Info, error) {
	if b.err != nil {
		return nil, blob.Info{}, b.err
	}
	return nopCloser{bytes.NewReader(b.body)}, blob.Info{Size: int64(len(b.body))}, nil
}

func (b blobs) Stat(context.Context, string) (blob.Info, error) {
	return blob.Info{Size: int64(len(b.body))}, b.err
}

type nopCloser struct{ *bytes.Reader }

func (nopCloser) Close() error { return nil }

// ReadStored reads stored content, says when it is no distribution, and
// fails when the content cannot be opened or read.
func TestUnitReadStored(t *testing.T) {
	ctx := context.Background()
	body := wheel("x", "1.0", meta("x", "1.0"))
	d, ok, err := pypi.ReadStored(ctx, blobs{body: body}, "sha", wheelName("x", "1.0"))
	if err != nil || !ok || d.Name != "x" {
		t.Fatalf("%+v %v %v", d, ok, err)
	}
	if _, ok, err := pypi.ReadStored(
		ctx,
		blobs{body: []byte("x")},
		"sha",
		"x.whl",
	); ok ||
		err != nil {
		t.Errorf("no distribution: %v %v", ok, err)
	}
	if _, _, err := pypi.ReadStored(
		ctx,
		blobs{err: errRead},
		"sha",
		"x.whl",
	); !errors.Is(
		err,
		errRead,
	) {
		t.Errorf("open: %v", err)
	}
}

// rawZip is a zip of one entry whose stored data is raw, compressed with
// method.
func rawZip(name string, method uint16, raw []byte) []byte {
	var buf bytes.Buffer
	zw := zip.NewWriter(&buf)
	w, _ := zw.CreateRaw(&zip.FileHeader{
		Name: name, Method: method,
		CompressedSize64: uint64(len(raw)), UncompressedSize64: 64,
	})
	_, _ = w.Write(raw)
	_ = zw.Close()
	return buf.Bytes()
}

// A metadata file that cannot be opened or read, an archive that cannot be
// walked, and an entry that would leave the archive's folder are content
// the format cannot index, never a failure of the write.
func TestUnitOddArchives(t *testing.T) {
	garbage := []byte("not deflate data at all")
	for what, c := range map[string]struct {
		name string
		body []byte
	}{
		"a METADATA of an unknown compression": {
			wheelName("x", "1.0"), rawZip("x-1.0.dist-info/METADATA", 99, garbage),
		},
		"an egg PKG-INFO of an unknown compression": {
			"x-1.0.egg", rawZip("EGG-INFO/PKG-INFO", 99, garbage),
		},
		"a corrupt egg PKG-INFO":      {"x-1.0.egg", rawZip("EGG-INFO/PKG-INFO", zip.Deflate, garbage)},
		"an egg that is a broken zip": {"x-1.0.egg", []byte("PK\x03\x04 and nothing after")},
		"a corrupt root PKG-INFO":     {"x-1.0.zip", rawZip("PKG-INFO", zip.Deflate, garbage)},
		"a folder PKG-INFO of an unknown compression": {
			"x-1.0.zip", rawZip("x-1.0/PKG-INFO", 99, garbage),
		},
	} {
		notDist(t, what, c.name, c.body)
	}
	d := mustRead(t, "x-1.0.tar.gz", tarOf(true,
		entry{"../PKG-INFO", meta("escapee", "1.0")}, entry{"x-1.0/PKG-INFO", meta("x", "1.0")}))
	if d.Name != "x" {
		t.Errorf("an entry leaving the folder was read: %+v", d)
	}
}
