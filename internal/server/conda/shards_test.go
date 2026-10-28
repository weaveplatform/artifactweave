package conda

import (
	"errors"
	"io"
	"net/url"
	"strings"
	"testing"
	"testing/iotest"

	condafmt "github.com/weaveplatform/artifactweave/internal/format/conda"
)

func TestUnitCondaShardURLsRemainInsideConfiguredSource(t *testing.T) {
	id, _ := condafmt.ParseMetadataPath("team/noarch/" + condafmt.ShardIndexFilename)
	for _, tc := range []struct {
		root, response, packages, shards string
		valid                            bool
	}{
		{"https://up.example/channel", "https://up.example/channel/team/noarch/repodata_shards.msgpack.zst", "./", "./shards/", true},
		{"https://up.example/channel", "https://up.example/channel/team/noarch/repodata_shards.msgpack.zst", "https://up.example/channel/team/noarch/", "../../shards/", true},
		{"https://up.example/channel", "https://evil.example/index", "./", "./", false},
		{"https://up.example/channel", "https://up.example/channel/team/noarch/index", "https://evil.example/", "./", false},
		{"https://up.example/channel", "https://up.example/channel/team/noarch/index", "./", "http://up.example/channel/shards/", false},
		{"https://up.example/channel", "https://up.example/channel/team/noarch/index", "./", "../../../shards/", false},
		{"https://up.example/channel", "https://up.example/channel/team/noarch/index", "./", "./shards/?token=secret", false},
		{"https://up.example/channel", "https://up.example/channel/team/noarch/index", "./", "https://user:secret@up.example/channel/shards/", false},
		{"https://up.example/channel", "https://up.example/channel/team/noarch/index", "./", "./shards/#fragment", false},
		{"https://up.example/channel", "https://up.example/channel/team/noarch/index", "./", "./%2fshards/", false},
		{"https://up.example/channel", "https://up.example/channel/team/noarch/index", "./", "./shards", false},
		{"https://up.example/channel", "https://up.example/channel/team/noarch/index", "../", "./", false},
		{"https://up.example/%zz", "", "./", "./", false},
		{"https://up.example/channel", "%zz", "./", "./", false},
		{"https://up.example/channel", "https://up.example/channel/team/noarch/index", "%zz", "./", false},
		{"https://up.example/channel", "https://up.example/channel/team/noarch/index", "./", "%zz", false},
	} {
		t.Run(tc.shards+tc.packages+tc.response, func(t *testing.T) {
			_, err := shardURLs(
				tc.root,
				id,
				tc.response,
				condafmt.ShardIndex{BaseURL: tc.packages, ShardsBaseURL: tc.shards},
			)
			if (err == nil) != tc.valid {
				t.Fatal(err)
			}
		})
	}
	u, _ := url.Parse("https://up.example/")
	if shardDestination(nil, u) || shardDestination(u, nil) {
		t.Fatal("nil URL permitted")
	}
}

func TestUnitCondaShardMetadataReaderBounds(t *testing.T) {
	for _, size := range []int64{-1, 0, 4, 6, maxShardSnapshotBytes + 1} {
		if _, err := readShardMetadata(strings.NewReader("hello"), size); err == nil {
			t.Fatal(size)
		}
	}
	if _, err := readShardMetadata(iotest.ErrReader(errors.New("failed")), 5); err == nil {
		t.Fatal("read failure ignored")
	}
	body, err := readShardMetadata(io.LimitReader(strings.NewReader("hello"), 5), 5)
	if err != nil || string(body) != "hello" {
		t.Fatal(err)
	}
}
