package huggingface_test

import (
	"bytes"
	"crypto/sha256"
	"encoding/base64"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"net/http/httptest"
	"net/url"
	"strings"
	"testing"

	"github.com/weaveplatform/artifactweave/internal/access/accesstest"
	"github.com/weaveplatform/artifactweave/internal/access/api"
	"github.com/weaveplatform/artifactweave/internal/artifacts/api/repo"
	"github.com/weaveplatform/artifactweave/internal/artifacts/ecosystem/huggingface/xet"
	"github.com/weaveplatform/artifactweave/internal/kernel"
	"github.com/weaveplatform/artifactweave/internal/platform/testkit/repopath"
	"github.com/weaveplatform/artifactweave/test/server/support"
)

const (
	xetFixtureHash = "4f7ccc9872630143f6a4a26904b5255fef96e6c75cf78f0449114d77a94fd197"
	xetFixtureFile = "869fb7b9851f0c53b04e946381db72f24d74462c24cbe4737856e41992386def"
)

type xetTestToken struct {
	CAS   string `json:"casUrl"`
	Token string `json:"accessToken"`
}

func xetFixture(t *testing.T, name string) []byte {
	t.Helper()
	return repopath.Read(t, "internal/artifacts/ecosystem/huggingface/xet/testdata/"+name)
}

func newXetWorld(t *testing.T) *itemWorld {
	t.Helper()
	w := newItemWorld(
		t,
		false,
		func(c *Config) { c.HuggingFaceXetEnabled = true; c.Keys = support.TerraformBackendKeys(t) },
	)
	for _, c := range []repo.Config{{Key: "hf", Class: repo.Local, PackageType: repo.HuggingFaceML}, {Key: "hf-f", Class: repo.Federated, PackageType: repo.HuggingFaceML}, {Key: "hf-v", Class: repo.Virtual, PackageType: repo.HuggingFaceML, Repositories: []string{"hf"}, DefaultDeploymentRepo: "hf"}} {
		if err := w.db.Repos().Create(t.Context(), *repo.Defaults(&c)); err != nil {
			t.Fatal(err)
		}
	}
	return w
}

func xetTokenFor(t *testing.T, w *itemWorld, key, identity, mode, as string) xetTestToken {
	t.Helper()
	r := w.do(
		"GET",
		"/artifactory/api/huggingfaceml/"+key+"/api/models/"+identity+"/xet-"+mode+"-token/main",
		as,
		"",
	)
	wantStatus(t, r, 200)
	var token xetTestToken
	if e := json.Unmarshal(r.Body.Bytes(), &token); e != nil {
		t.Fatal(e)
	}
	return token
}

func xetCall(
	w *itemWorld,
	token xetTestToken,
	method, path string,
	body []byte,
) *httptest.ResponseRecorder {
	return w.do(method, token.CAS+path, "", string(body), "Authorization", "Bearer "+token.Token)
}

func publishXetFixture(t *testing.T, w *itemWorld, key string) (xetTestToken, string) {
	t.Helper()
	write := xetTokenFor(t, w, key, "acme/demo", "write", "admin")
	wantStatus(
		t,
		xetCall(w, write, "POST", "/v1/xorbs/default/"+xetFixtureHash, xetFixture(t, "large.xorb")),
		200,
	)
	wantStatus(t, xetCall(w, write, "POST", "/v2/shards", xetFixture(t, "large.shard")), 200)
	data := make([]byte, 1280000)
	for i := range data {
		data[i] = byte(i)
	}
	sum := sha256.Sum256(data)
	body := fmt.Sprintf(
		"{\"key\":\"header\",\"value\":{\"summary\":\"native Xet\"}}\n{\"key\":\"lfsFile\",\"value\":{\"path\":\"weights.bin\",\"algo\":\"sha256\",\"oid\":\"%x\",\"size\":%d}}\n",
		sum,
		len(data),
	)
	commit := w.do(
		"POST",
		"/artifactory/api/huggingfaceml/"+key+"/api/models/acme/demo/commit/main",
		"admin",
		body,
		"Content-Type",
		"application/x-ndjson",
	)
	wantStatus(t, commit, 200)
	var result struct {
		OID string `json:"commitOid"`
	}
	if e := json.Unmarshal(commit.Body.Bytes(), &result); e != nil {
		t.Fatal(e)
	}
	return write, result.OID
}

