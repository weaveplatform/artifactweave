package p2

import (
	"archive/zip"
	"bytes"
	"encoding/binary"
	"errors"
	"io"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/ulikunitz/xz"
)

// The files are published Eclipse GLSP 2.7.0 update-site bytes, downloaded
// from https://download.eclipse.org/glsp/server/p2/releases/2.7.0/ . Their
// hashes and retrieval date are recorded in docs/testing/p2.md.
func TestUnitPublishedEclipseMetadata(t *testing.T) {
	for _, name := range []string{"p2.index", "content.jar", "artifacts.jar", "content.xml.xz", "artifacts.xml.xz"} {
		body, err := os.ReadFile(filepath.Join("testdata", name))
		if err != nil {
			t.Fatal(err)
		}
		if err := Validate(name, bytes.NewReader(body), int64(len(body))); err != nil {
			t.Errorf("published %s: %v", name, err)
		}
		if name == "content.jar" {
			corrupt := bytes.Clone(body)
			corrupt[len(corrupt)/2] ^= 0xff
			if err := Validate(
				name,
				bytes.NewReader(corrupt),
				int64(len(corrupt)),
			); !errors.Is(
				err,
				ErrInvalid,
			) {
				t.Errorf("corrupt JAR accepted: %v", err)
			}
		}
	}
	plugin := "org.eclipse.glsp.layout.source_2.7.0.jar"
	body, err := os.ReadFile(filepath.Join("testdata", plugin))
	if err != nil {
		t.Fatal(err)
	}
	if err := Validate("plugins/"+plugin, bytes.NewReader(body), int64(len(body))); err != nil {
		t.Errorf("published Eclipse plugin: %v", err)
	}
}

func TestUnitPublishedP2ArtifactChecksum(t *testing.T) {
	body, err := os.ReadFile(filepath.Join("testdata", "artifacts.jar"))
	if err != nil {
		t.Fatal(err)
	}
	checksums, err := ArtifactChecksums("artifacts.jar", body,
		"plugins/org.eclipse.glsp.layout.source_2.7.0.jar")
	if err != nil ||
		checksums.SHA256 != "80c9be399f166cddc9934a42e27484e6427684bf8adf398fbfeb8224a189a728" {
		t.Fatalf("published Eclipse checksum: %+v, %v", checksums, err)
	}
	if _, err := ArtifactChecksums(
		"artifacts.jar",
		body,
		"plugins/missing.jar",
	); !errors.Is(
		err,
		ErrInvalid,
	) {
		t.Fatalf("missing artifact admitted: %v", err)
	}
}

func TestUnitP2MetadataEncodingsAndBounds(t *testing.T) {
	for _, pair := range []struct{ archive, entry string }{
		{"content.jar", "content.xml"},
		{"artifacts.jar", "artifacts.xml"},
	} {
		body, err := os.ReadFile(filepath.Join("testdata", pair.archive))
		if err != nil {
			t.Fatal(err)
		}
		zr, err := zip.NewReader(bytes.NewReader(body), int64(len(body)))
		if err != nil {
			t.Fatal(err)
		}
		r, err := zr.File[0].Open()
		if err != nil {
			t.Fatal(err)
		}
		xmlBody, err := io.ReadAll(r)
		_ = r.Close()
		if err != nil {
			t.Fatal(err)
		}
		if err := Validate(pair.entry, bytes.NewReader(xmlBody), int64(len(xmlBody))); err != nil {
			t.Errorf("plain published %s: %v", pair.entry, err)
		}
		if err := Validate(
			pair.archive,
			readSeekerOnly{bytes.NewReader(body)},
			int64(len(body)),
		); err != nil {
			t.Errorf("published %s through ReadSeeker: %v", pair.archive, err)
		}
	}
	for _, tc := range []struct {
		name string
		body []byte
		size int64
	}{
		{"content.xml.xz", []byte("bad xz"), 6},
		{"content.jar", []byte("bad zip"), 7},
		{"plugins/demo.jar", []byte("bad zip"), 7},
		{"binary/demo", []byte("x"), MaxArtifact + 1},
		{"p2.index", []byte("version=1\n"), 10},
		{"p2.index", []byte("version=1\nmetadata.repository.factory.order=content.xml,!\n"), 65},
	} {
		if err := Validate(
			tc.name,
			bytes.NewReader(tc.body),
			tc.size,
		); !errors.Is(
			err,
			ErrInvalid,
		) {
			t.Errorf("invalid %s accepted: %v", tc.name, err)
		}
	}
	if err := Validate("binary/demo", bytes.NewReader([]byte("x")), 1); err != nil {
		t.Errorf("binary artifact rejected: %v", err)
	}
}

