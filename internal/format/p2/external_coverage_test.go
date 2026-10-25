package p2

import (
	"archive/zip"
	"bytes"
	"encoding/binary"
	"errors"
	"fmt"
	"io"
	"os"
	"strings"
	"testing"

	"github.com/ulikunitz/xz"
)

func TestUnitP2ArtifactChecksumsAcrossPublishedEncodings(t *testing.T) {
	sha256 := strings.Repeat("a", 64)
	sha512 := strings.Repeat("b", 128)
	metadata := []byte(
		fmt.Sprintf(
			`<repository type="org.eclipse.equinox.p2.artifact.repository.simpleRepository"><artifacts size="3">`+
				`<artifact classifier="osgi.bundle" id="plugin" version="1.0.0"><properties size="2">`+
				`<property name="download.checksum.sha-256" value="%s"/>`+
				`<property name="download.checksum.sha-512" value="%s"/></properties></artifact>`+
				`<artifact classifier="org.eclipse.update.feature" id="feature" version="2.0.0"><properties size="1">`+
				`<property name="download.checksum.sha-256" value="%s"/></properties></artifact>`+
				`<artifact classifier="binary" id="archive" version="3.0.0"><properties size="1">`+
				`<property name="download.checksum.sha-512" value="%s"/></properties></artifact>`+
				`</artifacts></repository>`,
			sha256,
			sha512,
			sha256,
			sha512,
		),
	)
	for _, tc := range []struct {
		name string
		body []byte
	}{
		{"artifacts.xml", metadata},
		{"artifacts.xml.xz", p2TestXZ(t, metadata)},
		{"artifacts.jar", p2TestJAR(t, "artifacts.xml", metadata, nil)},
	} {
		t.Run(tc.name, func(t *testing.T) {
			for _, want := range []struct {
				path, sha256, sha512 string
			}{
				{"plugins/plugin_1.0.0.jar", sha256, sha512},
				{"features/feature_2.0.0.jar", sha256, ""},
				{"binary/archive_3.0.0", "", sha512},
			} {
				got, err := ArtifactChecksums(tc.name, tc.body, want.path)
				if err != nil || got.SHA256 != want.sha256 || got.SHA512 != want.sha512 {
					t.Fatalf("%s: %+v, %v", want.path, got, err)
				}
			}
		})
	}
	for name, body := range map[string][]byte{
		"wrong.xml":     metadata,
		"artifacts.xml": bytes.Replace(metadata, []byte(sha256), []byte("not-a-digest"), 1),
	} {
		if _, err := ArtifactChecksums(
			name,
			body,
			"plugins/plugin_1.0.0.jar",
		); !errors.Is(
			err,
			ErrInvalid,
		) {
			t.Errorf("invalid %s checksum accepted: %v", name, err)
		}
	}
	duplicate := bytes.Replace(metadata,
		[]byte(`</artifacts>`),
		[]byte(`<artifact classifier="osgi.bundle" id="plugin" version="1.0.0"/></artifacts>`), 1)
	if _, err := ArtifactChecksums("artifacts.xml", duplicate,
		"plugins/plugin_1.0.0.jar"); !errors.Is(err, ErrInvalid) {
		t.Errorf("duplicate artifact checksum accepted: %v", err)
	}
	invalidHex := bytes.Replace(metadata, []byte(sha256), []byte(strings.Repeat("z", 64)), 1)
	if _, err := ArtifactChecksums("artifacts.xml", invalidHex,
		"plugins/plugin_1.0.0.jar"); !errors.Is(err, ErrInvalid) {
		t.Errorf("non-hex published checksum accepted: %v", err)
	}
}

func TestUnitP2MetadataExtractionRejectsMissingArchiveEntry(t *testing.T) {
	for _, tc := range []struct {
		name string
		body []byte
	}{
		{"artifacts.jar", p2TestJAR(t, "unrelated.xml", []byte("safe"), nil)},
		{"artifacts.jar", []byte("not a zip")},
		{"artifacts.xml.xz", []byte("not xz")},
		{"artifacts.other", []byte("unsupported")},
	} {
		if _, err := metadataXML(tc.name, tc.body); !errors.Is(err, ErrInvalid) {
			t.Errorf("%s metadata extraction accepted: %v", tc.name, err)
		}
	}
}

