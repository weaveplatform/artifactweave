package p2

import (
	"bytes"
	"encoding/binary"
	"errors"
	"hash/crc32"
	"runtime"
	"strings"
	"testing"
)

// xzCut is data, one xz stream without stream padding, less its index and
// footer, which ulikunitz/xz's reader decoded whole.
func xzCut(t *testing.T, data []byte) []byte {
	t.Helper()
	footer := data[len(data)-12:]
	if string(footer[10:]) != "YZ" {
		t.Fatal("no xz footer at the end")
	}
	return data[:len(data)-int(binary.LittleEndian.Uint32(footer[4:8])+1)*4-12]
}

// artifactsXML is an artifacts.xml whose one artifact declares a SHA-256.
var artifactsXML = []byte(
	`<repository type="org.eclipse.equinox.p2.artifact.repository.simpleRepository"><artifacts size="1">` +
		`<artifact classifier="osgi.bundle" id="plugin" version="1.0.0"><properties size="1">` +
		`<property name="download.checksum.sha-256" value="` + strings.Repeat(
		"a",
		64,
	) + `"/>` +
		`</properties></artifact></artifacts></repository>`,
)

// An .xml.xz metadata file whose xz stream lost its index and footer is
// refused wherever p2 reads one, as Eclipse's XZInputStream refuses it, and
// the whole file is still read (#1331): Validate, which admits a remote's
// file, and ArtifactChecksums and EditComposite, which read one again.
func TestUnitXZMetadataNeedsItsWholeStream(t *testing.T) {
	artifacts := artifactsXML
	composite := []byte(
		`<repository type="org.eclipse.equinox.internal.p2.metadata.repository.CompositeMetadataRepository">` +
			`<children size="1"><child location="child"/></children></repository>`,
	)
	keep := func(raw string) (string, error) { return raw, nil }
	for _, c := range []struct {
		name string
		body []byte
		read func(name string, body []byte) error
	}{
		{"artifacts.xml.xz", p2TestXZ(t, artifacts), func(name string, body []byte) error {
			return Validate(name, bytes.NewReader(body), int64(len(body)))
		}},
		{"artifacts.xml.xz", p2TestXZ(t, artifacts), func(name string, body []byte) error {
			_, err := ArtifactChecksums(name, body, "plugins/plugin_1.0.0.jar")
			return err
		}},
		{"compositeContent.xml.xz", p2TestXZ(t, composite), func(name string, body []byte) error {
			_, err := EditComposite(name, body, keep)
			return err
		}},
	} {
		if err := c.read(c.name, c.body); err != nil {
			t.Fatalf("whole %s: %v", c.name, err)
		}
		if err := c.read(c.name, xzCut(t, c.body)); !errors.Is(err, ErrInvalid) {
			t.Errorf("%s without its index: %v, want ErrInvalid", c.name, err)
		}
	}
	// metadataXML reads a file Validate admitted; it refuses a cut one too.
	if _, err := metadataXML(
		"artifacts.xml.xz",
		xzCut(t, p2TestXZ(t, artifacts)),
	); !errors.Is(
		err,
		ErrInvalid,
	) {
		t.Errorf("metadataXML without its index: %v, want ErrInvalid", err)
	}
}

// storedXZ is data in one xz stream without a check, held in uncompressed
// LZMA2 chunks of one block whose header names dictionary property prop.
func storedXZ(data []byte, prop byte) []byte {
	sealed := func(b []byte) []byte {
		return binary.LittleEndian.AppendUint32(b, crc32.ChecksumIEEE(b))
	}
	out := append([]byte{0xfd, '7', 'z', 'X', 'Z', 0}, sealed([]byte{0, 0})...)
	header := sealed([]byte{2, 0, 0x21, 1, prop, 0, 0, 0})
	var chunks []byte
	for i, control := 0, byte(0x01); i < len(data); i, control = i+64<<10, 0x02 {
		chunk := data[i:min(i+64<<10, len(data))]
		chunks = append(chunks, control, byte((len(chunk)-1)>>8), byte(len(chunk)-1))
		chunks = append(chunks, chunk...)
	}
	chunks = append(chunks, 0)
	out = append(append(out, header...), chunks...)
	out = append(out, make([]byte, -len(chunks)&3)...)
	index := binary.AppendUvarint([]byte{0, 1}, uint64(len(header)+len(chunks)))
	index = binary.AppendUvarint(index, uint64(len(data)))
	for len(index)%4 != 0 {
		index = append(index, 0)
	}
	index = sealed(index)
	footer := binary.LittleEndian.AppendUint32(nil, uint32(len(index)/4-1))
	footer = append(footer, 0, 0, 'Y', 'Z')
	footer = append(
		binary.LittleEndian.AppendUint32(nil, crc32.ChecksumIEEE(footer[:6])),
		footer...)
	return append(append(out, index...), footer...)
}

// An .xml.xz whose block header names a 1.5 GiB dictionary, the largest XZ
// for Java reads, is admitted with a dictionary no larger than MaxMetadata,
// all p2 reads of it: ulikunitz/xz allocated all 1.5 GiB (#1332). One that
// names 4 GiB is refused, as XZ for Java refuses it.
func TestUnitXZMetadataDictionaryIsBoundedByMaxMetadata(t *testing.T) {
	for _, c := range []struct {
		prop byte
		ok   bool
	}{{37, true}, {40, false}} {
		body := storedXZ(artifactsXML, c.prop)
		var before, after runtime.MemStats
		runtime.ReadMemStats(&before)
		err := Validate("artifacts.xml.xz", bytes.NewReader(body), int64(len(body)))
		runtime.ReadMemStats(&after)
		if c.ok && err != nil || !c.ok && !errors.Is(err, ErrInvalid) {
			t.Fatalf("property %d: %v", c.prop, err)
		}
		if n := after.TotalAlloc - before.TotalAlloc; n > MaxMetadata+8<<20 {
			t.Errorf(
				"property %d allocated %d MiB, want at most %d MiB",
				c.prop,
				n>>20,
				(MaxMetadata+8<<20)>>20,
			)
		}
	}
}