func TestUnitXetPublishedReconstructionAndCapabilities(t *testing.T) {
	for _, key := range []string{"hf", "hf-f", "hf-v"} {
		t.Run(key, func(t *testing.T) {
			w := newXetWorld(t)
			write, revision := publishXetFixture(t, w, key)
			// Native clients retry CAS objects and shard publication, including
			// choosing a different compression for the same immutable xorb.
			xorb, err := xet.ParseXorb(xetFixture(t, "large.xorb"), xetFixtureHash)
			if err != nil {
				t.Fatal(err)
			}
			chunks := make([][]byte, len(xorb.Chunks))
			for i, chunk := range xorb.Chunks {
				chunks[i] = chunk.Data
			}
			reencoded, err := xet.EncodeChunks(chunks)
			if err != nil {
				t.Fatal(err)
			}
			wantStatus(
				t,
				xetCall(
					w,
					write,
					"POST",
					"/v1/xorbs/default/"+xetFixtureHash,
					reencoded,
				),
				200,
			)
			wantStatus(
				t,
				xetCall(w, write, "POST", "/v2/shards", xetFixture(t, "large.shard")),
				200,
			)
			endpoint := "/artifactory/api/huggingfaceml/" + key
			head := w.do("HEAD", endpoint+"/acme/demo/resolve/main/weights.bin", "reader", "")
			wantStatus(t, head, 200)
			if head.Header().Get("X-Xet-Hash") != xetFixtureFile ||
				!strings.Contains(head.Header().Get("X-Xet-Refresh-Route"), "/"+key+"/") {
				t.Fatalf("missing scoped Xet metadata: %v", head.Header())
			}
			read := xetTokenFor(t, w, key, "acme/demo", "read", "reader")
			for _, v := range []string{"v1", "v2"} {
				wantStatus(
					t,
					xetCall(w, read, "GET", "/"+v+"/reconstructions/"+xetFixtureFile, nil),
					200,
				)
			}
			result := xetCall(w, read, "GET", "/v1/reconstructions/"+xetFixtureFile, nil)
			var rec struct {
				Terms []struct {
					Hash  string                   `json:"hash"`
					Range struct{ Start, End int } `json:"range"`
				} `json:"terms"`
				Fetch map[string][]struct {
					URL string `json:"url"`
				} `json:"fetch_info"`
			}
			if e := json.Unmarshal(result.Body.Bytes(), &rec); e != nil {
				t.Fatal(e)
			}
			var got []byte
			for _, term := range rec.Terms {
				address := rec.Fetch[term.Hash][0].URL
				response := w.do("GET", address, "", "")
				wantStatus(t, response, 200)
				parsed, e := xet.ParseXorb(response.Body.Bytes(), term.Hash)
				if e != nil {
					t.Fatal(e)
				}
				for _, chunk := range parsed.Chunks[term.Range.Start:term.Range.End] {
					got = append(got, chunk.Data...)
				}
			}
			if len(got) != 1280000 {
				t.Fatalf("reconstruction size %d", len(got))
			}
			for i, b := range got {
				if b != byte(i) {
					t.Fatalf("bad reconstruction at %d", i)
				}
			}
			wantStatus(t, xetCall(w, read, "POST", "/v1/shards", xetFixture(t, "large.shard")), 403)
			wantStatus(t, xetCall(w, write, "GET", "/v1/reconstructions/"+xetFixtureFile, nil), 403)
			forged := read
			forged.Token += "x"
			wantStatus(
				t,
				xetCall(w, forged, "GET", "/v1/reconstructions/"+xetFixtureFile, nil),
				403,
			)
			other := read
			other.CAS = strings.Replace(other.CAS, "/acme/demo/", "/acme/secret/", 1)
			wantStatus(t, xetCall(w, other, "GET", "/v1/reconstructions/"+xetFixtureFile, nil), 403)
			wantStatus(
				t,
				xetCall(w, read, "GET", "/v1/reconstructions/"+strings.Repeat("a", 64), nil),
				404,
			)
			member := "hf"
			if key == "hf-f" {
				member = key
			}
			wantStatus(
				t,
				w.do(
					"DELETE",
					"/artifactory/"+member+"/models--acme--demo/snapshots/"+revision,
					"admin",
					"",
				),
				204,
			)
			wantStatus(t, xetCall(w, read, "GET", "/v1/reconstructions/"+xetFixtureFile, nil), 404)
			wantStatus(
				t,
				w.do(
					"POST",
					"/artifactory/api/trash/restore/"+member+"/models--acme--demo/snapshots/"+revision+"?to="+member,
					"admin",
					"",
				),
				202,
			)
			wantStatus(t, xetCall(w, read, "GET", "/v1/reconstructions/"+xetFixtureFile, nil), 200)
			user, e := w.db.Users().Get(t.Context(), "reader")
			if e != nil {
				t.Fatal(e)
			}
			user.PasswordHash = []byte("revoked")
			if e = w.db.Users().Update(t.Context(), user); e != nil {
				t.Fatal(e)
			}
			wantStatus(t, xetCall(w, read, "GET", "/v1/reconstructions/"+xetFixtureFile, nil), 403)
		})
	}
}

