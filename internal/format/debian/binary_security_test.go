package debian

import (
	"archive/tar"
	"bytes"
	"context"
	"errors"
	"io"
	"strings"
	"testing"
)

// The archive reader is an admission boundary. Keep these mutations separate
// from the dpkg-built fixtures so a rejected upload cannot be fixed by
// changing a fixture to match the reader.
func TestReadBinaryRejectsCorruptArStructure(t *testing.T) {
	valid := fixture(t, validControl)
	mutate := func(offset int, value byte) []byte {
		out := bytes.Clone(valid)
		out[offset] = value
		return out
	}
	for _, tc := range []struct {
		name string
		body []byte
		size int64
	}{
		{"declared size too large", valid, maxBinarySize + 1},
		{"truncated header", append(bytes.Clone(valid), 'x'), int64(len(valid) + 1)},
		{"ar header marker", mutate(8+58, 'x'), int64(len(valid))},
		{"ar member name", mutate(8+3, ' '), int64(len(valid))},
		{"ar member size", mutate(8+48, 'x'), int64(len(valid))},
		{"missing ar padding", func() []byte {
			body := makeAR(t, fixtureMember{"_odd", []byte("x")})
			return body[:len(body)-1]
		}(), 0},
		{"version member exceeds limit", makeAR(t,
			fixtureMember{"debian-binary", append([]byte("2.0\n"), bytes.Repeat([]byte("x"), 129)...)},
			fixtureMember{"control.tar.gz", compressed(t, "gz", makeTar(t, "./control", validControl))},
			fixtureMember{"data.tar.gz", compressed(t, "gz", makeTar(t, "./usr/bin/hello", "hello"))},
		), 0},
		{"archive version", makeAR(t,
			fixtureMember{"debian-binary", []byte("2.0")},
			fixtureMember{"control.tar.gz", compressed(t, "gz", makeTar(t, "./control", validControl))},
			fixtureMember{"data.tar.gz", compressed(t, "gz", makeTar(t, "./usr/bin/hello", "hello"))},
		), 0},
		{"unsupported control compression", makeAR(t,
			fixtureMember{"debian-binary", []byte("2.0\n")},
			fixtureMember{"control.tar.zip", []byte("not a tar")},
			fixtureMember{"data.tar.gz", compressed(t, "gz", makeTar(t, "./usr/bin/hello", "hello"))},
		), 0},
		{"malformed control gzip", makeAR(t,
			fixtureMember{"debian-binary", []byte("2.0\n")},
			fixtureMember{"control.tar.gz", []byte("not gzip")},
			fixtureMember{"data.tar.gz", compressed(t, "gz", makeTar(t, "./usr/bin/hello", "hello"))},
		), 0},
		{"malformed control xz", makeAR(t,
			fixtureMember{"debian-binary", []byte("2.0\n")},
			fixtureMember{"control.tar.xz", []byte("not xz")},
			fixtureMember{"data.tar.gz", compressed(t, "gz", makeTar(t, "./usr/bin/hello", "hello"))},
		), 0},
		{"malformed control zstd", makeAR(t,
			fixtureMember{"debian-binary", []byte("2.0\n")},
			fixtureMember{"control.tar.zst", []byte("not zstd")},
			fixtureMember{"data.tar.gz", compressed(t, "gz", makeTar(t, "./usr/bin/hello", "hello"))},
		), 0},
		{"missing control file", makeAR(t,
			fixtureMember{"debian-binary", []byte("2.0\n")},
			fixtureMember{"control.tar.gz", compressed(t, "gz", makeTar(t, "./other", "text"))},
			fixtureMember{"data.tar.gz", compressed(t, "gz", makeTar(t, "./usr/bin/hello", "hello"))},
		), 0},
		{"malformed data tar", makeAR(t,
			fixtureMember{"debian-binary", []byte("2.0\n")},
			fixtureMember{"control.tar.gz", compressed(t, "gz", makeTar(t, "./control", validControl))},
			fixtureMember{"data.tar.gz", compressed(t, "gz", []byte("not a tar"))},
		), 0},
		{"unsupported data compression", makeAR(t,
			fixtureMember{"debian-binary", []byte("2.0\n")},
			fixtureMember{"control.tar.gz", compressed(t, "gz", makeTar(t, "./control", validControl))},
			fixtureMember{"data.tar.zip", []byte("not a tar")},
		), 0},
	} {
		t.Run(tc.name, func(t *testing.T) {
			size := tc.size
			if size == 0 {
				size = int64(len(tc.body))
			}
			if _, err := ReadBinary(t.Context(), bytes.NewReader(tc.body), size); err == nil {
				t.Fatal("corrupt Debian archive accepted")
			}
		})
	}
	canceled, cancel := context.WithCancel(t.Context())
	cancel()
	if _, err := ReadBinary(
		canceled,
		bytes.NewReader(valid),
		int64(len(valid)),
	); !errors.Is(
		err,
		context.Canceled,
	) {
		t.Fatalf("canceled archive read: %v", err)
	}
	withExtension := makeAR(t,
		fixtureMember{"debian-binary", []byte("2.0\n")},
		fixtureMember{"_gpgorigin", []byte("extension")},
		fixtureMember{"control.tar.gz", compressed(t, "gz", makeTar(t, "./control", validControl))},
		fixtureMember{"data.tar.gz", compressed(t, "gz", makeTar(t, "./usr/bin/hello", "hello"))},
		fixtureMember{"future-member", []byte("ignored")},
	)
	if got, err := ReadBinary(
		t.Context(),
		bytes.NewReader(withExtension),
		int64(len(withExtension)),
	); err != nil ||
		got.Name != "hello" {
		t.Fatalf("dpkg-compatible extension rejected: %+v, %v", got, err)
	}
}

