package conda

import (
	"bytes"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"math"
	"strings"
	"testing"

	"github.com/klauspost/compress/zstd"
	"github.com/tinylib/msgp/msgp"
)

func shardRecord() Record {
	return Record{
		Name:        "demo",
		Version:     "1",
		Build:       "0",
		BuildNumber: 0,
		Size:        42,
		Subdir:      "noarch",
		Depends:     []string{"python >=3"},
		SHA256:      strings.Repeat("a", 64),
		MD5:         strings.Repeat("b", 32),
	}
}

func compressMessage(t *testing.T, raw []byte) []byte {
	t.Helper()
	e, err := zstd.NewWriter(nil, zstd.WithEncoderConcurrency(1))
	if err != nil {
		t.Fatal(err)
	}
	defer func() { _ = e.Close() }()
	return e.EncodeAll(raw, nil)
}

func encodeMessage(t *testing.T, doc map[string]any) []byte {
	t.Helper()
	body, err := encodeMessagePack(doc)
	if err != nil {
		t.Fatal(err)
	}
	return body
}

func TestUnitCondaShardRoundTripAndDeterministicIdentity(t *testing.T) {
	id, _ := ParseMetadataPath("team/noarch/" + ShardIndexFilename)
	index := EmptyRepodata("noarch")
	index.Packages["demo-1-0.tar.bz2"] = shardRecord()
	index.PackagesConda["demo-1-0.conda"] = shardRecord()
	files, err := EncodeShards(index, "team", "noarch", "./")
	if err != nil {
		t.Fatal(err)
	}
	shards, err := DecodeShardIndex(files[ShardIndexFilename])
	if err != nil || len(shards.Shards) != 1 || shards.BaseURL != "./" ||
		shards.ShardsBaseURL != "./" {
		t.Fatalf("%+v %v", shards, err)
	}
	digest := hex.EncodeToString(shards.Shards["demo"])
	body := files[digest+".msgpack.zst"]
	decoded, err := DecodeShard(body, id, "demo", digest)
	if err != nil || decoded.Packages["demo-1-0.tar.bz2"].SHA256 != strings.Repeat("a", 64) ||
		decoded.PackagesConda["demo-1-0.conda"].MD5 != strings.Repeat("b", 32) {
		t.Fatalf("%+v %v", decoded, err)
	}
	for range 5 {
		again, err := EncodeShards(index, "team", "noarch", "./")
		if err != nil || !bytes.Equal(again[ShardIndexFilename], files[ShardIndexFilename]) ||
			!bytes.Equal(again[digest+".msgpack.zst"], body) {
			t.Fatalf("nondeterministic shard: %v", err)
		}
	}
	for _, filename := range []string{ShardIndexFilename, digest + ".msgpack.zst"} {
		if _, ok := ParseMetadataPath("team/noarch/" + filename); !ok {
			t.Fatal(filename)
		}
	}
	for _, filename := range []string{"A" + digest[1:] + ".msgpack.zst", digest + ".zst", "bad.msgpack.zst"} {
		if _, ok := ParseMetadataPath("noarch/" + filename); ok {
			t.Fatal(filename)
		}
	}
}