func TestUnitXetRejectsCorruptContentAndGenericBypasses(t *testing.T) {
	w := newXetWorld(t)
	write, _ := publishXetFixture(t, w, "hf")
	corrupt := bytes.Clone(xetFixture(t, "large.xorb"))
	corrupt[20] ^= 1
	wantStatus(t, xetCall(w, write, "POST", "/v1/xorbs/default/"+xetFixtureHash, corrupt), 422)
	wantStatus(t, xetCall(w, write, "POST", "/v1/shards", []byte("invalid")), 422)
	wantStatus(t, xetCall(w, write, "HEAD", "/v1/xorbs/default/"+xetFixtureHash, nil), 200)
	wantStatus(t, xetCall(w, write, "HEAD", "/v1/xorbs/default/"+strings.Repeat("a", 64), nil), 404)
	wantStatus(t, xetCall(w, write, "GET", "/v1/chunks/default/"+xetFixtureHash, nil), 404)
	for _, suffix := range []string{"", ".sha256", "!/entry"} {
		wantStatus(
			t,
			w.do(
				"GET",
				"/artifactory/hf/models--acme--demo/.xet/xorbs/"+xetFixtureHash+suffix,
				"admin",
				"",
			),
			404,
		)
	}
	wantStatus(
		t,
		w.do(
			"PUT",
			"/artifactory/hf/models--acme--demo/.xet/xorbs/"+xetFixtureHash,
			"admin",
			string(xetFixture(t, "large.xorb")),
		),
		403,
	)
	for _, operation := range []string{"copy", "move"} {
		response := w.do(
			"POST",
			"/artifactory/api/"+operation+"/hf/models--acme--demo/.xet/xorbs/"+xetFixtureHash+"?to=other-local/leak",
			"admin",
			"",
		)
		if response.Code == 200 &&
			!strings.Contains(response.Body.String(), "native model endpoint") {
			t.Fatalf("%s exposed CAS: %s", operation, response.Body)
		}
	}
	if e := w.db.Permissions().
		Create(t.Context(), accesstest.Permission{Name: "xet-private-only", Resources: map[api.ResourceType]accesstest.Resource{api.ResourceArtifact: {Actions: accesstest.Grants{Users: map[string]api.Actions{"user": api.ActionRead | api.ActionWrite | api.ActionManage | api.ActionDelete}}, Targets: map[string]accesstest.Target{"hf": {Include: []string{"models--acme--demo/.xet/**"}}, "other-local": {Include: []string{"**"}}}}}}); e != nil {
		t.Fatal(e)
	}
	sum := sha256.Sum256(xetFixture(t, "large.xorb"))
	response := w.do(
		"PUT",
		"/artifactory/other-local/escaped",
		"user",
		"",
		"X-Checksum-Deploy",
		"true",
		"X-Checksum-Sha256",
		hex.EncodeToString(sum[:]),
	)
	if response.Code == 201 {
		t.Fatal("checksum deployment exposed private CAS")
	}
	from := "models--acme--demo/.xet/xorbs/" + xetFixtureHash
	wantStatus(t, w.do("DELETE", "/artifactory/hf/"+from, "user", ""), 204)
	wantStatus(
		t,
		w.do(
			"POST",
			"/artifactory/api/trash/restore/hf/"+from+"?to="+url.QueryEscape("other-local/escaped"),
			"user",
			"",
		),
		400,
	)
	wantStatus(
		t,
		w.do("POST", "/artifactory/api/trash/restore/hf/"+from+"?to=hf/"+from, "user", ""),
		202,
	)
}

