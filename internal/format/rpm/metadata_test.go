package rpm

import (
	"bytes"
	"compress/gzip"
	"crypto/sha256"
	"encoding/xml"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"
)

func TestEncodeMetadataFromRpmbuildPackage(t *testing.T) {
	t.Parallel()
	body, err := os.ReadFile(filepath.Join("testdata", fixtureName))
	if err != nil {
		t.Fatal(err)
	}
	pkg, err := ReadPackage(
		t.Context(),
		bytes.NewReader(body),
		int64(len(body)),
		"pool/"+fixtureName,
	)
	if err != nil {
		t.Fatal(err)
	}
	pkg.SHA256 = strings.ToLower(hexSHA256(body))
	meta, err := EncodeMetadata([]Package{pkg}, "", time.Unix(1700000000, 0))
	if err != nil {
		t.Fatal(err)
	}
	var manifest repoDoc
	if err := xml.Unmarshal(meta.Files["repodata/repomd.xml"], &manifest); err != nil {
		t.Fatal(err)
	}
	if len(manifest.Data) != 1 || manifest.Data[0].Type != "primary" {
		t.Fatalf("wrong manifest: %+v", manifest)
	}
	primaryPath := manifest.Data[0].Location.Href
	compressed, ok := meta.Files[primaryPath]
	if !ok || manifest.Data[0].Checksum.Value != hexSHA256(compressed) {
		t.Fatal("repomd does not bind primary index")
	}
	decodedPath, decodedSHA, err := DecodeRepomd(meta.Files["repodata/repomd.xml"], "")
	if err != nil || decodedPath != primaryPath || decodedSHA != manifest.Data[0].Checksum.Value {
		t.Fatalf("cannot decode generated manifest: %s %s %v", decodedPath, decodedSHA, err)
	}
	decoded, err := DecodePrimary(compressed, "", decodedSHA)
	if err != nil || len(decoded) != 1 || decoded[0].Path != pkg.Path {
		t.Fatalf("cannot decode generated primary: %+v %v", decoded, err)
	}
	gz, err := gzip.NewReader(bytes.NewReader(compressed))
	if err != nil {
		t.Fatal(err)
	}
	raw, err := io.ReadAll(gz)
	if err != nil {
		t.Fatal(err)
	}
	if err := gz.Close(); err != nil {
		t.Fatal(err)
	}
	var primary primaryDoc
	if err := xml.Unmarshal(raw, &primary); err != nil {
		t.Fatal(err)
	}
	if primary.Packages != 1 || len(primary.Items) != 1 ||
		primary.Items[0].Name != pkg.Name ||
		primary.Items[0].Location.Href != pkg.Path ||
		primary.Items[0].Checksum.Value != pkg.SHA256 {
		t.Fatalf("wrong primary index: %+v", primary)
	}
}

func TestDecodePrimaryRejectsChangedBytes(t *testing.T) {
	t.Parallel()
	meta, err := EncodeMetadata(nil, "", time.Unix(1700000000, 0))
	if err != nil {
		t.Fatal(err)
	}
	path, sha, err := DecodeRepomd(meta.Files["repodata/repomd.xml"], "")
	if err != nil {
		t.Fatal(err)
	}
	changed := bytes.Clone(meta.Files[path])
	changed[len(changed)-1] ^= 0xff
	if _, err := DecodePrimary(changed, "", sha); err == nil {
		t.Fatal("modified metadata accepted")
	}
}

func hexSHA256(body []byte) string {
	return fmt.Sprintf("%x", sha256.Sum256(body))
}
