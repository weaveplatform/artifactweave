package p2

import (
	"archive/zip"
	"bytes"
	"errors"
	"io"
	"os"
	"path/filepath"
	"testing"
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
		if got := Classify(name); got != Invalid {
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
		{"compositeContent.xml", `<repository type="org.eclipse.equinox.internal.p2.metadata.repository.CompositeMetadataRepository"><children size="1"><child location="https://outside.example"/></children></repository>`},
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

type failingSeeker struct{}

func (failingSeeker) Read([]byte) (int, error)       { return 0, io.ErrUnexpectedEOF }
func (failingSeeker) Seek(int64, int) (int64, error) { return 0, io.ErrUnexpectedEOF }