func TestUnitXetShardAuthenticatesEveryDeclarationBeforePublication(t *testing.T) {
	w := newXetWorld(t)
	write := xetTokenFor(t, w, "hf", "acme/demo", "write", "admin")
	shard := xetFixture(t, "large.shard")
	// Missing xorbs cannot be borrowed from another model or the global blob store.
	wantStatus(t, xetCall(w, write, "POST", "/v2/shards", shard), 422)
	wantStatus(
		t,
		xetCall(w, write, "POST", "/v1/xorbs/default/"+xetFixtureHash, xetFixture(t, "large.xorb")),
		200,
	)
	for _, tc := range []struct {
		name string
		at   int
	}{
		{"file root", 48},
		{"segment xorb", 96},
		{"segment unpacked size", 132},
		{"segment boundary", 140},
		{"segment verification", 528},
		{"file SHA256", 960},
		{"declared xorb", 1056},
		{"declared chunk", 1104},
	} {
		t.Run(tc.name, func(t *testing.T) {
			corrupt := bytes.Clone(shard)
			corrupt[tc.at] ^= 1
			wantStatus(t, xetCall(w, write, "POST", "/v2/shards", corrupt), 422)
			_, err := w.db.Items().
				Get(t.Context(), "hf", false, "models--acme--demo/.xet/files/"+xetFixtureFile)
			if !errors.Is(err, kernel.ErrNotFound) {
				t.Fatalf("invalid shard published a file mapping: %v", err)
			}
		})
	}
	wantStatus(t, xetCall(w, write, "POST", "/v2/shards", shard), 200)
}

func TestUnitXetOversizedCapabilityIsRefusedAtIssuance(t *testing.T) {
	w := newXetWorld(t)
	publishXetFixture(t, w, "hf")
	// Legacy user creation does not impose Access v2's 255-byte name limit.
	name := strings.Repeat("u", 16<<10)
	if err := w.db.Users().
		Create(t.Context(), accesstest.User{Name: name, Admin: true, PasswordHash: cheapHash}); err != nil {
		t.Fatal(err)
	}
	endpoint := "/artifactory/api/huggingfaceml/hf"
	for _, mode := range []string{"read", "write"} {
		got := w.do("GET", endpoint+"/api/models/acme/demo/xet-"+mode+"-token/main", name, "")
		wantStatus(t, got, 400)
		if got.Header().Get("X-Xet-Access-Token") != "" ||
			!strings.Contains(got.Body.String(), "capability metadata") {
			t.Fatal("oversize issuance did not return an explicit refusal")
		}
	}
	got := w.do(
		"POST",
		endpoint+"/acme/demo.git/info/lfs/objects/batch",
		name,
		fmt.Sprintf(
			`{"operation":"upload","objects":[{"oid":%q,"size":1280000}]}`,
			strings.Repeat("a", 64),
		),
		"Content-Type",
		"application/vnd.git-lfs+json",
	)
	wantStatus(t, got, 400)
	if !strings.Contains(got.Body.String(), "capability metadata") {
		t.Fatal("oversize LFS grant did not return an explicit refusal")
	}
}

