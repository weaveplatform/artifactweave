package helm_test

import (
	"bytes"
	"fmt"
	"net/http"
	"strings"
	"testing"

	"github.com/ProtonMail/go-crypto/openpgp"
	"github.com/ProtonMail/go-crypto/openpgp/clearsign"
	"github.com/ProtonMail/go-crypto/openpgp/packet"

	"github.com/weaveplatform/artifactweave/internal/fakeupstream"
	"github.com/weaveplatform/artifactweave/pkg/repo"
)

func signHelmProvenance(t *testing.T, body string) []byte {
	t.Helper()
	key, err := openpgp.NewEntity(
		"fixture",
		"throwaway",
		"test@example.invalid",
		&packet.Config{Algorithm: packet.PubKeyAlgoRSA, RSABits: 2048},
	)
	if err != nil {
		t.Fatal(err)
	}
	var output bytes.Buffer
	w, err := clearsign.Encode(&output, key.PrivateKey, nil)
	if err != nil {
		t.Fatal(err)
	}
	if _, err = w.Write([]byte(body)); err != nil {
		t.Fatal(err)
	}
	if err = w.Close(); err != nil {
		t.Fatal(err)
	}
	return output.Bytes()
}

func TestAccRemoteProvenanceIsPreservedAndValidated(t *testing.T) {
	for _, fault := range []string{"valid", "identity", "digest", "yaml", "unsigned", "trailing", "truncated"} {
		t.Run(fault, func(t *testing.T) {
			f := newRemoteFixture(t)
			a, _ := f.chart("web", "1.0.0", "visible/web-1.0.0.tgz")
			f.index(a)
			payload := fmt.Sprintf(
				"apiVersion: v2\nname: web\nversion: 1.0.0\n...\nfiles:\n  web-1.0.0.tgz: sha256:%s\n",
				a.Digest,
			)
			switch fault {
			case "identity":
				payload = strings.Replace(payload, "name: web", "name: other", 1)
			case "digest":
				payload = strings.Replace(payload, a.Digest, strings.Repeat("0", 64), 1)
			case "yaml":
				payload = "name: [bad YAML"
			}
			data := signHelmProvenance(t, payload)
			switch fault {
			case "unsigned":
				data = []byte(payload)
			case "trailing":
				data = append(data, []byte("garbage")...)
			case "truncated":
				data = data[:len(data)/2]
			}
			f.up.Set(
				"/charts/visible/web-1.0.0.tgz.prov",
				fakeupstream.Resource{
					Body:   data,
					Header: http.Header{"Content-Type": []string{"application/octet-stream"}},
				},
			)
			got := f.get("visible/web-1.0.0.tgz.prov", "partial")
			if fault != "valid" {
				expectRemote(t, got, 502)
				if _, err := f.items.Store.Items().
					Get(t.Context(), "remote", true, "visible/web-1.0.0.tgz.prov"); err == nil {
					t.Fatal("invalid provenance published")
				}
				return
			}
			expectRemote(t, got, 200)
			if !bytes.Equal(got.Body.Bytes(), data) {
				t.Fatal("provenance bytes changed")
			}
			f.up.Close()
			f.configure(func(c *repo.Config) { v := true; c.Offline = &v })
			got = f.get("visible/web-1.0.0.tgz.prov", "partial")
			expectRemote(t, got, 200)
			if !bytes.Equal(got.Body.Bytes(), data) {
				t.Fatal("offline provenance bytes changed")
			}
		})
	}
}