func TestUnitP2CompositeRewritePreservesArchiveSidecarAndFailsClosed(t *testing.T) {
	metadata := []byte(
		`<repository type="org.eclipse.equinox.internal.p2.metadata.repository.CompositeMetadataRepository"><children size="2">` +
			`<child location="https://external.example/first"/><child location="https://external.example/second"/>` +
			`</children></repository>`,
	)
	const sidecar = "published sidecar bytes"
	archive := p2TestJAR(t, "compositeContent.xml", metadata, []byte(sidecar))
	changed, err := EditComposite(
		"compositeContent.jar",
		archive,
		func(raw string) (string, error) {
			return "__p2_ext/" + strings.TrimPrefix(raw, "https://external.example/"), nil
		},
	)
	if err != nil || bytes.Contains(changed, []byte("external.example")) {
		t.Fatalf("archive rewrite leaked external URL: %v", err)
	}
	z, err := zip.NewReader(bytes.NewReader(changed), int64(len(changed)))
	if err != nil {
		t.Fatal(err)
	}
	for _, f := range z.File {
		r, err := f.Open()
		if err != nil {
			t.Fatal(err)
		}
		body, err := io.ReadAll(r)
		_ = r.Close()
		if err != nil {
			t.Fatal(err)
		}
		if f.Name == "sidecar.txt" && string(body) != sidecar {
			t.Fatalf("sidecar changed: %q", body)
		}
		if f.Name == "compositeContent.xml" &&
			(!bytes.Contains(body, []byte("__p2_ext/first")) ||
				!bytes.Contains(body, []byte("__p2_ext/second"))) {
			t.Fatalf("child rewrite incomplete: %s", body)
		}
	}
	if output, err := EditComposite(
		"compositeContent.jar",
		archive,
		func(raw string) (string, error) {
			if strings.HasSuffix(raw, "/second") {
				return "", errors.New("child denied")
			}
			return "__p2_ext/first", nil
		},
	); err == nil ||
		len(output) != 0 {
		t.Fatalf("denied child exposed partial metadata: %q, %v", output, err)
	}
	for _, malformed := range []string{
		`<child/>`,
		`<child location="https://external.example/first" location="https://external.example/second"/>`,
	} {
		body := []byte(
			`<repository type="org.eclipse.equinox.internal.p2.metadata.repository.CompositeMetadataRepository"><children size="1">` +
				malformed + `</children></repository>`,
		)
		if output, err := EditComposite("compositeContent.xml", body,
			func(string) (string, error) { return "safe", nil }); err == nil || len(output) != 0 {
			t.Fatalf("malformed child exposed metadata: %q, %v", output, err)
		}
	}
	if _, err := EditComposite("content.xml", metadata,
		func(string) (string, error) { return "safe", nil }); !errors.Is(err, ErrInvalid) {
		t.Fatalf("non-composite metadata rewritten: %v", err)
	}
}