// patchHubRepo changes the configuration of the repository key, as a
// repository update does.
func patchHubRepo(t *testing.T, w *itemWorld, key string, change func(*repo.Config)) {
	t.Helper()
	current, err := w.db.Repos().Get(t.Context(), key)
	if err != nil {
		t.Fatal(err)
	}
	change(&current)
	if err := w.db.Repos().Update(t.Context(), current); err != nil {
		t.Fatal(err)
	}
}

// wantRefusal fails t unless rec answers 404 with message, and without the
// headers resolve sets for a served file or the token route for an issued
// token, which would hand the client the refused file's commit, digest,
// Xet hash and token route, or a token.
func wantRefusal(t *testing.T, rec *httptest.ResponseRecorder, message string) {
	t.Helper()
	wantStatus(t, rec, 404)
	if !strings.Contains(rec.Body.String(), message) {
		t.Fatalf("body %s, want %q", rec.Body, message)
	}
	for _, header := range []string{
		"X-Repo-Commit", "ETag", "X-Xet-Hash", "X-Xet-Refresh-Route",
		"X-Xet-Cas-Url", "X-Xet-Access-Token", "X-Xet-Token-Expiration",
	} {
		if got := rec.Header().Get(header); got != "" {
			t.Fatalf("refusal carries %s: %q", header, got)
		}
	}
}

// xetChunkURL returns the first chunk range that the v1 reconstruction of
// the fixture's file names; the URL carries its own signed token.
func xetChunkURL(t *testing.T, w *itemWorld, read xetTestToken) string {
	t.Helper()
	rec := xetCall(w, read, "GET", "/v1/reconstructions/"+xetFixtureFile, nil)
	wantStatus(t, rec, 200)
	var answer struct {
		Fetch map[string][]struct {
			URL string `json:"url"`
		} `json:"fetch_info"`
	}
	if err := json.Unmarshal(rec.Body.Bytes(), &answer); err != nil {
		t.Fatal(err)
	}
	for _, fetches := range answer.Fetch {
		if len(fetches) > 0 {
			return fetches[0].URL
		}
	}
	t.Fatalf("reconstruction names no chunk range: %s", rec.Body)
	return ""
}

// commitHubFile commits a regular file to acme/demo's main in the
// repository key and returns the new revision.
func commitHubFile(t *testing.T, w *itemWorld, key, path, content string) string {
	t.Helper()
	body := `{"key":"header","value":{"summary":"publish"}}` + "\n" + fmt.Sprintf(
		`{"key":"file","value":{"path":%q,"content":%q,"encoding":"base64"}}`,
		path, base64.StdEncoding.EncodeToString([]byte(content)),
	) + "\n"
	rec := w.do("POST", "/artifactory/api/huggingfaceml/"+key+"/api/models/acme/demo/commit/main",
		"admin", body, "Content-Type", "application/x-ndjson")
	wantStatus(t, rec, 200)
	var result struct {
		OID string `json:"commitOid"`
	}
	if err := json.Unmarshal(rec.Body.Bytes(), &result); err != nil {
		t.Fatal(err)
	}
	return result.OID
}

