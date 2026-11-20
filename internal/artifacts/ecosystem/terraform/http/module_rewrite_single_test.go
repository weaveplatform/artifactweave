package terraformhttp

import (
	"archive/tar"
	"bytes"
	"compress/gzip"
	"crypto/sha256"
	"encoding/hex"
	"io"
	"net/url"
	"strings"
	"testing"

	"github.com/dsnet/compress/bzip2"
	"github.com/ulikunitz/xz"

	"github.com/weaveplatform/artifactweave/internal/artifacts/adapters/upstream/fakeupstream"
)

func TestAccSingleCompressedModuleDeliveryPreservesOriginalAndWrapsOneFile(t *testing.T) {
	for _, key := range []string{"remote", "virtual"} {
		for _, kind := range []string{"gz", "bz2", "xz"} {
			for _, file := range []struct{ name, body, want string }{
				{"main.tf", `output "identity" { value = "single" }`, `output "identity" { value = "single" }`},
				{"main.tf.json", `{"module":{"child":{"source":"acme/helper/aws"}}}`, "registry.test/" + key + "__acme/helper/aws"},
				{"README.md", "complete non-config file\n", "complete non-config file\n"},
			} {
				t.Run(key+"/"+kind+"/"+file.name, func(t *testing.T) {
					f := newRemoteFixture(t)
					f.modules("1.0.0")
					if key == "virtual" {
						virtualMembers(t, f.fixture, "remote")
					}
					var raw bytes.Buffer
					var compressor io.WriteCloser
					var err error
					switch kind {
					case "gz":
						compressor = gzip.NewWriter(&raw)
					case "bz2":
						compressor, err = bzip2.NewWriter(&raw, nil)
					case "xz":
						compressor, err = xz.NewWriter(&raw)
					}
					if err != nil {
						t.Fatal(err)
					}
					if _, err := io.WriteString(compressor, file.body); err != nil {
						t.Fatal(err)
					}
					if err := compressor.Close(); err != nil {
						t.Fatal(err)
					}
					path := "/archives/" + file.name + "." + kind
					f.up.Set(path, fakeupstream.Resource{Body: raw.Bytes()})
					f.location("1.0.0", path)
					metadata := f.do("GET", prefix+"v1/modules/"+key+"__ns/name/aws/1.0.0/download", "admin")
					expect(t, metadata, 204)
					public, err := url.Parse(metadata.Header().Get("X-Terraform-Get"))
					if err != nil || public.Query().Get("archive") != "tar.gz" {
						t.Fatal("single compressed source did not advertise portable package delivery")
					}
					delivered := f.do("GET", public.String(), "")
					expect(t, delivered, 200)
					gz, err := gzip.NewReader(bytes.NewReader(delivered.Body.Bytes()))
					if err != nil {
						t.Fatal(err)
					}
					defer gz.Close()
					tw := tar.NewReader(gz)
					header, err := tw.Next()
					if err != nil || header.Name != file.name || header.Typeflag != tar.TypeReg {
						t.Fatal("portable package lost the single file's name/type", err)
					}
					body, err := io.ReadAll(tw)
					if err != nil || !strings.Contains(string(body), file.want) {
						t.Fatal("portable package lost file content or registry rewriting", err)
					}
					if _, err := tw.Next(); err != io.EOF {
						t.Fatal("portable package added another file", err)
					}
					original, err := f.db.Items().Get(t.Context(), "remote", true, strings.TrimPrefix(path, "/"))
					if err != nil {
						t.Fatal(err)
					}
					originalHash, wireHash := sha256.Sum256(raw.Bytes()), sha256.Sum256(delivered.Body.Bytes())
					if original.Actual.SHA256 != hex.EncodeToString(originalHash[:]) || delivered.Header().Get("X-Checksum-Sha256") != hex.EncodeToString(wireHash[:]) ||
						originalHash == wireHash {
						t.Fatal("original and delivered digest identities were conflated")
					}
					query := public.Query()
					query.Set("archive", kind)
					public.RawQuery = query.Encode()
					expect(t, f.do("GET", public.String(), ""), 403)
				})
			}
		}
	}
}