type readSeekerOnly struct{ io.ReadSeeker }

func TestUnitExtractsArchiveEntriesAndRejectsUntrustedPaths(t *testing.T) {
	index, err := os.ReadFile(filepath.Join("testdata", "p2.index"))
	if err != nil {
		t.Fatal(err)
	}
	plugin, err := os.ReadFile(
		filepath.Join("testdata", "org.eclipse.glsp.layout.source_2.7.0.jar"),
	)
	if err != nil {
		t.Fatal(err)
	}
	var archive bytes.Buffer
	zw := zip.NewWriter(&archive)
	for name, body := range map[string][]byte{
		"p2.index": index,
		"plugins/org.eclipse.glsp.layout.source_2.7.0.jar": plugin,
	} {
		f, err := zw.Create(name)
		if err != nil {
			t.Fatal(err)
		}
		if _, err := f.Write(body); err != nil {
			t.Fatal(err)
		}
	}
	if err := zw.Close(); err != nil {
		t.Fatal(err)
	}
	for name, want := range map[string][]byte{
		"p2.index": index,
		"plugins/org.eclipse.glsp.layout.source_2.7.0.jar": plugin,
	} {
		got, err := ExtractArchive(archive.Bytes(), name)
		if err != nil || !bytes.Equal(got, want) {
			t.Errorf("archive %s: %v, size %d", name, err, len(got))
		}
	}
	for _, name := range []string{"missing.jar", "../p2.index", "plugins/../p2.index"} {
		if _, err := ExtractArchive(archive.Bytes(), name); !errors.Is(err, ErrInvalid) {
			t.Errorf("unsafe archive entry %s accepted: %v", name, err)
		}
	}
	if _, err := ExtractArchive([]byte("not a zip"), "p2.index"); !errors.Is(err, ErrInvalid) {
		t.Errorf("corrupt archive accepted: %v", err)
	}
	var malicious bytes.Buffer
	zw = zip.NewWriter(&malicious)
	f, err := zw.Create("../private")
	if err != nil {
		t.Fatal(err)
	}
	if _, err := f.Write([]byte("secret")); err != nil {
		t.Fatal(err)
	}
	if err := zw.Close(); err != nil {
		t.Fatal(err)
	}
	if _, err := ExtractArchive(malicious.Bytes(), "p2.index"); !errors.Is(err, ErrInvalid) {
		t.Errorf("traversal archive accepted: %v", err)
	}
}

