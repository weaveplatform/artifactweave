package lzma2

import (
	"bytes"
	"io"
	"sync"
	"testing"

	"github.com/ulikunitz/xz/lzma"
)

// benchSize is the content and the dictionary of BenchmarkReader2: large
// enough that the dictionary grows ten times, from 64 KiB.
const benchSize = 64 << 20

var benchStreams = sync.OnceValue(func() map[string][]byte {
	content := text(21, benchSize)
	var stored []byte
	for off := 0; off < benchSize; off += 64 << 10 {
		stored = append(stored, raw(off == 0, content[off:off+64<<10])...)
	}
	compressed, err := packed(benchSize, content)
	if err != nil {
		panic(err)
	}
	return map[string][]byte{"text": compressed, "stored": append(stored, 0)}
})

// BenchmarkReader2 decodes 64 MiB of text with a 64 MiB dictionary, with
// this Reader2, whose dictionary grows as it fills, and with ulikunitz/xz's,
// which allocates all of it first: compressed, where decoding costs most,
// and stored in uncompressed chunks, where the copies of the growth weigh
// the most they can.
func BenchmarkReader2(b *testing.B) {
	for _, name := range []string{"text", "stored"} {
		stream := benchStreams()[name]
		for _, impl := range []struct {
			name string
			open func(io.Reader) (io.Reader, error)
		}{
			{"growing", func(r io.Reader) (io.Reader, error) {
				return Reader2Config{DictCap: benchSize}.NewReader2(r)
			}},
			{"ulikunitz", func(r io.Reader) (io.Reader, error) {
				return lzma.Reader2Config{DictCap: benchSize}.NewReader2(r)
			}},
		} {
			b.Run(name+"/"+impl.name, func(b *testing.B) {
				b.SetBytes(benchSize)
				b.ReportAllocs()
				for b.Loop() {
					r, err := impl.open(bytes.NewReader(stream))
					if err != nil {
						b.Fatal(err)
					}
					if n, err := io.Copy(io.Discard, r); err != nil || n != benchSize {
						b.Fatalf("%d bytes, %v", n, err)
					}
				}
			})
		}
	}
}