func TestUnitCondaMessagePackRejectsUnboundedAndAmbiguousInput(t *testing.T) {
	deep := append(bytes.Repeat([]byte{0x91}, 34), 0xc0)
	for name, raw := range map[string][]byte{
		"empty": nil, "huge map": {0xdf, 0xff, 0xff, 0xff, 0xff}, "huge array": {0xdd, 0xff, 0xff, 0xff, 0xff},
		"truncated array": {0x92, 0xc0}, "truncated map": {0x81, 0xa1, 'a'}, "truncated string": {0xa2, 'a'},
		"duplicate": {0x82, 0xa1, 'a', 0xc0, 0xa1, 'a', 0xc0}, "nonstring key": {0x81, 0x01, 0xc0},
		"utf8 key": {0x81, 0xa1, 0xff, 0xc0}, "utf8 value": {0x81, 0xa1, 'a', 0xa1, 0xff}, "reserved": {0xc1},
		"depth": deep, "trailing": {0x80, 0x80}, "scalar": {0x01},
	} {
		t.Run(name, func(t *testing.T) {
			if _, err := decodeMessagePack(
				compressMessage(t, raw),
			); !errors.Is(
				err,
				ErrInvalidRepodata,
			) {
				t.Fatal(err)
			}
		})
	}
	for _, body := range [][]byte{nil, []byte("bad zstd"), bytes.Repeat([]byte{0}, maxRepodataBytes+1), compressMessage(t, bytes.Repeat([]byte{0}, maxRepodataBytes+1))} {
		if _, err := decodeMessagePack(body); err == nil {
			t.Fatal("accepted oversized or malformed zstd")
		}
	}
	budget := 0
	if _, _, err := unpack([]byte{0xc0}, 0, &budget); err == nil {
		t.Fatal("node budget ignored")
	}
	budget = 1
	if _, _, err := unpack([]byte{0x92, 0xc0, 0xc0}, 0, &budget); err == nil {
		t.Fatal("array budget ignored")
	}
	budget = 1
	if _, _, err := unpack([]byte{0x81, 0xa1, 'a', 0xc0}, 0, &budget); err == nil {
		t.Fatal("map budget ignored")
	}
}

func TestUnitCondaMessagePackScalarsAndEncodingFailures(t *testing.T) {
	doc := map[string]any{
		"nil":   nil,
		"bool":  true,
		"int":   int64(-1),
		"uint":  uint64(math.MaxUint64),
		"float": 1.5,
		"bin":   []byte{1, 2},
		"str":   "hello",
		"array": []any{json.Number("10")},
	}
	got, err := decodeMessagePack(encodeMessage(t, doc))
	if err != nil || got["bool"] != true || got["str"] != "hello" {
		t.Fatalf("%+v %v", got, err)
	}
	raw := msgp.AppendMapHeader(nil, 1)
	raw = msgp.AppendFloat32(msgp.AppendString(raw, "float"), 1.25)
	if _, err := decodeMessagePack(compressMessage(t, raw)); err != nil {
		t.Fatal(err)
	}
	for _, value := range []any{make(chan int), map[string]any{"bad": make(chan int)}, []any{make(chan int)}} {
		if _, err := pack(nil, value); err == nil {
			t.Fatal("unsupported value encoded")
		}
	}
	if _, err := encodeMessagePack(map[string]any{"bad": make(chan int)}); err == nil {
		t.Fatal("unsupported document encoded")
	}
	if _, err := jsonMap(make(chan int)); err == nil {
		t.Fatal("unsupported JSON encoded")
	}
	if _, err := jsonMap("scalar"); err == nil {
		t.Fatal("scalar JSON accepted")
	}
}

func TestUnitCondaShardIndexRejectsInvalidStructure(t *testing.T) {
	for name, mutate := range map[string]func(map[string]any){
		"version": func(d map[string]any) { d["version"] = 2 }, "info": func(d map[string]any) { d["info"] = nil },
		"base": func(d map[string]any) { d["info"] = map[string]any{"base_url": 1, "shards_base_url": "./"} },
		"long base": func(d map[string]any) {
			d["info"] = map[string]any{"base_url": strings.Repeat("x", 4097), "shards_base_url": "./"}
		},
		"shards":       func(d map[string]any) { d["shards"] = nil },
		"hex digest":   func(d map[string]any) { d["shards"] = map[string]any{"demo": strings.Repeat("a", 64)} },
		"short digest": func(d map[string]any) { d["shards"] = map[string]any{"demo": []byte{1}} },
		"name":         func(d map[string]any) { d["shards"] = map[string]any{"../demo": make([]byte, 32)} },
	} {
		t.Run(name, func(t *testing.T) {
			d := map[string]any{
				"version": 1,
				"info":    map[string]any{"base_url": "./", "shards_base_url": "./"},
				"shards":  map[string]any{},
			}
			mutate(d)
			if _, err := DecodeShardIndex(encodeMessage(t, d)); err == nil {
				t.Fatal("invalid index accepted")
			}
		})
	}
	if _, err := DecodeShardIndex([]byte("bad")); err == nil {
		t.Fatal("bad compression accepted")
	}
}