func TestUnitRejectsUnsafeP2MetadataAndPaths(t *testing.T) {
	for _, name := range []string{"", "../content.jar", "/content.xml", "other.xml", "plugins/../bundle.jar", "plugins/bundle.zip", "content.jar?x=1"} {
		if got := Classify(name); got != KindInvalid {
			t.Errorf("%q classified %d", name, got)
		}
	}
	for _, tc := range []struct{ name, body string }{
		{"p2.index", "version=1\nmetadata.repository.factory.order=https://outside.example,!\n"},
		{"content.xml", "<wrong/>"},
		{"content.xml", "<!DOCTYPE repository [<!ENTITY x SYSTEM 'file:///etc/passwd'>]><repository/>"},
		{"content.xml", `<repository type="wrong"><units size="0"/></repository>`},
		{"compositeContent.xml", `<repository type="org.eclipse.equinox.internal.p2.metadata.repository.LocalMetadataRepository"><children size="1"><child location="one"/></children></repository>`},
		{"compositeContent.xml", `<repository><children size="1"><child location="../hidden"/></children></repository>`},
		{"content.xml", "<repository"},
	} {
		if err := Validate(
			tc.name,
			bytes.NewReader([]byte(tc.body)),
			int64(len(tc.body)),
		); !errors.Is(
			err,
			ErrInvalid,
		) {
			t.Errorf("unsafe %s accepted: %v", tc.name, err)
		}
	}
	validComposite := `<repository type="org.eclipse.equinox.internal.p2.metadata.repository.CompositeMetadataRepository"><children size="1"><child location="child"/></children></repository>`
	if err := Validate(
		"compositeContent.xml",
		bytes.NewReader([]byte(validComposite)),
		int64(len(validComposite)),
	); err != nil {
		t.Errorf("safe composite rejected: %v", err)
	}
	if err := validateXML(
		"content.xml",
		[]byte("<?xml version='1.0'?>"),
	); !errors.Is(
		err,
		ErrInvalid,
	) {
		t.Errorf("rootless XML accepted: %v", err)
	}
}

func TestUnitExternalCompositeEditingBoundsAndEncodings(t *testing.T) {
	for _, raw := range []string{
		"../private", "%2e%2e/private", "//outside.example", "https://user:pass@child.example/site",
		"https://child.example/%2e%2e/private", "https://child.example/site#fragment",
		"javascript:alert(1)", strings.Repeat("x", MaxChildURL+1),
	} {
		if ValidChildLocation(raw) {
			t.Errorf("unsafe child location admitted: %q", raw)
		}
	}
	content := []byte(
		`<?xml version="1.0"?><repository type="org.eclipse.equinox.internal.p2.metadata.repository.CompositeMetadataRepository"><children size="1"><child location="https://child.example/site?token=opaque"/></children></repository>`,
	)
	for _, name := range []string{"compositeContent.xml", "compositeContent.jar", "compositeContent.xml.xz"} {
		body := content
		if strings.HasSuffix(name, ".jar") {
			var packed bytes.Buffer
			w := zip.NewWriter(&packed)
			f, err := w.Create("compositeContent.xml")
			if err != nil {
				t.Fatal(err)
			}
			_, _ = f.Write(content)
			if err := w.Close(); err != nil {
				t.Fatal(err)
			}
			body = packed.Bytes()
		} else if strings.HasSuffix(name, ".xz") {
			var packed bytes.Buffer
			w, err := xz.NewWriter(&packed)
			if err != nil {
				t.Fatal(err)
			}
			_, _ = w.Write(content)
			if err := w.Close(); err != nil {
				t.Fatal(err)
			}
			body = packed.Bytes()
		}
		out, err := EditComposite(name, body, func(raw string) (string, error) {
			if raw != "https://child.example/site?token=opaque" {
				t.Fatalf("raw child %q", raw)
			}
			return "__p2_ext/opaque", nil
		})
		if err != nil || bytes.Contains(out, []byte("child.example")) ||
			Validate(name, bytes.NewReader(out), int64(len(out))) != nil {
			t.Errorf("%s rewrite: %v", name, err)
		}
	}
	for _, raw := range []string{
		`<child location="https://child.example/../private"/>`,
		`<child location="https://child.example/site" location="https://other.example"/>`,
	} {
		bad := []byte(
			`<repository type="org.eclipse.equinox.internal.p2.metadata.repository.CompositeMetadataRepository"><children size="1">` + raw + `</children></repository>`,
		)
		if _, err := EditComposite(
			"compositeContent.xml",
			bad,
			func(string) (string, error) { return "ok", nil },
		); err == nil {
			t.Errorf("unsafe child metadata accepted: %s", raw)
		}
	}
}

