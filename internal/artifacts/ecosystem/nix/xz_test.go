package nix

import (
	"bytes"
	"encoding/binary"
	"errors"
	"hash/crc32"
	"runtime"
	"testing"
)

// A NAR whose xz stream lost its index and footer is no valid archive, and
// a channel tarball whose stream lost them is no valid channel.
func TestUnitNARsNeedTheirWholeXZStream(t *testing.T) {
	info, archive, _ := fixture(t)
	cut := archive[:len(archive)-xzTail(t, archive)]
	if _, err := ReadArchive(
		bytes.NewReader(cut),
		int64(len(cut)),
		"xz",
	); !errors.Is(
		err,
		ErrInvalid,
	) {
		t.Fatalf("cut NAR: %v", err)
	}
	if a, err := ReadArchive(
		bytes.NewReader(archive),
		int64(len(archive)),
		"xz",
	); err != nil ||
		!a.Matches(info) {
		t.Fatalf("whole NAR: %v", err)
	}
}

func TestUnitChannelsNeedTheirWholeXZStream(t *testing.T) {
	tarball := compressXZ(t, channelTar(t, 0))
	if err := ReadChannel(bytes.NewReader(tarball), int64(len(tarball))); err != nil {
		t.Fatal(err)
	}
	cutTarball := tarball[:len(tarball)-xzTail(t, tarball)]
	if err := ReadChannel(
		bytes.NewReader(cutTarball),
		int64(len(cutTarball)),
	); !errors.Is(
		err,
		ErrInvalid,
	) {
		t.Fatalf("cut channel tarball: %v", err)
	}
}

// xzTail is the length of the index and footer that end data, a stream
// that holds no stream padding: the footer's backward size plus the
// footer.
func xzTail(t *testing.T, data []byte) int {
	t.Helper()
	footer := data[len(data)-12:]
	if !bytes.Equal(footer[10:], []byte("YZ")) {
		t.Fatal("no footer at the end")
	}
	return int(binary.LittleEndian.Uint32(footer[4:8])+1)*4 + 12
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

// A NAR whose xz block header names a 4 GiB dictionary is refused before
// anything is allocated, as XZ for Java refuses a dictionary above 1.5 GiB:
// before the shared reader, ReadArchive allocated all 4 GiB before it read
// a byte (#1332).
func TestUnitXZDictionaryIsBounded(t *testing.T) {
	nar := regular("hello\n")
	// Property 20 names 2 MiB: the same file under it is a valid NAR.
	small := storedXZ(nar, 20)
	if a, err := ReadArchive(bytes.NewReader(small), int64(len(small)), "xz"); err != nil ||
		a.NarSize != int64(len(nar)) {
		t.Fatalf("NAR under a 2 MiB dictionary: %+v %v", a, err)
	}
	archive := storedXZ(nar, 40)
	var before, after runtime.MemStats
	runtime.ReadMemStats(&before)
	_, err := ReadArchive(bytes.NewReader(archive), int64(len(archive)), "xz")
	runtime.ReadMemStats(&after)
	if !errors.Is(err, ErrInvalid) {
		t.Fatalf("NAR under a 4 GiB dictionary: %v, want ErrInvalid", err)
	}
	if n := after.TotalAlloc - before.TotalAlloc; n > 16<<20 {
		t.Errorf("refusing it allocated %d MiB, want at most 16 MiB", n>>20)
	}
}
