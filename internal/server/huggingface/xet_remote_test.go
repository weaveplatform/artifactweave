package huggingface_test

import (
	"bytes"
	"context"
	"crypto/sha256"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"net/http/httptest"
	"net/url"
	"strconv"
	"strings"
	"sync/atomic"
	"testing"

	"github.com/go-chi/chi/v5"

	"github.com/weaveplatform/artifactweave/internal/format/huggingface/xet"
	"github.com/weaveplatform/artifactweave/internal/source"
	"github.com/weaveplatform/artifactweave/pkg/blob"
	"github.com/weaveplatform/artifactweave/pkg/repo"
)

func TestUnitRemoteXetUsesVerifiedCacheAndNoStorePolicy(t *testing.T) {
	for _, persist := range []bool{true, false} {
		t.Run(fmt.Sprint(persist), func(t *testing.T) {
			content := bytes.Repeat([]byte("remote Xet verified bytes"), 10000)
			if persist {
				content = bytes.Repeat([]byte("r"), xet.MaxXorb+xet.MaxChunk)
			}
			digest := fmt.Sprintf("%x", sha256.Sum256(content))
			var calls atomic.Int32
			var bytesRead atomic.Int64
			router := chi.NewRouter()
			router.Get(
				"/api/models/acme/demo/revision/main",
				func(w http.ResponseWriter, r *http.Request) {
					calls.Add(1)
					w.Header().Set("Content-Type", "application/json")
					_ = json.NewEncoder(w).
						Encode(map[string]any{"id": "acme/demo", "sha": sha, "siblings": []map[string]any{{"rfilename": "weights.bin", "size": len(content), "lfs": map[string]any{"sha256": digest, "size": len(content), "pointerSize": 130}}}})
				},
			)
			router.Get(
				"/acme/demo/resolve/main/weights.bin",
				func(w http.ResponseWriter, r *http.Request) { calls.Add(1); _, _ = w.Write(content) },
			)
			up := httptest.NewServer(router)
			defer up.Close()
			f := newFixture(t, repo.Remote, func(f *fixture) *source.Service {
				f.xetEnabled = true
				f.items.Blobs = xetCountedBlobs{Store: f.items.Blobs, sha: digest, read: &bytesRead}
				svc := remoteSetup(t, up.URL, 3600)(f)
				c, e := f.db.Repos().Get(t.Context(), "hf")
				if e != nil {
					t.Fatal(e)
				}
				c.StoreArtifactsLocally = &persist
				if e = f.db.Repos().Update(t.Context(), c); e != nil {
					t.Fatal(e)
				}
				return svc
			})
			head := f.request("HEAD", "acme/demo/resolve/main/weights.bin", "", nil)
			readResponse(t, head)
			hash := head.Header.Get("X-Xet-Hash")
			if !persist {
				if head.StatusCode != 200 || hash != "" {
					t.Fatal("no-store source advertised expensive Xet derivation")
				}
				response := f.request("GET", "acme/demo/resolve/main/weights.bin", "", nil)
				if got := readResponse(
					t,
					response,
				); response.StatusCode != 200 ||
					!bytes.Equal(got, content) {
					t.Fatal("no-store native Hub read changed")
				}
				return
			}
			if head.StatusCode != 200 || len(hash) != 64 {
				t.Fatalf("remote Xet head: %d %v", head.StatusCode, head.Header)
			}
			refresh := head.Header.Get("X-Xet-Refresh-Route")
			r, e := f.server.Client().Get(refresh)
			if e != nil {
				t.Fatal(e)
			}
			body := readResponse(t, r)
			var token struct {
				CAS   string `json:"casUrl"`
				Token string `json:"accessToken"`
			}
			if json.Unmarshal(body, &token) != nil || r.StatusCode != 200 {
				t.Fatalf("token: %d %s", r.StatusCode, body)
			}
			call := func(path string, expected int) []byte {
				t.Helper()
				req, e := http.NewRequestWithContext(t.Context(), "GET", token.CAS+path, nil)
				if e != nil {
					t.Fatal(e)
				}
				req.Header.Set("Authorization", "Bearer "+token.Token)
				r, e := f.server.Client().Do(req)
				if e != nil {
					t.Fatal(e)
				}
				body := readResponse(t, r)
				if r.StatusCode != expected {
					t.Fatalf("%s: %d %s", path, r.StatusCode, body)
				}
				return body
			}
			beforeMetadata := bytesRead.Load()
			rec := call("/v1/reconstructions/"+hash, 200)
			if bytesRead.Load() != beforeMetadata {
				t.Fatal("reconstruction re-read the complete verified file")
			}
			var parsed struct {
				Fetch map[string][]struct {
					URL string `json:"url"`
				} `json:"fetch_info"`
			}
			if e = json.Unmarshal(rec, &parsed); e != nil {
				t.Fatal(e)
			}
			for _, fetches := range parsed.Fetch {
				for _, fetch := range fetches {
					address, e := url.Parse(fetch.URL)
					if e != nil {
						t.Fatal(e)
					}
					parts := strings.Split(address.Path, "/")
					start, e := strconv.Atoi(parts[len(parts)-2])
					if e != nil {
						t.Fatal(e)
					}
					parts[len(parts)-1] = strconv.Itoa(start + 1)
					address.Path = strings.Join(parts, "/")
					r, e := f.server.Client().Get(address.String())
					if e != nil {
						t.Fatal(e)
					}
					body := readResponse(t, r)
					if r.StatusCode != 200 || len(body) == 0 {
						t.Fatalf("chunks: %d", r.StatusCode)
					}
				}
			}
			if delta := bytesRead.Load() - beforeMetadata; delta > int64(len(content)) {
				t.Fatalf(
					"chunk downloads scanned %d bytes, want at most one pass through %d bytes",
					delta,
					len(content),
				)
			}
			before := calls.Load()
			call("/v2/reconstructions/"+hash, 200)
			if persist && calls.Load() != before {
				t.Fatal("warm Xet cache fetched upstream")
			}
			if !persist && calls.Load() == before {
				t.Fatal("no-store source reused persistent content")
			}
			c, e := f.db.Repos().Get(t.Context(), "hf")
			if e != nil {
				t.Fatal(e)
			}
			c.ExcludesPattern = new("**/weights.bin")
			if e = f.db.Repos().Update(t.Context(), c); e != nil {
				t.Fatal(e)
			}
			body = call("/v1/reconstructions/"+hash, 404)
			if strings.Contains(string(body), hash) {
				t.Fatal("excluded source disclosed reconstruction")
			}
		})
	}
}

type xetCountedBlobs struct {
	blob.Store
	sha  string
	read *atomic.Int64
}

func (b xetCountedBlobs) Open(
	ctx context.Context,
	sha string,
) (io.ReadSeekCloser, blob.Info, error) {
	body, info, err := b.Store.Open(ctx, sha)
	if err != nil || sha != b.sha {
		return body, info, err
	}
	return xetCountedReader{ReadSeekCloser: body, read: b.read}, info, nil
}

type xetCountedReader struct {
	io.ReadSeekCloser
	read *atomic.Int64
}

func (r xetCountedReader) Read(p []byte) (int, error) {
	n, err := r.ReadSeekCloser.Read(p)
	r.read.Add(int64(n))
	return n, err
}