func TestUnitP2ArchiveRejectsInvalidEntryAndSeekFailure(t *testing.T) {
	if _, err := ExtractArchive([]byte("data"), "../p2.index"); !errors.Is(err, ErrInvalid) {
		t.Errorf("unsafe archive entry accepted: %v", err)
	}
	if err := Validate("content.jar", failingSeeker{}, 1); !errors.Is(err, ErrInvalid) {
		t.Errorf("unreadable metadata accepted: %v", err)
	}
	if _, err := (seekReaderAt{failingSeeker{}}).ReadAt(
		make([]byte, 1),
		0,
	); !errors.Is(
		err,
		io.ErrUnexpectedEOF,
	) {
		t.Errorf("seek error lost: %v", err)
	}
}

func TestUnitP2IndexRejectsIncompleteAndMisleadingOrders(t *testing.T) {
	for _, body := range []string{
		"version=1\nmetadata.repository.factory.order=content.xml\n",
		"version=2\nmetadata.repository.factory.order=content.xml\nartifact.repository.factory.order=artifacts.xml\n",
		"version=1\nmetadata.repository.factory.order=content.xml\nartifact.repository.factory.order=https://outside.example\n",
		"version=1\nmetadata.repository.factory.order=content.xml\nartifact.repository.factory.order=artifacts.xml\nunknown=1\n",
		"version=1\nmetadata.repository.factory.order\nartifact.repository.factory.order=artifacts.xml\n",
	} {
		if err := Validate(
			"p2.index",
			bytes.NewReader([]byte(body)),
			int64(len(body)),
		); !errors.Is(
			err,
			ErrInvalid,
		) {
			t.Errorf("invalid index accepted: %q: %v", body, err)
		}
	}
	if err := Validate(
		"p2.index",
		bytes.NewReader([]byte("x")),
		64<<10+1,
	); !errors.Is(
		err,
		ErrInvalid,
	) {
		t.Errorf("oversized index accepted: %v", err)
	}
}

func TestUnitP2RejectsBrokenArchiveEntries(t *testing.T) {
	for _, name := range []string{"directory selected", "unsafe archive path", "malformed metadata"} {
		t.Run(name, func(t *testing.T) {
			var archive bytes.Buffer
			zw := zip.NewWriter(&archive)
			add := func(entry string, body []byte) {
				t.Helper()
				f, err := zw.Create(entry)
				if err != nil {
					t.Fatal(err)
				}
				if _, err := f.Write(body); err != nil {
					t.Fatal(err)
				}
			}
			switch name {
			case "directory selected":
				h := &zip.FileHeader{Name: "p2.index", Method: zip.Store}
				h.SetMode(os.ModeDir | 0o755)
				if _, err := zw.CreateHeader(h); err != nil {
					t.Fatal(err)
				}
			case "unsafe archive path":
				add(
					"p2.index",
					[]byte(
						"version=1\nmetadata.repository.factory.order=content.xml\nartifact.repository.factory.order=artifacts.xml\n",
					),
				)
				add(`plugins\secret.jar`, []byte("bad"))
			case "malformed metadata":
				add("p2.index", []byte("version=1\n"))
			}
			if err := zw.Close(); err != nil {
				t.Fatal(err)
			}
			if _, err := ExtractArchive(archive.Bytes(), "p2.index"); !errors.Is(err, ErrInvalid) {
				t.Fatalf("invalid archive entry accepted: %v", err)
			}
		})
	}
	var empty bytes.Buffer
	if err := zip.NewWriter(&empty).Close(); err != nil {
		t.Fatal(err)
	}
	if _, err := ExtractArchive(empty.Bytes(), "p2.index"); !errors.Is(err, ErrInvalid) {
		t.Errorf("empty archive accepted: %v", err)
	}
	var missing bytes.Buffer
	zw := zip.NewWriter(&missing)
	f, err := zw.Create("other.txt")
	if err != nil {
		t.Fatal(err)
	}
	if _, err := f.Write([]byte("x")); err != nil {
		t.Fatal(err)
	}
	if err := zw.Close(); err != nil {
		t.Fatal(err)
	}
	if err := Validate(
		"content.jar",
		bytes.NewReader(missing.Bytes()),
		int64(missing.Len()),
	); !errors.Is(
		err,
		ErrInvalid,
	) {
		t.Errorf("metadata JAR without content.xml accepted: %v", err)
	}
}

