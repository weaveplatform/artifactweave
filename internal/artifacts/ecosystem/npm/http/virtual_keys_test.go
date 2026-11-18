package npmhttp_test

import (
	"context"
	"encoding/json"
	"fmt"
	"net/http"
	"strconv"
	"strings"
	"testing"
	"time"

	"github.com/weaveplatform/artifactweave/internal/access/accesstest"
	"github.com/weaveplatform/artifactweave/internal/access/api"
	"github.com/weaveplatform/artifactweave/internal/access/api/accessapitest"
	"github.com/weaveplatform/artifactweave/internal/artifacts/adapters/upstream/fakeupstream"
	"github.com/weaveplatform/artifactweave/internal/artifacts/api/repo"
	npmfmt "github.com/weaveplatform/artifactweave/internal/artifacts/ecosystem/npm"
	npmhttp "github.com/weaveplatform/artifactweave/internal/artifacts/ecosystem/npm/http"
	"github.com/weaveplatform/artifactweave/internal/kernel"
)

type observedSigningKeys struct {
	api.Keys
	calls int
}

func (k *observedSigningKeys) Keypair(ctx context.Context, name string) (api.Keypair, error) {
	k.calls++
	return k.Keys.Keypair(ctx, name)
}

func TestAccNPMConfiguredKeysCheckReadAndPolicyBeforeKeyStore(t *testing.T) {
	for _, gate := range []string{"read", "policy", "authorized-store-fault"} {
		t.Run(gate, func(t *testing.T) {
			f := newFixture(t)
			keys := &observedSigningKeys{Keys: &accessapitest.Keys{Err: fmt.Errorf("keypair database: %w", kernel.ErrStore)}}
			f.sign(t, keys, "configured", "")
			user, want := "reader", 500
			switch gate {
			case "read":
				user, want = "nobody", 403
			case "policy":
				c, err := f.db.Repos().Get(t.Context(), "npm-local")
				if err != nil {
					t.Fatal(err)
				}
				c.ExcludesPattern = new(".npm/keys.json")
				if err := f.db.Repos().Update(t.Context(), c); err != nil {
					t.Fatal(err)
				}
				want = 404
			}
			response := f.do(http.MethodGet, keysPath, user)
			wantStatus(t, response, want)
			if gate != "authorized-store-fault" && keys.calls != 0 {
				t.Fatal("key store reached before document gates", keys.calls)
			}
			if gate == "authorized-store-fault" && keys.calls != 1 {
				t.Fatal("authorized read did not retain real store fault", keys.calls)
			}
		})
	}
}

