package support

import (
	"archive/tar"
	"bytes"
	"compress/gzip"
	"testing"
)

// TarGz returns a gzip-compressed tar archive that holds one file of mode
// 0600 per entry, named by the entry's key and holding its value, and fails
// tb when the archive cannot be written. The order of the files in the
// archive follows map iteration, so a test must not depend on it. Conan's
// recipe and package archives and CRAN's source packages are archives of
// this shape.
func TarGz(tb testing.TB, entries map[string]string) string {
	tb.Helper()
	var body bytes.Buffer
	gz := gzip.NewWriter(&body)
	tw := tar.NewWriter(gz)
	for name, content := range entries {
		if err := tw.WriteHeader(
			&tar.Header{Name: name, Mode: 0o600, Size: int64(len(content))},
		); err != nil {
			tb.Fatal(err)
		}
		if _, err := tw.Write([]byte(content)); err != nil {
			tb.Fatal(err)
		}
	}
	if err := tw.Close(); err != nil {
		tb.Fatal(err)
	}
	if err := gz.Close(); err != nil {
		tb.Fatal(err)
	}
	return body.String()
}
