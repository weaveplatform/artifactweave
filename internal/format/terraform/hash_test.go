package terraform_test

import (
	"archive/zip"
	"bytes"
	"errors"
	"io"
	"strings"
	"testing"

	"github.com/weaveplatform/artifactweave/internal/format/terraform"
)

// Terraform's own example (v1.16.4, internal/getproviders,
// TestPackageHashAuthentication_success): its testdata unpacked package of
// hashicorp/null 2.0.0 for linux_amd64, one 85-byte placeholder file,
// hashes to this h1:. An archive of that file hashes the same, since h1:
// is over the entries' names and contents.
func TestUnitH1MatchesTerraformsExample(t *testing.T) {
	body := zipOf(t, [2]string{
		"terraform-provider-null",
		"# This is just a placeholder file for discovery testing, not a real provider plugin.\n",
	})
	got, ok, err := terraform.H1(bytes.NewReader(body), int64(len(body)))
	if want := "h1:qjsREM4DqEWECD43FcPqddZ9oxCG+IaMTxvWPciS05g="; got != want || !ok || err != nil {
		t.Fatalf("%s %v %v, want %s", got, ok, err, want)
	}
}

// H1 is what Terraform computes, dirhash.HashZip over the file, for zips
// with folders, folder entries of their own, several files and none.
func TestUnitH1IsTerraformsHash(t *testing.T) {
	withDir := func() []byte {
		var b bytes.Buffer
		z := zip.NewWriter(&b)
		for _, name := range []string{"docs/", "docs/index.md", "bin/run", "LICENSE"} {
			w, err := z.Create(name)
			if err != nil {
				t.Fatal(err)
			}
			if !strings.HasSuffix(name, "/") {
				_, _ = io.WriteString(w, "content of "+name)
			}
		}
		if err := z.Close(); err != nil {
			t.Fatal(err)
		}
		return b.Bytes()
	}()
	for name, body := range map[string][]byte{
		"folders":  withDir,
		"two":      zipOf(t, [2]string{"b", "2"}, [2]string{"a", "1"}),
		"empty":    zipOf(t),
		"provider": zipOf(t, [2]string{"terraform-provider-null_v3.3.2_x5", strings.Repeat("x", 1<<16)}),
	} {
		got, ok, err := terraform.H1(bytes.NewReader(body), int64(len(body)))
		if want := terraformH1(t, body); got != want || !ok || err != nil {
			t.Errorf("%s: %s %v %v, want %s", name, got, ok, err, want)
		}
	}
}

// Content that is no zip, a zip whose entry cannot be inflated, a name the
// hash refuses, and a zip that inflates past the limit have no hash, and
// that is no failure.
func TestUnitH1OfWhatIsNoZip(t *testing.T) {
	good := zipOf(t, [2]string{"a", strings.Repeat("compressible ", 1000)})
	broken := bytes.Clone(good)
	// The entry's deflate stream starts after its 30-byte local header and
	// its one-byte name.
	for i := 31; i < 60; i++ {
		broken[i] ^= 0xff
	}
	newline := zipOf(t, [2]string{"a\nb", "x"})
	// An entry compressed by a method zip does not know cannot be opened.
	var method bytes.Buffer
	z := zip.NewWriter(&method)
	if _, err := z.CreateRaw(&zip.FileHeader{Name: "a", Method: 99}); err != nil {
		t.Fatal(err)
	}
	if err := z.Close(); err != nil {
		t.Fatal(err)
	}
	for name, body := range map[string][]byte{
		"text": []byte("not a zip"), "broken": broken, "newline": newline, "method": method.Bytes(),
	} {
		got, ok, err := terraform.H1(bytes.NewReader(body), int64(len(body)))
		if got != "" || ok || err != nil {
			t.Errorf("%s: %q %v %v", name, got, ok, err)
		}
	}
	defer terraform.SetMaxInflated(100)()
	got, ok, err := terraform.H1(bytes.NewReader(good), int64(len(good)))
	if got != "" || ok || err != nil {
		t.Fatalf("past the limit: %q %v %v", got, ok, err)
	}
}

var errDisk = errors.New("disk")

// failingAt fails every read that touches the bytes from from to to.
type failingAt struct {
	r        io.ReaderAt
	from, to int64
}

func (f failingAt) ReadAt(p []byte, off int64) (int, error) {
	if off < f.to && off+int64(len(p)) > f.from {
		return 0, errDisk
	}
	return f.r.ReadAt(p, off) //nolint:wrapcheck // test double
}

// A store that cannot be read is a failure, not content without a hash,
// whether the central directory or an entry cannot be read.
func TestUnitH1ReadFailure(t *testing.T) {
	body := zipOf(t, [2]string{"a", strings.Repeat("x", 4096)})
	for _, bad := range [][2]int64{{0, int64(len(body))}, {31, 40}} {
		_, ok, err := terraform.H1(
			failingAt{bytes.NewReader(body), bad[0], bad[1]},
			int64(len(body)),
		)
		if ok || !errors.Is(err, errDisk) {
			t.Errorf("failing %v: %v %v", bad, ok, err)
		}
	}
}