func TestUnitP2RejectsReadFailures(t *testing.T) {
	if err := Validate(
		"content.xml",
		readErrorSeeker{bytes.NewReader([]byte("x"))},
		1,
	); !errors.Is(
		err,
		ErrInvalid,
	) {
		t.Errorf("unreadable XML accepted: %v", err)
	}
	compressed, err := os.ReadFile(filepath.Join("testdata", "content.xml.xz"))
	if err != nil {
		t.Fatal(err)
	}
	if err := Validate(
		"content.xml.xz",
		bytes.NewReader(compressed[:len(compressed)/2]),
		int64(len(compressed)/2),
	); !errors.Is(
		err,
		ErrInvalid,
	) {
		t.Errorf("truncated XZ metadata accepted: %v", err)
	}
	if _, err := (seekReaderAt{readErrorSeeker{bytes.NewReader([]byte("x"))}}).ReadAt(
		make([]byte, 1),
		0,
	); !errors.Is(
		err,
		io.ErrUnexpectedEOF,
	) {
		t.Errorf("JAR read error lost: %v", err)
	}
}

func TestUnitP2ArchiveRejectsBadCRCAndUnknownCompression(t *testing.T) {
	index, err := os.ReadFile(filepath.Join("testdata", "p2.index"))
	if err != nil {
		t.Fatal(err)
	}
	var archive bytes.Buffer
	zw := zip.NewWriter(&archive)
	f, err := zw.CreateHeader(&zip.FileHeader{Name: "p2.index", Method: zip.Store})
	if err != nil {
		t.Fatal(err)
	}
	if _, err := f.Write(index); err != nil {
		t.Fatal(err)
	}
	if err := zw.Close(); err != nil {
		t.Fatal(err)
	}
	corrupt := bytes.Clone(archive.Bytes())
	position := bytes.Index(corrupt, index)
	if position < 0 {
		t.Fatal("stored update-site index not found in ZIP")
	}
	corrupt[position] ^= 1
	if _, err := ExtractArchive(corrupt, "p2.index"); !errors.Is(err, ErrInvalid) {
		t.Errorf("CRC-corrupted update site accepted: %v", err)
	}
	unknown := bytes.Clone(archive.Bytes())
	central := bytes.Index(unknown, []byte("PK\x01\x02"))
	if central < 0 {
		t.Fatal("ZIP central directory not found")
	}
	// Keep the payload intact while making the member's declared compressor unsupported.
	binary.LittleEndian.PutUint16(unknown[8:10], 99)
	binary.LittleEndian.PutUint16(unknown[central+10:central+12], 99)
	if _, err := ExtractArchive(unknown, "p2.index"); !errors.Is(err, ErrInvalid) {
		t.Errorf("unsupported ZIP compressor delivered: %v", err)
	}
	zr, err := zip.NewReader(bytes.NewReader(unknown), int64(len(unknown)))
	if err != nil || len(zr.File) != 1 {
		t.Fatalf("unknown-compressor fixture is not a parseable ZIP: %v", err)
	}
	if r, err := zr.File[0].Open(); err == nil {
		_ = r.Close()
		t.Fatal("unknown-compressor fixture remained readable")
	}
	if err := Validate(
		"content.jar",
		bytes.NewReader(unknown),
		int64(len(unknown)),
	); !errors.Is(
		err,
		ErrInvalid,
	) {
		t.Errorf("unsupported JAR compressor admitted: %v", err)
	}
	oversized := bytes.Clone(archive.Bytes())
	binary.LittleEndian.PutUint32(oversized[central+24:central+28], uint32(MaxArtifact+1))
	if err := Validate(
		"content.jar",
		bytes.NewReader(oversized),
		int64(len(oversized)),
	); !errors.Is(
		err,
		ErrInvalid,
	) {
		t.Errorf("JAR declaring an oversized member admitted: %v", err)
	}
}

