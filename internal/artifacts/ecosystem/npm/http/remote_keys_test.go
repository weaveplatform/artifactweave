package npmhttp_test

import (
	"crypto/ecdsa"
	"crypto/elliptic"
	"crypto/rand"
	"crypto/sha256"
	"crypto/x509"
	"encoding/base64"
	"encoding/json"
	"net/http"
	"testing"
	"time"

	"github.com/weaveplatform/artifactweave/internal/artifacts/adapters/upstream/fakeupstream"
	npmfmt "github.com/weaveplatform/artifactweave/internal/artifacts/ecosystem/npm"
)

// remoteKeys is the remote repository's keys document, and upstreamKeys
// the upstream's own, which the remote relays.
const (
	remoteKeys   = remoteRegistry + "-/npm/v1/keys"
	upstreamKeys = "/-/npm/v1/keys"
)

// keysBody is registry.npmjs.org's keys document as it answered on
// 2026-11-13, an expired key first, with a field npm does not read: the
// remote relays it byte for byte, whatever it holds.
const keysBody = `{"keys":[{"expires":"2025-01-29T00:00:00.000Z",` +
	`"keyid":"SHA256:jl3bwswu80PjjokCgh0o2w5c2U4LhQAE57gj9cz1kzA",` +
	`"keytype":"ecdsa-sha2-nistp256","scheme":"ecdsa-sha2-nistp256",` +
	`"key":"MFkwEwYHKoZIzj0CAQYIKoZIzj0DAQcDQgAE1Olb3zMAFFxXKHiIkQO5cJ3Yhl5i6UPp+IhuteBJbuHcA5UogKo0EWtlWwW6KSaKoTNEYL7JlCQiVnkhBktUgg=="}],` +
	`"future-field":true}`

// setKeys serves body as the upstream's keys document, labelled typ.
func (f *remoteFixture) setKeys(body, typ string) {
	f.up.Set(upstreamKeys, fakeupstream.Resource{
		Body: []byte(body), Header: http.Header{"Content-Type": []string{typ}},
	})
}

// A remote repository answers its upstream's keys document byte for byte,
// as application/json whatever the upstream labels it, with the upstream's
// validators, for HEAD too; it caches the document at .npm/keys.json for
// the retrieval cache period, sends the remote's own credentials upstream,
// and answers from the cache while offline.
func TestAccRemoteKeysRelayTheUpstreamDocument(t *testing.T) {
	f := newRemoteFixture(t)
	f.setKeys(keysBody, "text/plain; charset=utf-8")
	c, err := f.db.Repos().Get(t.Context(), "npm-remote")
	if err != nil {
		t.Fatal(err)
	}
	c.Username, c.Password = "upstream-user", "upstream-secret"
	if err := f.db.Repos().Update(t.Context(), c); err != nil {
		t.Fatal(err)
	}

	rec := f.do(http.MethodGet, remoteKeys, "reader")
	wantAnswer(t, rec, http.StatusOK, keysBody)
	if got := rec.Header().Get("Content-Type"); got != "application/json" {
		t.Errorf("Content-Type %q", got)
	}
	etag := rec.Header().Get("ETag")
	if etag == "" {
		t.Fatal("no ETag")
	}
	wantStatus(t, f.do(http.MethodGet, remoteKeys, "reader", "If-None-Match", etag),
		http.StatusNotModified)
	head := f.do(http.MethodHead, remoteKeys, "reader")
	wantStatus(t, head, http.StatusOK)
	if head.Body.Len() != 0 || head.Header().Get("Content-Length") != "" &&
		head.Header().Get("Content-Length") != rec.Header().Get("Content-Length") {
		t.Errorf("HEAD %q %q", head.Header().Get("Content-Length"), head.Body)
	}
	// One fetch, the remote's HEAD and GET, served every read since.
	requests := f.up.Requests()
	if f.up.Count(http.MethodGet, upstreamKeys) != 1 || len(requests) != 2 {
		t.Fatalf("upstream asked %d times: %+v", len(requests), requests)
	}
	basic := "Basic " + base64.StdEncoding.EncodeToString([]byte("upstream-user:upstream-secret"))
	for _, request := range requests {
		if request.Path != upstreamKeys || !request.HeaderMatches("Authorization", basic) {
			t.Errorf("%s %s without the remote's credentials", request.Method, request.Path)
		}
	}

	// A rotated upstream key is answered once the cache period (five
	// seconds in the fixture) has passed, and not before.
	rotated := `{"keys":[]}`
	f.setKeys(rotated, "application/json")
	wantAnswer(t, f.do(http.MethodGet, remoteKeys, "reader"), http.StatusOK, keysBody)
	f.clock.Advance(6 * time.Second)
	wantAnswer(t, f.do(http.MethodGet, remoteKeys, "reader"), http.StatusOK, rotated)

	f.up.Close()
	off := true
	c.Offline = &off
	if err := f.db.Repos().Update(t.Context(), c); err != nil {
		t.Fatal(err)
	}
	f.clock.Advance(6 * time.Second)
	wantAnswer(t, f.do(http.MethodGet, remoteKeys, "reader"), http.StatusOK, rotated)
}