func TestUnitCondaShardIntegrityIdentityAndRemovedRecords(t *testing.T) {
	id, _ := ParseMetadataPath("noarch/" + ShardIndexFilename)
	idx := EmptyRepodata("noarch")
	idx.Packages["demo-1-0.tar.bz2"] = shardRecord()
	files, _ := EncodeShards(idx, "", "noarch", "./")
	si, _ := DecodeShardIndex(files[ShardIndexFilename])
	hash := hex.EncodeToString(si.Shards["demo"])
	original, _ := decodeMessagePack(files[hash+".msgpack.zst"])
	for name, mutate := range map[string]func(map[string]any){
		"map":    func(d map[string]any) { d["packages"] = nil },
		"record": func(d map[string]any) { d["packages"] = map[string]any{"demo-1-0.tar.bz2": 1} },
		"name": func(d map[string]any) {
			d["packages"] = map[string]any{"demo-1-0.tar.bz2": map[string]any{"name": "other"}}
		},
		"digest": func(d map[string]any) {
			p, _ := d["packages"].(map[string]any)
			r, _ := p["demo-1-0.tar.bz2"].(map[string]any)
			r["sha256"] = "hex"
		},
		"size": func(d map[string]any) {
			p, _ := d["packages"].(map[string]any)
			r, _ := p["demo-1-0.tar.bz2"].(map[string]any)
			r["size"] = 0
		},
		"removed identity": func(d map[string]any) { d["removed"] = []any{"other-1-0.tar.bz2"} },
		"removed path":     func(d map[string]any) { d["removed"] = []any{"../demo-1-0.tar.bz2"} },
	} {
		t.Run(name, func(t *testing.T) {
			doc, _ := decodeMessagePack(files[hash+".msgpack.zst"])
			mutate(doc)
			body := encodeMessage(t, doc)
			digest := sha256.Sum256(body)
			if _, err := DecodeShard(body, id, "demo", hex.EncodeToString(digest[:])); err == nil {
				t.Fatal("invalid shard accepted")
			}
		})
	}
	original["removed"] = []any{"demo-1-0.tar.bz2"}
	body := encodeMessage(t, original)
	sum := sha256.Sum256(body)
	got, err := DecodeShard(body, id, "demo", hex.EncodeToString(sum[:]))
	if err != nil || len(got.Packages) != 0 {
		t.Fatalf("removed record still indexed: %+v %v", got, err)
	}
	for _, body := range [][]byte{[]byte("bad"), files[hash+".msgpack.zst"]} {
		if _, err := DecodeShard(body, id, "demo", strings.Repeat("0", 64)); err == nil {
			t.Fatal("digest mismatch accepted")
		}
	}
	bad := []byte("bad")
	sum = sha256.Sum256(bad)
	if _, err := DecodeShard(bad, id, "demo", hex.EncodeToString(sum[:])); err == nil {
		t.Fatal("bad compression accepted")
	}
	if _, err := EncodeShards(idx, "", "linux-64", "./"); err == nil {
		t.Fatal("wrong subdir encoded")
	}
	idx.Packages["bad"] = shardRecord()
	if _, err := EncodeShards(idx, "", "noarch", "./"); err == nil {
		t.Fatal("bad filename encoded")
	}
}

func TestUnitCondaMessagePackUnknownExtensionsAndCollectionLimits(t *testing.T) {
	for _, raw := range [][]byte{{0x81, 0xa1, 'x', 0xd4, 0x05, 0x01}, {0x81, 0xa1, 'x', 0xd6, 0xff, 0x00, 0x00, 0x00, 0x00}} {
		if _, err := decodeMessagePack(compressMessage(t, raw)); err != nil {
			t.Fatal(err)
		}
	}
	if _, err := decodeMessagePack(
		compressMessage(t, []byte{0x81, 0xa1, 'x', 0xc9, 0xff, 0xff, 0xff, 0xff, 0x05}),
	); err == nil {
		t.Fatal("unbounded extension accepted")
	}
	if _, err := pack(nil, make([]any, maxRepodataItems+1)); err == nil {
		t.Fatal("oversized array encoded")
	}
	oversized := make(map[string]any, maxRepodataItems+1)
	for i := range maxRepodataItems + 1 {
		oversized[fmt.Sprint(i)] = nil
	}
	if _, err := pack(nil, oversized); err == nil {
		t.Fatal("oversized map encoded")
	}
}