func TestUnitP2IndexAndArchiveIntegrityEdges(t *testing.T) {
	validIndex := []byte(
		"# Eclipse p2 index\nversion=1\nmetadata.repository.factory.order=content.jar,\\!\n" +
			"artifact.repository.factory.order=artifacts.xml,!\n",
	)
	if err := Validate(
		"p2.index",
		bytes.NewReader(validIndex),
		int64(len(validIndex)),
	); err != nil {
		t.Fatalf("valid escaped fallback index rejected: %v", err)
	}
	for _, body := range [][]byte{
		[]byte("version=2\nmetadata.repository.factory.order=content.jar\n" +
			"artifact.repository.factory.order=artifacts.jar\n"),
		[]byte("version=1\nmetadata.repository.factory.order=content.jar\n" +
			"artifact.repository.factory.order=artifacts.jar\nunknown.property=true\n"),
		[]byte("version=1\nmetadata.repository.factory.order=outside.example\n" +
			"artifact.repository.factory.order=artifacts.jar\n"),
		[]byte("version=1\nmetadata.repository.factory.order\n" +
			"artifact.repository.factory.order=artifacts.jar\n"),
	} {
		if err := Validate(
			"p2.index",
			bytes.NewReader(body),
			int64(len(body)),
		); !errors.Is(
			err,
			ErrInvalid,
		) {
			t.Errorf("invalid index accepted: %q, %v", body, err)
		}
	}
	missingMetadata := p2TestJAR(t, "other.xml", []byte("unrelated"), nil)
	if err := Validate(
		"content.jar",
		bytes.NewReader(missingMetadata),
		int64(len(missingMetadata)),
	); !errors.Is(
		err,
		ErrInvalid,
	) {
		t.Fatalf("JAR without declared metadata accepted: %v", err)
	}
	invalidEntry := p2TestJAR(t, "content.xml", []byte("<not-a-repository/>"), nil)
	if _, err := ExtractArchive(invalidEntry, "content.xml"); !errors.Is(err, ErrInvalid) {
		t.Fatalf("invalid archive metadata delivered: %v", err)
	}
	var directory bytes.Buffer
	w := zip.NewWriter(&directory)
	h := &zip.FileHeader{Name: "content.jar", Method: zip.Store}
	h.SetMode(os.ModeDir | 0o755)
	if _, err := w.CreateHeader(h); err != nil {
		t.Fatal(err)
	}
	if err := w.Close(); err != nil {
		t.Fatal(err)
	}
	if _, err := ExtractArchive(directory.Bytes(), "content.jar"); !errors.Is(err, ErrInvalid) {
		t.Fatalf("directory delivered as metadata: %v", err)
	}
	oversized := p2TestJAR(
		t,
		"content.xml",
		[]byte(
			`<repository type="org.eclipse.equinox.internal.p2.metadata.repository.LocalMetadataRepository"/>`,
		),
		nil,
	)
	central := bytes.Index(oversized, []byte("PK\x01\x02"))
	if central < 0 {
		t.Fatal("test JAR has no central directory")
	}
	binary.LittleEndian.PutUint32(oversized[central+24:], MaxMetadata+1)
	if err := Validate(
		"content.jar",
		bytes.NewReader(oversized),
		int64(len(oversized)),
	); !errors.Is(
		err,
		ErrInvalid,
	) {
		t.Fatalf("declared metadata expansion escaped limit: %v", err)
	}
}

type p2ReadErrorSeeker struct{}

func (p2ReadErrorSeeker) Read([]byte) (int, error)       { return 0, io.ErrUnexpectedEOF }
func (p2ReadErrorSeeker) Seek(int64, int) (int64, error) { return 0, nil }

func TestUnitP2MetadataReadErrorsFailClosed(t *testing.T) {
	for _, name := range []string{"content.xml", "p2.index"} {
		if err := Validate(name, p2ReadErrorSeeker{}, 1); !errors.Is(err, ErrInvalid) {
			t.Fatalf("%s read error accepted: %v", name, err)
		}
	}
	if _, err := (seekReaderAt{p2ReadErrorSeeker{}}).ReadAt(
		make([]byte, 1),
		0,
	); !errors.Is(
		err,
		io.ErrUnexpectedEOF,
	) {
		t.Fatalf("archive random-read error lost: %v", err)
	}
}

func TestUnitP2ArchiveDeclaredAndAggregateExpansionLimits(t *testing.T) {
	var compact bytes.Buffer
	w := zip.NewWriter(&compact)
	f, err := w.Create("p2.index")
	if err != nil {
		t.Fatal(err)
	}
	if _, err := f.Write([]byte("version=1\n")); err != nil {
		t.Fatal(err)
	}
	if err := w.Close(); err != nil {
		t.Fatal(err)
	}
	declared := bytes.Clone(compact.Bytes())
	central := bytes.Index(declared, []byte("PK\x01\x02"))
	if central < 0 {
		t.Fatal("test archive has no central directory")
	}
	binary.LittleEndian.PutUint32(declared[central+24:], MaxArtifact+1)
	if _, err := ExtractArchive(declared, "p2.index"); !errors.Is(err, ErrInvalid) {
		t.Fatalf("declared archive expansion escaped limit: %v", err)
	}
	var aggregate bytes.Buffer
	w = zip.NewWriter(&aggregate)
	for _, name := range []string{"part-one", "part-two"} {
		f, err := w.Create(name)
		if err != nil {
			t.Fatal(err)
		}
		if _, err := io.Copy(f, io.LimitReader(zeroReader{}, 19<<20)); err != nil {
			t.Fatal(err)
		}
	}
	if err := w.Close(); err != nil {
		t.Fatal(err)
	}
	if err := Validate(
		"content.jar",
		bytes.NewReader(aggregate.Bytes()),
		int64(aggregate.Len()),
	); !errors.Is(
		err,
		ErrInvalid,
	) {
		t.Fatalf("aggregate metadata expansion escaped limit: %v", err)
	}
}