// The keys the remote relays verify the dist.signatures it relays: npm
// checks each version's signature over "<name>@<version>:<integrity>"
// against the key the keys document lists under its keyid.
func TestAccRemoteKeysVerifyRelayedSignatures(t *testing.T) {
	f := newRemoteFixture(t)
	k, err := ecdsa.GenerateKey(elliptic.P256(), rand.Reader)
	if err != nil {
		t.Fatal(err)
	}
	pub, err := npmfmt.PublicKey(&k.PublicKey)
	if err != nil {
		t.Fatal(err)
	}
	keys, err := json.Marshal(map[string]any{"keys": []npmfmt.Key{pub}})
	if err != nil {
		t.Fatal(err)
	}
	f.setKeys(string(keys), "application/json")
	doc := f.packageDoc(t, "@acme/signed", "1.0.0")
	dist := doc["versions"].(map[string]any)["1.0.0"].(map[string]any)["dist"].(map[string]any)
	digest := sha256.Sum256([]byte("@acme/signed@1.0.0:" + dist["integrity"].(string)))
	sig, err := ecdsa.SignASN1(rand.Reader, k, digest[:])
	if err != nil {
		t.Fatal(err)
	}
	dist["signatures"] = []any{map[string]string{
		"keyid": pub.KeyID, "sig": base64.StdEncoding.EncodeToString(sig),
	}}
	f.setJSON("/@acme/signed", doc)

	rec := f.do(http.MethodGet, remoteRegistry+"@acme%2fsigned", "reader")
	wantStatus(t, rec, http.StatusOK)
	var packument struct {
		Versions map[string]struct {
			Dist struct {
				Integrity  string `json:"integrity"`
				Signatures []struct {
					KeyID string `json:"keyid"`
					Sig   string `json:"sig"`
				} `json:"signatures"`
			} `json:"dist"`
		} `json:"versions"`
	}
	if err := json.Unmarshal(rec.Body.Bytes(), &packument); err != nil {
		t.Fatal(err)
	}
	rec = f.do(http.MethodGet, remoteKeys, "reader")
	wantStatus(t, rec, http.StatusOK)
	var listed struct{ Keys []npmfmt.Key }
	if err := json.Unmarshal(rec.Body.Bytes(), &listed); err != nil {
		t.Fatal(err)
	}
	relayed := packument.Versions["1.0.0"].Dist
	if len(relayed.Signatures) != 1 || len(listed.Keys) != 1 ||
		listed.Keys[0].KeyID != relayed.Signatures[0].KeyID {
		t.Fatalf("signatures %+v, keys %s", relayed.Signatures, rec.Body)
	}
	der, err := base64.StdEncoding.DecodeString(listed.Keys[0].Key)
	if err != nil {
		t.Fatal(err)
	}
	key, err := x509.ParsePKIXPublicKey(der)
	if err != nil {
		t.Fatal(err)
	}
	got, err := base64.StdEncoding.DecodeString(relayed.Signatures[0].Sig)
	if err != nil {
		t.Fatal(err)
	}
	digest = sha256.Sum256([]byte("@acme/signed@1.0.0:" + relayed.Integrity))
	ecKey, ok := key.(*ecdsa.PublicKey)
	if !ok || !ecdsa.VerifyASN1(ecKey, digest[:], got) {
		t.Fatal("the relayed signature does not verify with the relayed key")
	}
}

