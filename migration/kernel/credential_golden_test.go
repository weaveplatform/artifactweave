package kernel_test

import (
	"crypto/sha256"
	"encoding/json"
	"flag"
	"os"
	"path/filepath"
	"testing"
	"time"

	"github.com/google/go-cmp/cmp"

	"github.com/weaveplatform/artifactweave/migration/kernel"
)

var update = flag.Bool("update", false, "rewrite the golden credential reference file")

// TestUnitCredentialReferenceGolden pins the JSON of credential references
// byte for byte. Work items persist a CredentialReference (decision 0033),
// replay scopes hash one (decision 0043) and signed download envelopes carry
// a DownloadCredential, so a change that looks harmless in Go, such as a
// renamed field or tag, strands queued work, makes stored replays miss and
// breaks envelopes already issued. Rewrite the golden file with -update only
// for a deliberate format change.
func TestUnitCredentialReferenceGolden(t *testing.T) {
	revision := sha256.Sum256([]byte("stored password hash"))
	registry := kernel.Principal{
		Name: "ann",
		Scope: kernel.Scope{
			Entries: []kernel.ScopeEntry{
				{Kind: kernel.ScopeGroups, Names: []string{"readers", "release managers"}},
			},
		},
		TokenID: "registry-token",
		// A named zone and nanoseconds pin the offset and precision that
		// encoding/json writes for a token's expiry.
		TokenExpires: time.Date(2026, 11, 5, 14, 30, 15, 123456789, time.FixedZone("BST", 3600)),
		Registry: &kernel.RegistryAccess{
			Realm: "docker-local",
			Grants: []kernel.RegistryGrant{
				{Type: "repository", Name: "library/alpine", Actions: []string{"pull", "push"}},
			},
		},
		Identity:             kernel.IdentityTransient,
		TokenParent:          "parent-token",
		IsTokenAuthenticated: true,
		TokenSubject:         "jfac@test/users/ann",
	}
	noRealm := kernel.Principal{
		Name:         "bob",
		Scope:        kernel.UserScope(),
		TokenID:      "path-registry-token",
		TokenExpires: time.Date(2026, 11, 5, 13, 0, 0, 0, time.UTC),
		Registry: &kernel.RegistryAccess{Grants: []kernel.RegistryGrant{
			{Type: "repository", Name: "docker-local/library/alpine", Actions: []string{"pull"}},
			{Type: "registry", Name: "catalog", Actions: []string{"*"}},
		}},
		IsTokenAuthenticated: true,
		TokenSubject:         "jfac@test/users/bob",
	}
	password := kernel.Principal{
		Name:             "carol",
		Groups:           []string{"readers"},
		Password:         true,
		PasswordRevision: revision,
	}
	token := kernel.Principal{
		Name:                 "ci-bot",
		Scope:                kernel.UserScope(),
		TokenID:              "access-token",
		TokenExpires:         time.Date(2026, 12, 1, 0, 0, 0, 0, time.UTC),
		IsTokenAuthenticated: true,
		TokenSubject:         "jfac@test/users/ci-bot",
	}
	cases := map[string]any{
		"registry":          registry.CredentialReference(),
		"registry-no-realm": noRealm.CredentialReference(),
		"password":          password.CredentialReference(),
		"token":             token.CredentialReference(),
		"anonymous":         kernel.Anonymous.CredentialReference(),
	}
	for name, p := range map[string]kernel.Principal{
		"download-token":     token,
		"download-password":  password,
		"download-anonymous": kernel.Anonymous,
	} {
		ref, err := p.DownloadReference()
		if err != nil {
			t.Fatalf("%s: %v", name, err)
		}
		cases[name] = ref
	}
	got, err := json.MarshalIndent(cases, "", "  ")
	if err != nil {
		t.Fatal(err)
	}
	got = append(got, '\n')
	path := filepath.Join("testdata", "credential_reference.golden.json")
	if *update {
		if err = os.WriteFile(path, got, 0o600); err != nil {
			t.Fatal(err)
		}
	}
	want, err := os.ReadFile(path)
	if err != nil {
		t.Fatal(err)
	}
	if diff := cmp.Diff(string(want), string(got)); diff != "" {
		t.Errorf("%s (-want +got):\n%s", path, diff)
	}
	for name, c := range cases {
		ref, ok := c.(kernel.CredentialReference)
		if !ok {
			continue
		}
		if diff := cmp.Diff(ref, ref.Principal().CredentialReference()); diff != "" {
			t.Errorf("%s through Principal and back (-want +got):\n%s", name, diff)
		}
	}
}