func TestAccNPMVirtualKeysMergeSigningAndUnsignedMembers(t *testing.T) {
	f := newRemoteFixture(t)
	pair, pub := ecdsaPair(t, "local-key")
	local, err := npmfmt.PublicKey(pub)
	if err != nil {
		t.Fatal(err)
	}
	keys := &accessapitest.Keys{Keypairs: map[string]api.Keypair{"local-key": pair}}
	f.sign(t, keys, "local-key", "")
	f.remount(t, npmhttp.Options{RemoteSources: f.svc, KeyPairs: keys})
	for _, c := range []repo.Config{
		{Key: "unsigned", Class: repo.Local, PackageType: repo.NPM},
		{Key: "inner", Class: repo.Virtual, PackageType: repo.NPM, Repositories: []string{"unsigned", "npm-local"}},
		{Key: "npm-all", Class: repo.Virtual, PackageType: repo.NPM, Repositories: []string{"inner", "npm-remote"}},
	} {
		if err := f.db.Repos().Create(t.Context(), *repo.Defaults(&c)); err != nil {
			t.Fatal(err)
		}
	}
	remote := `{"keys":[{"keyid":"` + local.KeyID + `","key":"different-losing-key","expires":"2000-01-01"},{"keyid":"remote","key":"remote-key","expires":42,"future":{"keep":true}}],"top-level":"not-a-key"}`
	f.setKeys(remote, "application/json")
	for _, target := range []string{keysPath, remoteKeys} {
		wantStatus(t, f.do("GET", target, "reader"), 200)
	}
	response := f.do("GET", "/artifactory/api/npm/npm-all/-/npm/v1/keys", "reader")
	wantStatus(t, response, 200)
	var doc struct {
		Keys []map[string]json.RawMessage `json:"keys"`
	}
	if err := json.Unmarshal(response.Body.Bytes(), &doc); err != nil {
		t.Fatal(err)
	}
	if len(doc.Keys) != 2 || string(doc.Keys[0]["key"]) != strconv.Quote(local.Key) || string(doc.Keys[1]["expires"]) != "42" || string(doc.Keys[1]["future"]) != `{"keep":true}` {
		t.Fatal(response.Body.String())
	}
	if response.Header().Get("Content-Type") != "application/json" || response.Header().Get("ETag") != "" {
		t.Fatal("virtual keys must use local document headers", response.Header())
	}
	head := f.do("HEAD", "/artifactory/api/npm/npm-all/-/npm/v1/keys", "reader")
	wantStatus(t, head, 200)
	if head.Header().Get("Content-Type") != "application/json" || head.Header().Get("ETag") != "" {
		t.Fatal(head.Header())
	}
	// An unsigned upstream's404 must not hide a readable signing member.
	f.clock.Advance(6 * time.Second)
	f.up.Delete(upstreamKeys)
	unsigned := f.do("GET", "/artifactory/api/npm/npm-all/-/npm/v1/keys", "reader")
	wantStatus(t, unsigned, 200)
	if err := json.Unmarshal(unsigned.Body.Bytes(), &doc); err != nil || len(doc.Keys) != 1 {
		t.Fatal(unsigned.Body.String(), err)
	}
}

func npmVirtualKeysFixture(t *testing.T) (*remoteFixture, *observedSigningKeys) {
	t.Helper()
	f := newRemoteFixture(t)
	pair, _ := ecdsaPair(t, "local-key")
	keys := &observedSigningKeys{Keys: &accessapitest.Keys{Keypairs: map[string]api.Keypair{"local-key": pair}}}
	f.sign(t, keys, "local-key", "")
	f.remount(t, npmhttp.Options{RemoteSources: f.svc, KeyPairs: keys})
	for _, c := range []repo.Config{{Key: "inner", Class: repo.Virtual, PackageType: repo.NPM, Repositories: []string{"npm-local"}}, {Key: "npm-all", Class: repo.Virtual, PackageType: repo.NPM, Repositories: []string{"inner", "npm-remote"}}} {
		if err := f.db.Repos().Create(t.Context(), *repo.Defaults(&c)); err != nil {
			t.Fatal(err)
		}
	}
	f.setKeys(`{"keys":[{"keyid":"remote","key":"upstream","expires":null,"future":true}]}`, "application/json")
	return f, keys
}

const virtualNPMKeysPath = "/artifactory/api/npm/npm-all/-/npm/v1/keys"

func TestAccNPMVirtualKeysRootAdmissionPrecedesContributions(t *testing.T) {
	for _, gate := range []string{"read", "policy"} {
		t.Run(gate, func(t *testing.T) {
			f, keys := npmVirtualKeysFixture(t)
			user, want := "nobody", 403
			if gate == "policy" {
				user, want = "admin", 404
				c, err := f.db.Repos().Get(t.Context(), "npm-all")
				if err != nil {
					t.Fatal(err)
				}
				c.ExcludesPattern = new(".npm/keys.json")
				if err := f.db.Repos().Update(t.Context(), c); err != nil {
					t.Fatal(err)
				}
			}
			response := f.do("GET", virtualNPMKeysPath, user)
			wantStatus(t, response, want)
			if keys.calls != 0 || len(f.up.Requests()) != 0 {
				t.Fatal("root admission read a contributor", keys.calls, f.up.Requests())
			}
		})
	}
}

