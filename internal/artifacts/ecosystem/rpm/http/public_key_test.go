package rpmhttp

import (
	"bytes"
	"context"
	"encoding/gob"
	"net/http"
	"testing"

	accessapi "github.com/weaveplatform/artifactweave/internal/access/api"
	"github.com/weaveplatform/artifactweave/internal/artifacts/adapters/http/itemkit/kittest"
	"github.com/weaveplatform/artifactweave/internal/artifacts/api/repo"
	"github.com/weaveplatform/artifactweave/internal/artifacts/core/virtual"
	"github.com/weaveplatform/artifactweave/internal/artifacts/ecosystem/terraform/terraformtest"
)

func TestAccRPMVirtualCachedPublicKeyIsValidated(t *testing.T) {
	key, err := terraformtest.NewKey()
	if err != nil {
		t.Fatal(err)
	}
	for _, tc := range []struct {
		name, value string
		want        int
	}{{"valid", key.Public, 200}, {"historical private", key.Private, 500}, {"historical mislabeled", key.Public + key.Private, 500}} {
		t.Run(tc.name, func(t *testing.T) {
			w := newItemWorld(t)
			c := *repo.Defaults(&repo.Config{Key: "rpm-virtual", Class: repo.Virtual, PackageType: repo.RPM})
			if err := w.db.Repos().Create(t.Context(), c); err != nil {
				t.Fatal(err)
			}
			k, err := w.kit.Packages(rpmProtocol(w.kit), true)
			if err != nil {
				t.Fatal(err)
			}
			r := kittest.Request(t, "GET", "/artifactory/rpm-virtual/repodata/repomd.xml.key")
			open := c
			open.IncludesPattern = nil
			open.ExcludesPattern = nil
			bound := k.Bind(r, open)
			// Seed exactly the immutable R8 envelope an older process could have
			// produced, without invoking the repaired signing path.
			var body bytes.Buffer
			if err := gob.NewEncoder(&body).Encode(map[string][]byte{"repodata/repomd.xml.key": []byte(tc.value)}); err != nil {
				t.Fatal(err)
			}
			if _, err := k.Merged(
				bound,
				"repodata/repomd.xml",
				"repodata/",
				func(_ *http.Request) (virtual.Entry, error) { return virtual.Entry{Body: body.Bytes()}, nil },
			); err != nil {
				t.Fatal(err)
			}
			rec := w.do("GET", "/artifactory/rpm-virtual/repodata/repomd.xml.key", "admin", "")
			if rec.Code != tc.want {
				t.Fatalf("cached public export status%d want%d (bytes withheld)", rec.Code, tc.want)
			}
			if tc.want == 200 && rec.Body.String() != key.Public {
				t.Fatal("valid cached public bytes changed")
			}
		})
	}
}

func TestAccRPMVirtualGenerationRejectsPrivatePublicField(t *testing.T) {
	w := newItemWorld(t)
	key, err := terraformtest.NewKey()
	if err != nil {
		t.Fatal(err)
	}
	if err := w.db.Keys().
		CreateKeypair(t.Context(), accessapi.Keypair{Name: "legacy", Type: accessapi.KeypairGPG, PrivateKey: key.Private, PublicKey: key.Public + key.Private}); err != nil {
		t.Fatal(err)
	}
	files := map[string][]byte{"repodata/repomd.xml": []byte("manifest")}
	keys := withAccess(t, Config{Store: w.db}).Access.Keys
	if err := signVirtualRPM(context.Background(), keys, repo.Config{KeyPair: "legacy"}, files); err == nil {
		t.Fatal("legacy private-bearing public value was exported")
	}
	if _, ok := files["repodata/repomd.xml.key"]; ok {
		t.Fatal("private-bearing key output was retained")
	}
}