type zeroReader struct{}

func (zeroReader) Read(p []byte) (int, error) {
	clear(p)
	return len(p), nil
}

func TestUnitP2MetadataArchiveRejectsCorruptMemberBytes(t *testing.T) {
	var archive bytes.Buffer
	w := zip.NewWriter(&archive)
	f, err := w.CreateHeader(&zip.FileHeader{Name: "artifacts.xml", Method: zip.Store})
	if err != nil {
		t.Fatal(err)
	}
	if _, err := f.Write([]byte("published metadata bytes")); err != nil {
		t.Fatal(err)
	}
	if err := w.Close(); err != nil {
		t.Fatal(err)
	}
	corrupt := bytes.Clone(archive.Bytes())
	z, err := zip.NewReader(bytes.NewReader(corrupt), int64(len(corrupt)))
	if err != nil {
		t.Fatal(err)
	}
	offset, err := z.File[0].DataOffset()
	if err != nil {
		t.Fatal(err)
	}
	corrupt[offset] ^= 0xff
	if _, err := metadataXML("artifacts.jar", corrupt); !errors.Is(err, ErrInvalid) {
		t.Fatalf("corrupt metadata JAR member admitted: %v", err)
	}
}

func TestUnitP2ArchiveRejectsCorruptEntryAndUnknownCompression(t *testing.T) {
	writeStored := func(name string, body []byte) []byte {
		t.Helper()
		var archive bytes.Buffer
		w := zip.NewWriter(&archive)
		f, err := w.CreateHeader(&zip.FileHeader{Name: name, Method: zip.Store})
		if err != nil {
			t.Fatal(err)
		}
		if _, err := f.Write(body); err != nil {
			t.Fatal(err)
		}
		if err := w.Close(); err != nil {
			t.Fatal(err)
		}
		return archive.Bytes()
	}
	index := []byte("version=1\nmetadata.repository.factory.order=content.jar,!\n" +
		"artifact.repository.factory.order=artifacts.jar,!\n")
	corrupt := writeStored("p2.index", index)
	z, err := zip.NewReader(bytes.NewReader(corrupt), int64(len(corrupt)))
	if err != nil {
		t.Fatal(err)
	}
	offset, err := z.File[0].DataOffset()
	if err != nil {
		t.Fatal(err)
	}
	corrupt[offset] ^= 0xff
	if _, err := ExtractArchive(corrupt, "p2.index"); !errors.Is(err, ErrInvalid) {
		t.Fatalf("corrupt archive entry delivered: %v", err)
	}
	unsupported := writeStored(
		"artifacts.xml",
		[]byte(`<repository type="org.eclipse.equinox.p2.artifact.repository.simpleRepository"/>`),
	)
	central := bytes.Index(unsupported, []byte("PK\x01\x02"))
	if central < 0 {
		t.Fatal("test archive has no central directory")
	}
	binary.LittleEndian.PutUint16(unsupported[8:], 99)
	binary.LittleEndian.PutUint16(unsupported[central+10:], 99)
	if _, err := metadataXML("artifacts.jar", unsupported); !errors.Is(err, ErrInvalid) {
		t.Fatalf("unsupported metadata compression admitted: %v", err)
	}
	if err := Validate(
		"artifacts.jar",
		bytes.NewReader(unsupported),
		int64(len(unsupported)),
	); !errors.Is(
		err,
		ErrInvalid,
	) {
		t.Fatalf("unsupported JAR compression admitted: %v", err)
	}
}

func p2TestJAR(t *testing.T, name string, body, sidecar []byte) []byte {
	t.Helper()
	var out bytes.Buffer
	w := zip.NewWriter(&out)
	for entry, content := range map[string][]byte{name: body, "sidecar.txt": sidecar} {
		f, err := w.Create(entry)
		if err != nil {
			t.Fatal(err)
		}
		if _, err := f.Write(content); err != nil {
			t.Fatal(err)
		}
	}
	if err := w.Close(); err != nil {
		t.Fatal(err)
	}
	return out.Bytes()
}

func p2TestXZ(t *testing.T, body []byte) []byte {
	t.Helper()
	var out bytes.Buffer
	w, err := xz.NewWriter(&out)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := w.Write(body); err != nil {
		t.Fatal(err)
	}
	if err := w.Close(); err != nil {
		t.Fatal(err)
	}
	return out.Bytes()
}
