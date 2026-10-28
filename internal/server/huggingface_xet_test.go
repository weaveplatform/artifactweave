package server

import (
	"bytes"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"net/http/httptest"
	"net/url"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/weaveplatform/artifactweave/internal/format/huggingface/xet"
	"github.com/weaveplatform/artifactweave/pkg/repo"
	"github.com/weaveplatform/artifactweave/pkg/store"
	"github.com/weaveplatform/artifactweave/pkg/types"
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
	b, e := os.ReadFile(filepath.Join("..", "format", "huggingface", "xet", "testdata", name))
	if e != nil {
		t.Fatal(e)
	}
	return b
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
		Create(t.Context(), types.Permission{Name: "xet-private-only", Resources: map[types.ResourceType]types.Resource{types.ResourceArtifact: {Actions: types.Grants{Users: map[string]types.Actions{"user": types.ActionRead | types.ActionWrite | types.ActionManage | types.ActionDelete}}, Targets: map[string]types.Target{"hf": {Include: []string{"models--acme--demo/.xet/**"}}, "other-local": {Include: []string{"**"}}}}}}); e != nil {
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
			if !errors.Is(err, store.ErrNotFound) {
				t.Fatalf("invalid shard published a file mapping: %v", err)
			}
		})
	}
	wantStatus(t, xetCall(w, write, "POST", "/v2/shards", shard), 200)
}
