package debian

import (
	"archive/tar"
	"bytes"
	"compress/gzip"
	"fmt"
	"testing"

	"github.com/klauspost/compress/zstd"
	"github.com/ulikunitz/xz"
)

const validControl = "Package: hello\nVersion: 1.0-1\nArchitecture: amd64\nMaintainer: Test <test@example.com>\nDescription: test binary\n"

type fixtureMember struct {
	name string
	body []byte
}

func makeAR(t *testing.T, members ...fixtureMember) []byte {
	t.Helper()
	var out bytes.Buffer
	out.WriteString("!<arch>\n")
	for _, m := range members {
		if len(m.name) > 16 {
			t.Fatalf("ar member name too long: %s", m.name)
		}
		_, err := fmt.Fprintf(&out, "%-16s%-12d%-6d%-6d%-8o%-10d`\n",
			m.name, 0, 0, 0, 0o644, len(m.body))
		if err != nil {
			t.Fatal(err)
		}
		out.Write(m.body)
		if len(m.body)%2 != 0 {
			out.WriteByte('\n')
		}
	}
	return out.Bytes()
}

func makeTar(t *testing.T, name, content string) []byte {
	t.Helper()
	var out bytes.Buffer
	tw := tar.NewWriter(&out)
	if err := tw.WriteHeader(
		&tar.Header{Name: name, Mode: 0o644, Size: int64(len(content))},
	); err != nil {
		t.Fatal(err)
	}
	if _, err := tw.Write([]byte(content)); err != nil {
		t.Fatal(err)
	}
	if err := tw.Close(); err != nil {
		t.Fatal(err)
	}
	return out.Bytes()
}

func compressed(t *testing.T, kind string, body []byte) []byte {
	t.Helper()
	var out bytes.Buffer
	switch kind {
	case "gz":
		w := gzip.NewWriter(&out)
		if _, err := w.Write(body); err != nil {
			t.Fatal(err)
		}
		if err := w.Close(); err != nil {
			t.Fatal(err)
		}
	case "xz":
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
	case "zst":
		w, err := zstd.NewWriter(&out)
		if err != nil {
			t.Fatal(err)
		}
		if _, err := w.Write(body); err != nil {
			t.Fatal(err)
		}
		if err := w.Close(); err != nil {
			t.Fatal(err)
		}
	default:
		t.Fatalf("unknown compression: %s", kind)
	}
	return out.Bytes()
}

func fixture(t *testing.T, control string) []byte {
	t.Helper()
	return makeAR(t,
		fixtureMember{"debian-binary", []byte("2.0\n")},
		fixtureMember{"control.tar.gz", compressed(t, "gz", makeTar(t, "./control", control))},
		fixtureMember{"data.tar.gz", compressed(t, "gz", makeTar(t, "./usr/bin/hello", "hello"))},
	)
}

func TestReadBinaryValidatesArchiveAndControl(t *testing.T) {
	for _, kind := range []string{"gz", "xz", "zst"} {
		t.Run(kind, func(t *testing.T) {
			body := makeAR(
				t,
				fixtureMember{"debian-binary", []byte("2.0\n")},
				fixtureMember{
					"control.tar." + kind,
					compressed(t, kind, makeTar(t, "./control", validControl)),
				},
				fixtureMember{
					"data.tar." + kind,
					compressed(t, kind, makeTar(t, "./usr/bin/hello", "hello")),
				},
			)
			got, err := ReadBinary(t.Context(), bytes.NewReader(body), int64(len(body)))
			if err != nil {
				t.Fatal(err)
			}
			if got.Name != "hello" || got.Version != "1.0-1" || got.Architecture != "amd64" ||
				string(got.Control) != validControl {
				t.Fatalf("wrong binary metadata: %+v", got)
			}
		})
	}
}

func TestReadBinaryRejectsMalformedInput(t *testing.T) {
	for _, tc := range []struct {
		name string
		body []byte
	}{
		{"no ar magic", []byte("not a deb")},
		{"missing data", makeAR(t, fixtureMember{"debian-binary", []byte("2.0\n")},
			fixtureMember{"control.tar.gz", compressed(t, "gz", makeTar(t, "./control", validControl))})},
		{"wrong order", makeAR(t, fixtureMember{"debian-binary", []byte("2.0\n")},
			fixtureMember{"data.tar.gz", compressed(t, "gz", makeTar(t, "./usr", ""))},
			fixtureMember{"control.tar.gz", compressed(t, "gz", makeTar(t, "./control", validControl))})},
		{"wrong version", makeAR(t, fixtureMember{"debian-binary", []byte("3.0\n")},
			fixtureMember{"control.tar.gz", compressed(t, "gz", makeTar(t, "./control", validControl))},
			fixtureMember{"data.tar.gz", compressed(t, "gz", makeTar(t, "./usr", ""))})},
		{"bad data gzip", makeAR(t, fixtureMember{"debian-binary", []byte("2.0\n")},
			fixtureMember{"control.tar.gz", compressed(t, "gz", makeTar(t, "./control", validControl))},
			fixtureMember{"data.tar.gz", []byte("bad gzip")})},
		{"missing package name", fixture(t, "Version: 1.0-1\nArchitecture: amd64\nMaintainer: Test <test@example.com>\nDescription: test\n")},
		{"duplicate package name", fixture(t, validControl+"Package: other\n")},
		{"second stanza", fixture(t, validControl+"\nPackage: other\n")},
	} {
		t.Run(tc.name, func(t *testing.T) {
			if _, err := ReadBinary(
				t.Context(),
				bytes.NewReader(tc.body),
				int64(len(tc.body)),
			); err == nil {
				t.Fatal("invalid .deb admitted")
			}
		})
	}
}