func TestUnitP2JARRejectsInterruptedMemberRead(t *testing.T) {
	var archive bytes.Buffer
	zw := zip.NewWriter(&archive)
	f, err := zw.CreateHeader(&zip.FileHeader{Name: "content.xml", Method: zip.Store})
	if err != nil {
		t.Fatal(err)
	}
	payload := []byte(
		`<repository type="org.eclipse.equinox.p2.metadata.repository.SimpleMetadataRepository"/>`,
	)
	if _, err := f.Write(payload); err != nil {
		t.Fatal(err)
	}
	if err := zw.Close(); err != nil {
		t.Fatal(err)
	}
	start := bytes.Index(archive.Bytes(), payload)
	if start < 0 {
		t.Fatal("stored JAR member not found")
	}
	src := failingP2MemberReader{
		Reader:    bytes.NewReader(archive.Bytes()),
		failFrom:  int64(start),
		failUntil: int64(start + len(payload)),
	}
	if err := Validate("content.jar", src, int64(archive.Len())); !errors.Is(err, ErrInvalid) {
		t.Errorf("interrupted JAR member admitted: %v", err)
	}
	changed := &secondP2MemberReader{
		Reader: bytes.NewReader(
			archive.Bytes(),
		),
		failFrom:  int64(start),
		failUntil: int64(start + len(payload)),
	}
	if err := Validate(
		"content.jar",
		changed,
		int64(archive.Len()),
	); !errors.Is(err, ErrInvalid) ||
		changed.reads < 2 {
		t.Errorf(
			"metadata member changed between validation passes: %v, reads %d",
			err,
			changed.reads,
		)
	}
}

type failingP2MemberReader struct {
	*bytes.Reader
	failFrom, failUntil int64
}

func (r failingP2MemberReader) ReadAt(p []byte, off int64) (int, error) {
	if off >= r.failFrom && off < r.failUntil {
		return 0, io.ErrUnexpectedEOF
	}
	return r.Reader.ReadAt(p, off)
}

type secondP2MemberReader struct {
	*bytes.Reader
	failFrom, failUntil int64
	reads               int
}

func (r *secondP2MemberReader) ReadAt(p []byte, off int64) (int, error) {
	if off >= r.failFrom && off < r.failUntil {
		r.reads++
		if r.reads > 1 {
			return 0, io.ErrUnexpectedEOF
		}
	}
	return r.Reader.ReadAt(p, off)
}

func TestUnitP2XMLRejectsExcessiveTokenCount(t *testing.T) {
	const root = `<repository type="org.eclipse.equinox.p2.metadata.repository.SimpleMetadataRepository">`
	body := []byte(root + strings.Repeat("<unit/>", 250001) + "</repository>")
	if err := validateXML("content.xml", body); !errors.Is(err, ErrInvalid) {
		t.Errorf("XML token flood accepted: %v", err)
	}
}

type readErrorSeeker struct{ *bytes.Reader }

func (readErrorSeeker) Read([]byte) (int, error) { return 0, io.ErrUnexpectedEOF }

type failingSeeker struct{}

func (failingSeeker) Read([]byte) (int, error)       { return 0, io.ErrUnexpectedEOF }
func (failingSeeker) Seek(int64, int) (int64, error) { return 0, io.ErrUnexpectedEOF }
