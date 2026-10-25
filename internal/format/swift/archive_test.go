package swift

import (
	"archive/zip"
	"bytes"
	"context"
	"io"
	"testing"
)

const manifest = `// swift-tools-version:5.9
import PackageDescription
let package = Package(name: "Widget", targets: [.target(name: "Widget")])
`

func archive(t *testing.T, files map[string]string) []byte {
	t.Helper()
	var b bytes.Buffer
	z := zip.NewWriter(&b)
	for name, body := range files {
		w, err := z.Create(name)
		if err != nil {
			t.Fatal(err)
		}
		if _, err := w.Write([]byte(body)); err != nil {
			t.Fatal(err)
		}
	}
	if err := z.Close(); err != nil {
		t.Fatal(err)
	}
	return b.Bytes()
}

type failedArchiveReader struct{}

func (failedArchiveReader) Read([]byte) (int, error) { return 0, io.ErrUnexpectedEOF }

func TestUnitInspectArchiveReadFailure(t *testing.T) {
	if _, err := InspectArchive(t.Context(), failedArchiveReader{}, 0); err == nil {
		t.Fatal("failed archive transport accepted")
	}
}

func TestUnitInspectArchive(t *testing.T) {
	good := archive(t, map[string]string{
		"Package.swift":               manifest,
		"Sources/Widget/Widget.swift": "public struct Widget {}",
		"Package@swift-5.10.swift":    "// swift-tools-version:5.10\nimport PackageDescription\nlet package = Package(name: \"Widget\")",
	})
	a, err := InspectArchive(context.Background(), bytes.NewReader(good), int64(len(good)))
	if err != nil || string(a.Manifests["Package.swift"]) != manifest ||
		a.Tools["Package@swift-5.10.swift"] != "5.10" {
		t.Fatalf("valid archive: %#v, %v", a, err)
	}
	clientArchive := archive(t, map[string]string{
		"ExamplePackage/Package.swift":               manifest,
		"ExamplePackage/Sources/Widget/Widget.swift": "public struct Widget {}",
	})
	a, err = InspectArchive(
		context.Background(),
		bytes.NewReader(clientArchive),
		int64(len(clientArchive)),
	)
	if err != nil || string(a.Manifests["Package.swift"]) != manifest {
		t.Fatalf("SwiftPM archive-source layout: %#v, %v", a, err)
	}
	for name, data := range map[string][]byte{
		"missing manifest": archive(t, map[string]string{"Sources/A.swift": "x"}),
		"traversal":        archive(t, map[string]string{"Package.swift": manifest, "../secret": "x"}),
		"nested traversal": archive(t, map[string]string{"Package.swift": manifest, "Sources/../secret": "x"}),
		"bad manifest":     archive(t, map[string]string{"Package.swift": "let package = 1"}),
		"truncated":        good[:len(good)-10],
		"multiple roots": archive(t, map[string]string{
			"One/Package.swift": manifest, "Two/Package.swift": manifest,
		}),
		"outside root": archive(t, map[string]string{
			"ExamplePackage/Package.swift": manifest, "unrelated.txt": "x",
		}),
	} {
		t.Run(name, func(t *testing.T) {
			if _, err := InspectArchive(
				context.Background(),
				bytes.NewReader(data),
				int64(len(data)),
			); err == nil {
				t.Fatal("invalid archive accepted")
			}
		})
	}
	if _, err := InspectArchive(
		context.Background(),
		bytes.NewReader(good),
		int64(len(good)-1),
	); err == nil {
		t.Fatal("wrong declared size accepted")
	}
}