// TestAccHuggingFaceDownloadPolicy checks Artifactory's download policy on
// the Hub and Xet routes of a local and a federated repository (#1237).
// Patterns that reject a file refuse its resolve, for an administrator too,
// and its Xet reconstruction and chunk ranges, through a token issued
// before the change, and leave the file listed and a read token issued. A
// blacked-out repository refuses the same reads, and its info, tree and
// Xet read token, whose 7.x storage is not documented, with the refusal
// naming the model's folder. No refusal carries the headers of a served
// file or an issued token, and a caller who may not read the model learns
// nothing of how the repository is set.
func TestAccHuggingFaceDownloadPolicy(t *testing.T) {
	for _, key := range []string{"hf", "hf-f"} {
		t.Run(key, func(t *testing.T) {
			w := newXetWorld(t)
			_, revision := publishXetFixture(t, w, key)
			endpoint := "/artifactory/api/huggingfaceml/" + key
			resolve := endpoint + "/acme/demo/resolve/main/weights.bin"
			listings := []string{
				endpoint + "/api/models/acme/demo",
				endpoint + "/api/models/acme/demo/tree/main",
			}
			token := endpoint + "/api/models/acme/demo/xet-read-token/main"
			read := xetTokenFor(t, w, key, "acme/demo", "read", "reader")
			chunks := xetChunkURL(t, w, read)
			file := "models--acme--demo/snapshots/" + revision + "/weights.bin"
			xetReads := func() []*httptest.ResponseRecorder {
				return []*httptest.ResponseRecorder{
					xetCall(w, read, "GET", "/v1/reconstructions/"+xetFixtureFile, nil),
					xetCall(w, read, "GET", "/v2/reconstructions/"+xetFixtureFile, nil),
					w.do("GET", chunks, "", ""),
				}
			}

			patchHubRepo(
				t,
				w,
				key,
				func(c *repo.Config) { c.ExcludesPattern = new("**/weights.bin") },
			)
			rejected := "The repository '" + key + "' rejected the artifact '" + key + ":" + file +
				"' due to its include/exclude pattern settings."
			for _, as := range []string{"admin", "reader"} {
				wantRefusal(t, w.do("HEAD", resolve, as, ""), "")
				wantRefusal(t, w.do("GET", resolve, as, ""), rejected)
			}
			for _, route := range listings {
				rec := w.do("GET", route, "reader", "")
				wantStatus(t, rec, 200)
				if !strings.Contains(rec.Body.String(), `"weights.bin"`) {
					t.Fatalf("GET %s: %s, want weights.bin listed", route, rec.Body)
				}
			}
			for _, rec := range xetReads() {
				wantRefusal(t, rec, rejected)
			}
			xetTokenFor(t, w, key, "acme/demo", "read", "reader")

			patchHubRepo(t, w, key, func(c *repo.Config) {
				c.BlackedOut, c.ExcludesPattern = new(true), new("")
			})
			blackedOut := func(path string) string {
				return "The repository '" + key + "' is blacked out and cannot serve artifact '" +
					key + ":" + path + "'."
			}
			for _, as := range []string{"admin", "reader"} {
				wantRefusal(t, w.do("GET", resolve, as, ""), blackedOut(file))
				for _, route := range append([]string{token}, listings...) {
					wantRefusal(t, w.do("GET", route, as, ""), blackedOut("models--acme--demo"))
				}
			}
			for _, rec := range xetReads() {
				wantRefusal(t, rec, blackedOut(file))
			}
			// Read permission first (DV-057): the model is missing to user.
			for _, route := range append([]string{resolve, token}, listings...) {
				wantRefusal(
					t,
					w.do("GET", route, "user", ""),
					"Hugging Face revision or file not found",
				)
			}

			patchHubRepo(t, w, key, func(c *repo.Config) { c.BlackedOut = new(false) })
			wantStatus(t, w.do("GET", resolve, "reader", ""), 200)
			for _, rec := range xetReads() {
				wantStatus(t, rec, 200)
			}
			xetTokenFor(t, w, key, "acme/demo", "read", "reader")
		})
	}
}

