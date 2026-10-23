package support

import (
	"archive/tar"
	"bytes"
	"compress/gzip"
	"crypto/sha1" //nolint:gosec // npm wire shasum
	"crypto/sha512"
	"encoding/base64"
	"encoding/hex"
	"encoding/json"
)

func NPMPublishBody(name, version string) string {
	tgz := NPMTarballOf(name, version)
	s1 := sha1.Sum(tgz) //nolint:gosec // npm's dist.shasum is SHA-1
	s5 := sha512.Sum512(tgz)
	b, _ := json.Marshal(map[string]any{
		"_id": name, "name": name, "dist-tags": map[string]string{"latest": version},
		"versions": map[string]any{version: map[string]any{
			"name": name, "version": version, "dist": map[string]string{
				"shasum":    hex.EncodeToString(s1[:]),
				"integrity": "sha512-" + base64.StdEncoding.EncodeToString(s5[:]),
			},
		}},
		"_attachments": map[string]any{name + "-" + version + ".tgz": map[string]any{
			"content_type": "application/octet-stream",
			"data":         base64.StdEncoding.EncodeToString(tgz),
			"length":       len(tgz),
		}},
	})
	return string(b)
}

func NPMTarballOf(name, version string) []byte {
	var buf bytes.Buffer
	gz := gzip.NewWriter(&buf)
	tw := tar.NewWriter(gz)
	manifest := `{"name":"` + name + `","version":"` + version + `"}`
	_ = tw.WriteHeader(&tar.Header{
		Name: "package/package.json", Mode: 0o644, Size: int64(len(manifest)),
		Typeflag: tar.TypeReg,
	})
	_, _ = tw.Write([]byte(manifest))
	_ = tw.Close()
	_ = gz.Close()
	return buf.Bytes()
}
