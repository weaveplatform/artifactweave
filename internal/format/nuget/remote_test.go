package nuget_test

import (
	"archive/zip"
	"bytes"
	"context"
	"errors"
	"io"
	"testing"

	"github.com/weaveplatform/artifactweave/internal/format/nuget"
)

func TestRemoteArchiveAdmission(t *testing.T) {
	good := pkg("Acme.Package", "1.0.0", "")
	for _, tc := range []struct {
		name    string
		body    []byte
		id, key string
		valid   bool
	}{
		{"valid", good, "acme.package", "1.0.0", true},
		{"wrong id", good, "other.package", "1.0.0", false},
		{"wrong version", good, "acme.package", "2.0.0", false},
		{"symbols", symbols("Acme.Package", "1.0.0"), "acme.package", "1.0.0", false},
		{"not zip", []byte("<html>login</html>"), "acme.package", "1.0.0", false},
	} {
		t.Run(tc.name, func(t *testing.T) {
			err := nuget.ValidateRemote(
				t.Context(),
				bytes.NewReader(tc.body),
				int64(len(tc.body)),
				tc.id,
				tc.key,
			)
			if (err == nil) != tc.valid {
				t.Fatalf("valid=%v err=%v", tc.valid, err)
			}
		})
	}
	ctx, cancel := context.WithCancel(t.Context())
	cancel()
	if err := nuget.ValidateRemote(
		ctx,
		bytes.NewReader(good),
		int64(len(good)),
		"acme.package",
		"1.0.0",
	); !errors.Is(
		err,
		context.Canceled,
	) {
		t.Fatalf("cancellation: %v", err)
	}
}

func TestRemoteArchivePayloadLimitsAndCompression(t *testing.T) {
	for _, tc := range []struct {
		name   string
		method uint16
		size   uint64
	}{
		{"unknown compression", 255, 4},
		{"inflated size", zip.Store, 17 << 30},
	} {
		t.Run(tc.name, func(t *testing.T) {
			var b bytes.Buffer
			zw := zip.NewWriter(&b)
			w, _ := zw.Create("Acme.Package.nuspec")
			_, _ = io.WriteString(w, nuspecXML("Acme.Package", "1.0.0", ""))
			w, _ = zw.CreateRaw(
				&zip.FileHeader{
					Name:               "payload",
					Method:             tc.method,
					UncompressedSize64: tc.size,
					CompressedSize64:   4,
				},
			)
			_, _ = io.WriteString(w, "data")
			_ = zw.Close()
			if err := nuget.ValidateRemote(
				t.Context(),
				bytes.NewReader(b.Bytes()),
				int64(b.Len()),
				"acme.package",
				"1.0.0",
			); err == nil {
				t.Fatal("unsafe archive admitted")
			}
		})
	}
}