func TestReadBinaryRejectsMalformedControlTar(t *testing.T) {
	var tarBytes bytes.Buffer
	tw := tar.NewWriter(&tarBytes)
	for range 2 {
		if err := tw.WriteHeader(&tar.Header{
			Name: "./control", Mode: 0o644,
			Size: int64(len(validControl)),
		}); err != nil {
			t.Fatal(err)
		}
		if _, err := io.WriteString(tw, validControl); err != nil {
			t.Fatal(err)
		}
	}
	if err := tw.Close(); err != nil {
		t.Fatal(err)
	}
	for _, tc := range []struct {
		name    string
		control []byte
	}{
		{"duplicate control", tarBytes.Bytes()},
		{"broken tar", []byte("not a tar")},
	} {
		t.Run(tc.name, func(t *testing.T) {
			body := makeAR(
				t,
				fixtureMember{"debian-binary", []byte("2.0\n")},
				fixtureMember{"control.tar.gz", compressed(t, "gz", tc.control)},
				fixtureMember{
					"data.tar.gz",
					compressed(t, "gz", makeTar(t, "./usr/bin/hello", "hello")),
				},
			)
			if _, err := ReadBinary(
				t.Context(),
				bytes.NewReader(body),
				int64(len(body)),
			); err == nil {
				t.Fatal("corrupt control archive accepted")
			}
		})
	}
}

func TestBinaryControlRejectsAmbiguousFields(t *testing.T) {
	for _, tc := range []struct {
		name, control string
	}{
		{"orphan continuation", " continuation\n" + validControl},
		{"empty field", strings.Replace(validControl, "Description: test binary", "Description:  ", 1)},
		{"missing colon", strings.Replace(validControl, "Version: 1.0-1", "Version 1.0-1", 1)},
		{"invalid architecture", strings.Replace(validControl, "Architecture: amd64", "Architecture: ../amd64", 1)},
		{"unsafe version", strings.Replace(validControl, "Version: 1.0-1", "Version: 1.0/../2", 1)},
		{"missing maintainer", strings.Replace(validControl, "Maintainer: Test <test@example.com>\n", "", 1)},
		{"NUL", validControl + "X: a\x00b\n"},
	} {
		t.Run(tc.name, func(t *testing.T) {
			body := fixture(t, tc.control)
			if _, err := ReadBinary(
				t.Context(),
				bytes.NewReader(body),
				int64(len(body)),
			); err == nil {
				t.Fatal("ambiguous control accepted")
			}
		})
	}
}
