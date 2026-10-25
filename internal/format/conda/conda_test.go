package conda

import (
	"bytes"
	"crypto/sha256"
	"encoding/hex"
	"os"
	"path/filepath"
	"testing"
)

const fixtureName = "artifactweave-conda-fixture-1.0.0-0.tar.bz2"

func TestUnitParsePaths(t *testing.T) {
	for _, valid := range []string{
		"noarch/" + fixtureName,
		"main/noarch/" + fixtureName,
		"linux-64/demo-1.0-py311_0.conda",
	} {
		if _, ok := ParsePackagePath(valid); !ok {
			t.Errorf("valid package path refused: %q", valid)
		}
	}
	for _, invalid := range []string{
		"../noarch/" + fixtureName,
		"noarch/../../" + fixtureName,
		"noarch/a.zip",
		"linux-64/demo-1.0.conda",
		"unknown/demo-1.0-0.conda",
		"noarch/DEMO-1.0-0.conda",
		"main//noarch/demo-1.0-0.conda",
	} {
		if _, ok := ParsePackagePath(invalid); ok {
			t.Errorf("invalid package path accepted: %q", invalid)
		}
	}
	if _, ok := ParseMetadataPath("main/noarch/repodata.json.zst"); !ok {
		t.Fatal("valid subchannel metadata refused")
	}
	if _, ok := ParseMetadataPath("main/noarch/notices.json"); !ok {
		t.Fatal("valid notices path refused")
	}
}

func TestUnitPackageIndexAndRepodata(t *testing.T) {
	data, err := os.ReadFile(filepath.Join("testdata", fixtureName))
	if err != nil {
		t.Fatal(err)
	}
	id, ok := ParsePackagePath("noarch/" + fixtureName)
	if !ok {
		t.Fatal("fixture path rejected")
	}
	rec, err := ReadPackage(t.Context(), bytes.NewReader(data), int64(len(data)), id)
	if err != nil {
		t.Fatal(err)
	}
	if rec.Name != id.Name || rec.Version != id.Version || rec.Build != id.Build ||
		rec.Subdir != "noarch" || rec.Noarch != "generic" {
		t.Fatalf("package identity %+v", rec)
	}
	digest := sha256.Sum256(data)
	rec.SHA256 = hex.EncodeToString(digest[:])
	rec.Size = int64(len(data))
	index := EmptyRepodata("noarch")
	index.Packages[id.Filename] = rec
	encoded, err := EncodeRepodata(index, "", "noarch")
	if err != nil {
		t.Fatal(err)
	}
	for _, name := range []string{"repodata.json", "repodata.json.zst", "current_repodata.json"} {
		mp, ok := ParseMetadataPath("noarch/" + name)
		if !ok {
			t.Fatalf("metadata path %s refused", name)
		}
		got, err := DecodeRepodata(encoded[name], mp)
		if err != nil || got.Packages[id.Filename].SHA256 != rec.SHA256 {
			t.Fatalf("decode %s: %+v, %v", name, got, err)
		}
	}
	// Repodata must never disclose an arbitrary filename or a digest that
	// does not describe the package selected by the channel path.
	bad := bytes.ReplaceAll(encoded["repodata.json"], []byte(fixtureName), []byte("../bad.tar.bz2"))
	mp, _ := ParseMetadataPath("noarch/repodata.json")
	if _, err := DecodeRepodata(bad, mp); err == nil {
		t.Fatal("traversal package in repodata was accepted")
	}
	bad = bytes.ReplaceAll(encoded["repodata.json"], []byte(rec.SHA256), []byte("bad"))
	if _, err := DecodeRepodata(bad, mp); err == nil {
		t.Fatal("bad package digest was accepted")
	}
}

func TestUnitPackageIdentityCannotBeForged(t *testing.T) {
	data, err := os.ReadFile(filepath.Join("testdata", fixtureName))
	if err != nil {
		t.Fatal(err)
	}
	id, _ := ParsePackagePath("noarch/other-1.0.0-0.tar.bz2")
	if _, err := ReadPackage(t.Context(), bytes.NewReader(data), int64(len(data)), id); err == nil {
		t.Fatal("package indexed under another name")
	}
}

func TestUnitRealModernCondaPackage(t *testing.T) {
	const name = "ca-certificates-2026.7.22-hbd8a1cb_0.conda"
	data, err := os.ReadFile(filepath.Join("testdata", name))
	if err != nil {
		t.Fatal(err)
	}
	id, ok := ParsePackagePath("noarch/" + name)
	if !ok {
		t.Fatal("real .conda path rejected")
	}
	rec, err := ReadPackage(t.Context(), bytes.NewReader(data), int64(len(data)), id)
	if err != nil {
		t.Fatal(err)
	}
	if rec.Name != "ca-certificates" || rec.Version != "2026.7.22" ||
		rec.Build != "hbd8a1cb_0" || rec.Subdir != "noarch" {
		t.Fatalf("real .conda identity: %+v", rec)
	}
}