func TestAccNPMVirtualKeysUpstreamStatusKeepsDirectControl(t *testing.T) {
	for _, status := range []int{400, 401, 403, 404, 410, 429, 500, 503} {
		t.Run(strconv.Itoa(status), func(t *testing.T) {
			f, _ := npmVirtualKeysFixture(t)
			f.up.Set(upstreamKeys, fakeupstream.Resource{Status: status})
			direct := f.do("GET", remoteKeys, "reader")
			directStatus := status
			if status == 400 || status == 410 {
				directStatus = 502
			} // shared source safeError contract
			wantStatus(t, direct, directStatus)
			response := f.do("GET", virtualNPMKeysPath, "reader")
			want := directStatus
			if status == 404 {
				want = 200
			}
			wantStatus(t, response, want)
			if status != 404 && response.Header().Get("WWW-Authenticate") != direct.Header().Get("WWW-Authenticate") {
				t.Fatal("source failure challenged virtual caller", response.Header())
			}
		})
	}
}

func TestAccNPMVirtualKeysMemberVisibilityAndOfflineCache(t *testing.T) {
	for _, mode := range []string{"local-pattern", "nested-pattern", "remote-pattern", "remote-blackout", "remote-offline-miss", "remote-offline-warm", "unreadable-local"} {
		t.Run(mode, func(t *testing.T) {
			f, keys := npmVirtualKeysFixture(t)
			remoteOnly := true
			target := virtualNPMKeysPath
			user := "reader"
			switch mode {
			case "local-pattern", "nested-pattern":
				key := "npm-local"
				if mode == "nested-pattern" {
					key = "inner"
				}
				c, _ := f.db.Repos().Get(t.Context(), key)
				c.ExcludesPattern = new(".npm/keys.json")
				if err := f.db.Repos().Update(t.Context(), c); err != nil {
					t.Fatal(err)
				}
			case "unreadable-local":
				permission, err := f.db.Permissions().Get(t.Context(), "reader")
				if err != nil {
					t.Fatal(err)
				}
				resource := permission.Resources[api.ResourceArtifact]
				resource.Targets["npm-local"] = accesstest.Target{Include: []string{"pkg/**"}}
				permission.Resources[api.ResourceArtifact] = resource
				if err := f.db.Permissions().Update(t.Context(), permission); err != nil {
					t.Fatal(err)
				}
			default:
				remoteOnly = false
				c, _ := f.db.Repos().Get(t.Context(), "npm-remote")
				if mode == "remote-pattern" {
					c.ExcludesPattern = new(".npm/keys.json")
				}
				if mode == "remote-blackout" {
					c.BlackedOut = new(true)
				}
				if mode == "remote-offline-warm" {
					wantStatus(t, f.do("GET", target, user), 200)
					keys.calls = 0
				}
				if strings.HasPrefix(mode, "remote-offline") {
					c.Offline = new(true)
				}
				if err := f.db.Repos().Update(t.Context(), c); err != nil {
					t.Fatal(err)
				}
			}
			before := len(f.up.Requests())
			response := f.do("GET", target, user)
			wantStatus(t, response, 200)
			var doc struct {
				Keys []map[string]json.RawMessage `json:"keys"`
			}
			if err := json.Unmarshal(response.Body.Bytes(), &doc); err != nil {
				t.Fatal(err)
			}
			wantCount := 1
			if mode == "remote-offline-warm" {
				wantCount = 2
			}
			if len(doc.Keys) != wantCount {
				t.Fatal(mode, response.Body.String())
			}
			if remoteOnly && (string(doc.Keys[0]["keyid"]) != `"remote"` || keys.calls != 0) {
				t.Fatal("hidden local leaked keys", response.Body.String(), keys.calls)
			}
			if !remoteOnly && mode != "remote-offline-warm" && string(doc.Keys[0]["keyid"]) == `"remote"` {
				t.Fatal("refused remote contributed", response.Body.String())
			}
			if !remoteOnly && len(f.up.Requests()) != before {
				t.Fatal("offline/refused member reached upstream")
			}
		})
	}
}
