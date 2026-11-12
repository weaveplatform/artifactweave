package luarocks

import (
	"archive/zip"
	"bytes"
	"context"
	"errors"
	"io"
	"net/http"
	"testing"

	"github.com/weaveplatform/artifactweave/internal/artifacts/core/format"
)

var errRead = errors.New("the store dropped the connection")

// cutAt is content with ReadAt, as a file store's is, that serves its first
// sound reads and fails every later one with errRead, as a store whose
// connection drops does.
type cutAt struct {
	*bytes.Reader
	sound int
}

func (c *cutAt) ReadAt(p []byte, off int64) (int, error) {
	if c.sound == 0 {
		return 0, errRead
	}
	c.sound--
	return c.Reader.ReadAt(p, off)
}

// cutSeek is content that can only seek, as S3's, GCS's and Azure's is,
// whose first sound reads are served and every later one fails.
type cutSeek struct {
	r     *bytes.Reader
	sound int
}

func (c *cutSeek) Read(p []byte) (int, error) {
	if c.sound == 0 {
		return 0, errRead
	}
	c.sound--
	return c.r.Read(p)
}

func (c *cutSeek) Seek(off int64, whence int) (int64, error) { return c.r.Seek(off, whence) }

// rockOf is a rock holding entries in order, each written with method.
func rockOf(t *testing.T, method uint16, entries ...[2]string) []byte {
	t.Helper()
	var b bytes.Buffer
	z := zip.NewWriter(&b)
	for _, e := range entries {
		w, err := z.CreateHeader(&zip.FileHeader{Name: e[0], Method: method})
		if err != nil {
			t.Fatal(err)
		}
		if _, err := io.WriteString(w, e[1]); err != nil {
			t.Fatal(err)
		}
	}
	if err := z.Close(); err != nil {
		t.Fatal(err)
	}
	return b.Bytes()
}

const helloSpec = "package = 'hello'\nversion = '1.0-1'\n"

// A rock whose content fails a read is no bad rock: wherever the read
// fails, the sniff, the ZIP's directory, the embedded rockspec or another
// entry, on a store with ReadAt or one that can only seek, ValidateContent
// returns the read's error, never a refusal, until the reads reach the end
// and the rock passes.
func TestUnitValidateContentReturnsAFailedRead(t *testing.T) {
	ctx := context.Background()
	id, _ := Parse("hello-1.0-1.src.rock")
	for _, method := range []uint16{zip.Store, zip.Deflate} {
		body := rockOf(t, method,
			[2]string{"hello-1.0-1.rockspec", helloSpec},
			[2]string{"hello.lua", "return 'hello'"},
		)
		for name, content := range map[string]func(sound int) io.ReadSeeker{
			"ReadAt":    func(sound int) io.ReadSeeker { return &cutAt{bytes.NewReader(body), sound} },
			"seek only": func(sound int) io.ReadSeeker { return &cutSeek{bytes.NewReader(body), sound} },
		} {
			failed := 0
			for sound := 0; ; sound++ {
				err := ValidateContent(ctx, content(sound), int64(len(body)), id)
				if err == nil {
					break
				}
				if !errors.Is(err, errRead) || errors.As(err, new(*format.RejectionError)) {
					t.Fatalf("method %d %s, %d sound reads: %v", method, name, sound, err)
				}
				failed++
			}
			// The sniff, the directory, and the reads of both entries each
			// failed once at least.
			if failed < 4 {
				t.Fatalf("method %d %s: only %d reads failed", method, name, failed)
			}
		}
	}
}

// Content that breaks the rules is still refused 400, with the message it
// was refused with before ValidateContent told a failed read from it.
func TestUnitValidateContentStillRefusesBadRocks(t *testing.T) {
	ctx := context.Background()
	id, _ := Parse("hello-1.0-1.src.rock")
	spec := [2]string{"hello-1.0-1.rockspec", helloSpec}
	corrupt := rockOf(t, zip.Store, spec, [2]string{"hello.lua", "uncorrupted Lua content"})
	corrupt[bytes.Index(corrupt, []byte("uncorrupted"))] ^= 1 // the directory keeps the old CRC
	var unknown bytes.Buffer
	z := zip.NewWriter(&unknown)
	w, err := z.CreateRaw(&zip.FileHeader{
		Name: "hello.lua", Method: 99, CompressedSize64: 1, UncompressedSize64: 1,
	})
	if err != nil {
		t.Fatal(err)
	}
	if _, err := io.WriteString(w, "x"); err != nil {
		t.Fatal(err)
	}
	if err := z.Close(); err != nil {
		t.Fatal(err)
	}
	const invalid = "Unsafe or invalid LuaRocks archive: "
	for name, tc := range map[string]struct {
		body    []byte
		message string
	}{
		"no ZIP": {[]byte("not a ZIP"), "LuaRocks rock must be a ZIP archive"},
		"corrupt directory": {
			[]byte("PK\x03\x04garbage"),
			invalid + "archive: unusable archive: zip: not a valid zip file",
		},
		"corrupt entry": {corrupt, invalid + "luarocks: read archive entry: zip: checksum error"},
		"unknown method": {
			unknown.Bytes(),
			invalid + "luarocks: open archive entry: zip: unsupported compression algorithm",
		},
		"escaping entry": {
			rockOf(t, zip.Store, spec, [2]string{"../evil.lua", "x"}),
			invalid + `luarocks: archive path: archive: unusable archive: the entry "../evil.lua" ` +
				"leaves the archive's folder",
		},
		"no rockspec": {
			rockOf(t, zip.Store, [2]string{"hello.lua", "x"}),
			"LuaRocks rock contains no matching rockspec",
		},
	} {
		err := ValidateContent(ctx, bytes.NewReader(tc.body), int64(len(tc.body)), id)
		var refused *format.RejectionError
		if !errors.As(err, &refused) || refused.Status != http.StatusBadRequest ||
			refused.Message != tc.message {
			t.Errorf("%s: %v, want 400 %q", name, err, tc.message)
		}
	}
}
