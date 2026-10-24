package alpine

import (
	"archive/tar"
	"bytes"
	"compress/gzip"
	"crypto"
	"crypto/rand"
	"crypto/rsa"
	"crypto/sha1" //nolint:gosec // APKINDEX v2 RSA signatures use SHA-1.
	"fmt"
	"regexp"
	"slices"
	"strconv"
	"strings"
)

const maxIndexBytes = 32 << 20

var keyFile = regexp.MustCompile(`^[A-Za-z0-9@_.-]+\.rsa\.pub$`)

// EncodeIndex writes the APKINDEX.tar.gz stream, optionally prepending a
// signature stream using the same RSA wire form as abuild-sign. The key name
// must match the basename placed in the client's /etc/apk/keys directory.
func EncodeIndex(
	entries []Package,
	description, keyName string,
	key *rsa.PrivateKey,
) ([]byte, error) {
	entries = slices.Clone(entries)
	slices.SortFunc(entries, func(a, b Package) int {
		if n := strings.Compare(a.Name, b.Name); n != 0 {
			return n
		}
		return strings.Compare(a.Version, b.Version)
	})
	var records strings.Builder
	for _, entry := range entries {
		if records.Len() > maxIndexBytes {
			return nil, fmt.Errorf("%w: exceeds %d bytes", errInvalidIndex, maxIndexBytes)
		}
		records.WriteString(entry.IndexRecord())
	}
	if records.Len() > maxIndexBytes {
		return nil, fmt.Errorf("%w: exceeds %d bytes", errInvalidIndex, maxIndexBytes)
	}
	var raw bytes.Buffer
	if err := writeIndexTar(&raw, description, []byte(records.String())); err != nil {
		return nil, err
	}
	if key == nil {
		return raw.Bytes(), nil
	}
	if !keyFile.MatchString(keyName) || strings.Contains(keyName, "..") {
		return nil, fmt.Errorf("%w: invalid signing key name", errInvalidIndex)
	}
	h := sha1.Sum(raw.Bytes()) //nolint:gosec // APKINDEX v2 abuild-sign wire format.
	signature, err := rsa.SignPKCS1v15(rand.Reader, key, crypto.SHA1, h[:])
	if err != nil {
		return nil, fmt.Errorf("alpine: sign index: %w", err)
	}
	var signed bytes.Buffer
	if err := writeSignatureTar(&signed, ".SIGN.RSA."+keyName, signature); err != nil {
		return nil, err
	}
	signed.Write(raw.Bytes())
	return signed.Bytes(), nil
}

// IndexRecord renders the apk-tools v2 one-letter metadata fields. Values
// come from bounded .PKGINFO parsing, not from client-supplied index text.
func (p Package) IndexRecord() string {
	if p.IndexRaw != "" {
		return p.IndexRaw
	}
	var out strings.Builder
	field := func(k, v string) {
		if v != "" {
			out.WriteString(k)
			out.WriteByte(':')
			out.WriteString(v)
			out.WriteByte('\n')
		}
	}
	field("C", "Q1"+p.ControlSHA1)
	field("P", p.Name)
	field("V", p.Version)
	field("A", p.Architecture)
	field("S", strconv.FormatInt(p.ArchiveSize, 10))
	field("I", strconv.FormatInt(p.InstalledSize, 10))
	field("T", p.Description)
	field("U", p.URL)
	field("L", p.License)
	field("o", p.Origin)
	field("m", p.Maintainer)
	if p.BuildTime > 0 {
		field("t", strconv.FormatInt(p.BuildTime, 10))
	}
	field("c", p.Commit)
	field("D", strings.Join(p.Depends, " "))
	field("p", strings.Join(p.Provides, " "))
	field("i", strings.Join(p.InstallIf, " "))
	out.WriteByte('\n')
	return out.String()
}

func writeIndexTar(out *bytes.Buffer, description string, index []byte) error {
	gz := gzip.NewWriter(out)
	tw := tar.NewWriter(gz)
	for _, file := range []struct {
		name string
		body []byte
	}{{"DESCRIPTION", []byte(description)}, {"APKINDEX", index}} {
		if err := tw.WriteHeader(&tar.Header{
			Name: file.name, Mode: 0o644, Size: int64(len(file.body)),
		}); err != nil {
			return fmt.Errorf("alpine: write index header: %w", err)
		}
		if _, err := tw.Write(file.body); err != nil {
			return fmt.Errorf("alpine: write index entry: %w", err)
		}
	}
	if err := tw.Close(); err != nil {
		return fmt.Errorf("alpine: finish index tar: %w", err)
	}
	if err := gz.Close(); err != nil {
		return fmt.Errorf("alpine: finish index gzip: %w", err)
	}
	return nil
}

func writeSignatureTar(out *bytes.Buffer, name string, signature []byte) error {
	gz := gzip.NewWriter(out)
	tw := tar.NewWriter(gz)
	if err := tw.WriteHeader(&tar.Header{
		Name: name, Mode: 0o644, Size: int64(len(signature)),
	}); err != nil {
		return fmt.Errorf("alpine: write signature header: %w", err)
	}
	if _, err := tw.Write(signature); err != nil {
		return fmt.Errorf("alpine: write signature: %w", err)
	}
	// A signature is a tar segment, so flush its entry padding without the
	// two end-of-tar records that would terminate the following index.
	if err := tw.Flush(); err != nil {
		return fmt.Errorf("alpine: finish signature tar: %w", err)
	}
	if err := gz.Close(); err != nil {
		return fmt.Errorf("alpine: finish signature gzip: %w", err)
	}
	return nil
}
