package terraformhttp

import (
	"context"
	"encoding/json"
	"strings"
	"testing"

	"github.com/weaveplatform/artifactweave/internal/access/api"
)

func TestAccProviderStoredPublicKeyPrivacy(t *testing.T) {
	f, key, _ := providerFixture(t)
	marker := ""
	for _, line := range strings.Split(key.Private, "\n") {
		if len(line) > 40 && !strings.HasPrefix(line, "-----") {
			marker = line
			break
		}
	}
	if marker == "" {
		t.Fatal("synthetic key lacks packet marker")
	}
	healthy := f.do("GET", providerBase+"3.3.2/download/linux/amd64", "admin")
	if healthy.Code != 200 || strings.Contains(healthy.Body.String(), marker) {
		t.Fatal("valid provider public control failed (bytes withheld)")
	}
	t.Run("missing secondary retains primary public bytes", func(t *testing.T) {
		c, err := f.db.Repos().Get(t.Context(), "provider")
		if err != nil {
			t.Fatal(err)
		}
		c.SecondaryKeyPairRef = "missing-secondary"
		if err := f.db.Repos().Update(t.Context(), c); err != nil {
			t.Fatal(err)
		}
		t.Cleanup(func() {
			c.SecondaryKeyPairRef = ""
			if err := f.db.Repos().Update(context.WithoutCancel(t.Context()), c); err != nil {
				t.Error(err)
			}
		})
		rec := f.do("GET", providerBase+"3.3.2/download/linux/amd64", "admin")
		var doc struct {
			SigningKeys struct {
				Keys []publicKey `json:"gpg_public_keys"`
			} `json:"signing_keys"`
		}
		if rec.Code != 200 || json.Unmarshal(rec.Body.Bytes(), &doc) != nil ||
			len(doc.SigningKeys.Keys) != 1 || doc.SigningKeys.Keys[0].ID != key.KeyID() ||
			doc.SigningKeys.Keys[0].Armor != key.Public {
			t.Fatal("missing secondary changed the valid primary export (bytes withheld)")
		}
	})
	for _, tc := range []struct {
		name, value string
		secondary   bool
	}{
		{"primary private", key.Private, false},
		{"primary mislabeled private", strings.ReplaceAll(key.Private, "PGP PRIVATE KEY BLOCK", "PGP PUBLIC KEY BLOCK"), false},
		{"primary trailing private", key.Public + key.Private, false},
		{"secondary trailing private", key.Public + key.Private, true},
	} {
		t.Run(tc.name, func(t *testing.T) {
			pair := api.Keypair{Name: "primary", Type: api.KeypairGPG, PublicKey: tc.value}
			c, err := f.db.Repos().Get(t.Context(), "provider")
			if err != nil {
				t.Fatal(err)
			}
			if tc.secondary {
				if err := f.db.Keys().UpdateKeypair(t.Context(), api.Keypair{Name: "primary", Type: api.KeypairGPG, PublicKey: key.Public}); err != nil {
					t.Fatal(err)
				}
				pair.Name = "secondary"
				if err := f.db.Keys().CreateKeypair(t.Context(), pair); err != nil {
					t.Fatal(err)
				}
				c.SecondaryKeyPairRef = pair.Name
				if err := f.db.Repos().Update(t.Context(), c); err != nil {
					t.Fatal(err)
				}
			} else if err := f.db.Keys().UpdateKeypair(t.Context(), pair); err != nil {
				t.Fatal(err)
			}
			rec := f.do("GET", providerBase+"3.3.2/download/linux/amd64", "admin")
			exposed := strings.Contains(rec.Body.String(), marker)
			t.Logf("provider public export status%d privatePacketPresent=%t", rec.Code, exposed)
			if rec.Code != 409 || exposed {
				t.Fatal("provider public export did not fail closed (bytes withheld)")
			}
		})
	}
}
