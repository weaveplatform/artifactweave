package conda

import (
	"bytes"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"fmt"
)

const ShardIndexFilename = "repodata_shards.msgpack.zst"

type Settings struct{ ShardsEnabled bool }

// ShardIndex is CEP-16 version 1. Hashes are MessagePack binary values, not
// hex strings. The outward view always uses relative URLs into this channel.
type ShardIndex struct {
	BaseURL, ShardsBaseURL string
	Shards                 map[string][]byte
}

func DecodeShardIndex(body []byte) (ShardIndex, error) {
	value, err := decodeMessagePack(body)
	if err != nil {
		return ShardIndex{}, err
	}
	version, err := json.Marshal(value["version"])
	if err != nil || !bytes.Equal(version, []byte("1")) {
		return ShardIndex{}, ErrInvalidRepodata
	}
	info, ok := value["info"].(map[string]any)
	if !ok {
		return ShardIndex{}, ErrInvalidRepodata
	}
	base, baseOK := info["base_url"].(string)
	shardsBase, shardsOK := info["shards_base_url"].(string)
	shards, ok := value["shards"].(map[string]any)
	if !ok || !baseOK || !shardsOK || len(base) > 4096 || len(shardsBase) > 4096 {
		return ShardIndex{}, ErrInvalidRepodata
	}
	out := ShardIndex{BaseURL: base, ShardsBaseURL: shardsBase, Shards: map[string][]byte{}}
	for name, value := range shards {
		hash, ok := value.([]byte)
		if !ok || len(hash) != sha256.Size || !nameRE.MatchString(name) {
			return ShardIndex{}, ErrInvalidRepodata
		}
		out.Shards[name] = bytes.Clone(hash)
	}
	return out, nil
}

// DecodeShard pins the compressed wire bytes before parsing, then validates
// every archive identity and digest using the classic repodata contract.
func DecodeShard(body []byte, id MetadataPath, name, digest string) (Repodata, error) {
	actual := sha256.Sum256(body)
	if !validHexDigest(digest, sha256.Size) || hex.EncodeToString(actual[:]) != digest {
		return Repodata{}, fmt.Errorf("%w: shard digest", ErrInvalidRepodata)
	}
	value, err := decodeMessagePack(body)
	if err != nil {
		return Repodata{}, err
	}
	for _, field := range []string{"packages", "packages.conda"} {
		packages, ok := value[field].(map[string]any)
		if !ok {
			return Repodata{}, ErrInvalidRepodata
		}
		for _, entry := range packages {
			record, ok := entry.(map[string]any)
			if !ok || record["name"] != name {
				return Repodata{}, ErrInvalidRepodata
			}
			for key, size := range map[string]int{"sha256": sha256.Size, "md5": 16} {
				if v, exists := record[key]; exists {
					hash, ok := v.([]byte)
					if !ok || len(hash) != size {
						return Repodata{}, ErrInvalidRepodata
					}
					record[key] = hex.EncodeToString(hash)
				}
			}
		}
	}
	// Unknown top-level fields must not supply the classic subdir/version.
	classic := map[string]any{
		"packages":       value["packages"],
		"packages.conda": value["packages.conda"],
		"removed":        value["removed"],
	}
	raw, err := json.Marshal(classic)
	if err != nil {
		return Repodata{}, ErrInvalidRepodata
	}
	id.Filename = "repodata.json"
	index, err := DecodeRepodata(raw, id)
	if err != nil {
		return Repodata{}, err
	}
	for _, removed := range index.Removed {
		p, _ := ParsePackagePath(IndexPath(id.Channel, id.Subdir, removed))
		if p.Name != name {
			return Repodata{}, ErrInvalidRepodata
		}
		delete(index.Packages, removed)
		delete(index.PackagesConda, removed)
	}
	return index, nil
}

func EncodeShards(index Repodata, channel, subdir, baseURL string) (map[string][]byte, error) {
	if index.Info.Subdir != subdir {
		return nil, ErrInvalidRepodata
	}
	byName := map[string]map[string]any{}
	for field, packages := range map[string]map[string]Record{"packages": index.Packages, "packages.conda": index.PackagesConda} {
		ext := ".tar.bz2"
		if field == "packages.conda" {
			ext = ".conda"
		}
		for filename, rec := range packages {
			if err := validateRecord(filename, channel, subdir, rec, ext); err != nil {
				return nil, err
			}
			shard := byName[rec.Name]
			if shard == nil {
				shard = map[string]any{
					"packages":       map[string]any{},
					"packages.conda": map[string]any{},
					"removed":        []any{},
				}
				byName[rec.Name] = shard
			}
			record, err := jsonMap(rec)
			if err != nil {
				return nil, err
			}
			for _, key := range []string{"sha256", "md5"} {
				hash, _ := record[key].(string)
				if hash == "" {
					delete(record, key)
					continue
				}
				record[key], _ = hex.DecodeString(hash)
			}
			entries, _ := shard[field].(map[string]any)
			entries[filename] = record
		}
	}
	if len(byName) > maxRepodataItems {
		return nil, ErrInvalidRepodata
	}
	files := map[string][]byte{}
	hashes := map[string]any{}
	for name, shard := range byName {
		body, err := encodeMessagePack(shard)
		if err != nil {
			return nil, err
		}
		hash := sha256.Sum256(body)
		files[hex.EncodeToString(hash[:])+".msgpack.zst"] = body
		hashes[name] = hash[:]
	}
	body, err := encodeMessagePack(map[string]any{
		"version": 1,
		"info":    map[string]any{"base_url": baseURL, "shards_base_url": "./", "subdir": subdir},
		"shards":  hashes,
	})
	if err != nil {
		return nil, err
	}
	files[ShardIndexFilename] = body
	return files, nil
}