// TestAccHuggingFaceVirtualDownloadPolicy checks how a virtual repository
// chooses the member that serves a revision under the download policy
// (#1237). It passes over a blacked-out member, as 7.x ignores one when it
// resolves, and a later member serves the revision, its info and the
// revision a Xet read token names; with none left it answers its own "not
// found", naming no member. A member whose patterns reject the file is
// still the one chosen, and refuses it as the virtual repository's "not
// found". The virtual repository's own patterns refuse the file through
// the member request and leave the listing as it is. No refusal carries
// the headers of a served file.
func TestAccHuggingFaceVirtualDownloadPolicy(t *testing.T) {
	w := newXetWorld(t)
	member := repo.Config{Key: "hf2", Class: repo.Local, PackageType: repo.HuggingFaceML}
	if err := w.db.Repos().Create(t.Context(), *repo.Defaults(&member)); err != nil {
		t.Fatal(err)
	}
	patchHubRepo(t, w, "hf-v", func(c *repo.Config) { c.Repositories = []string{"hf", "hf2"} })
	revisions := map[string]string{}
	for _, key := range []string{"hf", "hf2"} {
		revisions[key] = commitHubFile(t, w, key, "member.txt", key)
	}
	endpoint := "/artifactory/api/huggingfaceml/hf-v"
	resolve := endpoint + "/acme/demo/resolve/main/member.txt"
	info := endpoint + "/api/models/acme/demo"
	served := func(from string) {
		t.Helper()
		rec := w.do("GET", resolve, "reader", "")
		wantStatus(t, rec, 200)
		if rec.Body.String() != from || rec.Header().Get("X-Repo-Commit") != revisions[from] {
			t.Fatalf("GET %s: body %q, commit %q, want %s's file",
				resolve, rec.Body, rec.Header().Get("X-Repo-Commit"), from)
		}
		listed := w.do("GET", info, "reader", "")
		wantStatus(t, listed, 200)
		if !strings.Contains(listed.Body.String(), `"sha":"`+revisions[from]+`"`) {
			t.Fatalf("GET %s: %s, want %s's revision", info, listed.Body, from)
		}
		// The token's CAS route names the revision it authorises.
		token := xetTokenFor(t, w, "hf-v", "acme/demo", "read", "reader")
		if !strings.HasSuffix(token.CAS, "/hf-v/xet/models/acme/demo/"+revisions[from]) {
			t.Fatalf("read token CAS %s, want %s's revision", token.CAS, from)
		}
	}

	served("hf")
	patchHubRepo(t, w, "hf", func(c *repo.Config) { c.BlackedOut = new(true) })
	served("hf2")
	patchHubRepo(t, w, "hf2", func(c *repo.Config) { c.BlackedOut = new(true) })
	for _, route := range []string{resolve, info, info + "/tree/main", info + "/xet-read-token/main"} {
		wantRefusal(t, w.do("GET", route, "admin", ""), "Hugging Face revision or file not found")
	}
	for _, key := range []string{"hf", "hf2"} {
		patchHubRepo(t, w, key, func(c *repo.Config) { c.BlackedOut = new(false) })
	}
	served("hf")

	patchHubRepo(t, w, "hf", func(c *repo.Config) { c.ExcludesPattern = new("**/member.txt") })
	wantRefusal(t, w.do("GET", resolve, "reader", ""), "Could not find resource")
	patchHubRepo(t, w, "hf", func(c *repo.Config) { c.ExcludesPattern = new("") })

	patchHubRepo(t, w, "hf-v", func(c *repo.Config) { c.ExcludesPattern = new("**/member.txt") })
	wantRefusal(t, w.do("HEAD", resolve, "reader", ""), "")
	wantRefusal(t, w.do("GET", resolve, "reader", ""),
		"No repository found to serve the request for hf-v:models--acme--demo/snapshots/"+
			revisions["hf"]+"/member.txt")
	listed := w.do("GET", info, "reader", "")
	wantStatus(t, listed, 200)
	if !strings.Contains(listed.Body.String(), `"member.txt"`) {
		t.Fatalf("GET %s: %s, want member.txt listed", info, listed.Body)
	}
}
