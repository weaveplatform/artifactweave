package debian

import (
	"bytes"
	"encoding/binary"
	"errors"
	"strings"
	"testing"
)

// xzCut is data, one xz stream without stream padding, less its index and
// footer: the footer's backward size plus the footer. ulikunitz/xz's reader
// decoded what is left whole.
func xzCut(t *testing.T, data []byte) []byte {
	t.Helper()
	footer := data[len(data)-12:]
	if string(footer[10:]) != "YZ" {
		t.Fatal("no xz footer at the end")
	}
	return data[:len(data)-int(binary.LittleEndian.Uint32(footer[4:8])+1)*4-12]
}

// A package whose control.tar.xz or data.tar.xz lost its index and footer
// is refused as any unreadable package is, as dpkg refuses it, and the
// whole package is still read (#1331).
func TestUnitReadBinaryNeedsWholeXZMembers(t *testing.T) {
	control := compressed(t, "xz", makeTar(t, "./control", validControl))
	data := compressed(t, "xz", makeTar(t, "./usr/bin/hello", "hello"))
	deb := func(control, data []byte) []byte {
		return makeAR(t,
			fixtureMember{"debian-binary", []byte("2.0\n")},
			fixtureMember{"control.tar.xz", control},
			fixtureMember{"data.tar.xz", data},
		)
	}
	whole := deb(control, data)
	if _, err := ReadBinary(t.Context(), bytes.NewReader(whole), int64(len(whole))); err != nil {
		t.Fatalf("whole package: %v", err)
	}
	for name, body := range map[string][]byte{
		"control.tar.xz": deb(xzCut(t, control), data),
		"data.tar.xz":    deb(control, xzCut(t, data)),
	} {
		if _, err := ReadBinary(
			t.Context(), bytes.NewReader(body), int64(len(body)),
		); !errors.Is(err, errInvalidBinary) {
			t.Errorf("%s without its index: %v, want the invalid package error", name, err)
		}
	}
}

// A Packages, installer Packages or Sources index whose xz stream lost its
// index and footer is no index, as apt refuses it (#1331).
func TestUnitIndexesNeedTheirWholeXZStream(t *testing.T) {
	packages := "Package: alpha\nVersion: 1.0\nArchitecture: amd64\n" +
		"Filename: pool/alpha.deb\nSize: 4\nSHA256: " + strings.Repeat("a", 64) + "\n\n"
	sources, err := Sources([]IndexedSource{dpkgSourceIndexEntry(t)})
	if err != nil {
		t.Fatal(err)
	}
	for name, c := range map[string]struct {
		body   []byte
		decode func([]byte) error
		err    error
	}{
		"Packages": {[]byte(packages), func(b []byte) error {
			_, err := DecodePackagesIndex(b)
			return err
		}, errInvalidBinary},
		"installer Packages": {[]byte(strings.Replace(packages, ".deb", ".udeb", 1)), func(b []byte) error {
			_, err := DecodeInstallerPackagesIndex(b)
			return err
		}, errInvalidBinary},
		"Sources": {sources, func(b []byte) error {
			_, err := DecodeSourcesIndex(b)
			return err
		}, errInvalidSource},
	} {
		whole := compressed(t, "xz", c.body)
		if err := c.decode(whole); err != nil {
			t.Fatalf("whole %s.xz: %v", name, err)
		}
		if err := c.decode(xzCut(t, whole)); !errors.Is(err, c.err) {
			t.Errorf("%s.xz without its index: %v, want %v", name, err, c.err)
		}
	}
}