// An upstream without a keys document, or an offline remote with none
// cached, answers the 404 the path answered before, which npm reads as a
// registry without keys; the upstream's 404 is remembered for the missed
// retrieval cache period. A document npm could not read answers 502 and is
// not cached, and a failing upstream answers its source error.
func TestAccRemoteKeysUpstreamMissingAndFailing(t *testing.T) {
	f := newRemoteFixture(t)
	missing := `{"error":"not found: GET ` + remoteKeys + `"}`
	wantAnswer(t, f.do(http.MethodGet, remoteKeys, "reader"), http.StatusNotFound, missing)
	wantAnswer(t, f.do(http.MethodGet, remoteKeys, "reader"), http.StatusNotFound, missing)
	if got := f.up.Count("", upstreamKeys); got != 1 {
		t.Fatalf("upstream asked %d times", got)
	}

	for _, invalid := range []string{
		`[]`, `{}`, `{"keys":null}`, `{"keys":{}}`, `{"keys":[null]}`, `{"keys":["key"]}`,
		`{"keys":[]} trailing`,
	} {
		f.clock.Advance(time.Hour)
		f.setKeys(invalid, "application/json")
		wantAnswer(t, f.do(http.MethodGet, remoteKeys, "reader"), http.StatusBadGateway,
			`{"error":"Bad Gateway"}`)
	}
	f.setKeys(keysBody, "application/json")
	wantAnswer(t, f.do(http.MethodGet, remoteKeys, "reader"), http.StatusOK, keysBody)

	f.clock.Advance(time.Hour)
	f.up.Set(upstreamKeys, fakeupstream.Resource{Status: http.StatusServiceUnavailable})
	// The cached document is served while the upstream fails (decision
	// 0007's stale fallback), and a remote with nothing cached answers the
	// failure.
	wantAnswer(t, f.do(http.MethodGet, remoteKeys, "reader"), http.StatusOK, keysBody)

	cold := newRemoteFixture(t)
	cold.up.Set(upstreamKeys, fakeupstream.Resource{Status: http.StatusServiceUnavailable})
	wantAnswer(t, cold.do(http.MethodGet, remoteKeys, "reader"), http.StatusServiceUnavailable,
		`{"error":"Service Unavailable"}`)
	cold.up.Close()
	cold.clock.Advance(time.Hour)
	wantAnswer(t, cold.do(http.MethodGet, remoteKeys, "reader"), http.StatusBadGateway,
		`{"error":"Bad Gateway"}`)
	c, err := cold.db.Repos().Get(t.Context(), "npm-remote")
	if err != nil {
		t.Fatal(err)
	}
	off := true
	c.Offline = &off
	if err := cold.db.Repos().Update(t.Context(), c); err != nil {
		t.Fatal(err)
	}
	wantAnswer(t, cold.do(http.MethodGet, remoteKeys, "reader"), http.StatusNotFound, missing)
}

// The keys document needs read permission at .npm/keys.json, then passes
// the remote's download policy there, as a local repository's does, and
// the upstream is asked only once both allow it.
func TestAccRemoteKeysPermissionAndPolicy(t *testing.T) {
	f := newRemoteFixture(t)
	f.setKeys(keysBody, "application/json")
	f.setPolicy(t, "npm-remote", true, "", "")
	wantAnswer(t, f.do(http.MethodGet, remoteKeys, "nobody"), http.StatusForbidden,
		`{"error":"Forbidden"}`)
	wantStatus(t, f.do(http.MethodGet, remoteKeys, "anonymous"), http.StatusUnauthorized)
	// A grant on a package's tarballs alone does not reach the document.
	wantAnswer(t, f.do(http.MethodGet, remoteKeys, "partial"), http.StatusForbidden,
		`{"error":"Forbidden"}`)
	for _, user := range []string{"reader", "admin"} {
		wantAnswer(t, f.do(http.MethodGet, remoteKeys, user), http.StatusNotFound,
			blackedOut("npm-remote", ".npm/keys.json"))
	}
	for _, c := range []struct{ include, exclude string }{
		{"", ".npm/**"},
		{"rich/**", ""},
	} {
		f.setPolicy(t, "npm-remote", false, c.include, c.exclude)
		wantAnswer(t, f.do(http.MethodGet, remoteKeys, "reader"), http.StatusNotFound,
			rejected("npm-remote", ".npm/keys.json"))
	}
	if n := len(f.up.Requests()); n != 0 {
		t.Fatalf("a refused read asked the upstream %d times", n)
	}
	f.setPolicy(t, "npm-remote", false, "rich/**,.npm/keys.json", "")
	wantAnswer(t, f.do(http.MethodGet, remoteKeys, "reader"), http.StatusOK, keysBody)
}
