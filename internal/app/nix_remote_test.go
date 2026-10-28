package app

import (
	"bytes"
	"errors"
	"net/http"
	"os"
	"strings"
	"testing"

	"github.com/weaveplatform/artifactweave/internal/fakeupstream"
	nixfmt "github.com/weaveplatform/artifactweave/internal/format/nix"
	"github.com/weaveplatform/artifactweave/pkg/repo"
	"github.com/weaveplatform/artifactweave/pkg/store"
)

func TestAccNixRawRoutesCannotBypassNativeValidation(t *testing.T) {
	for _, cache := range []bool{true, false} {
		t.Run(map[bool]string{true: "cached", false: "transient"}[cache], func(t *testing.T) {
			w := newRemoteWorld(t)
			w.c.PackageType = repo.Nix
			w.c.StoreArtifactsLocally = &cache
			w.update()
			const path = "fyli4q2yj6vh01bs0jxqbvj5hir5zvqc.narinfo"
			metadata, err := os.ReadFile("../format/nix/testdata/cache/" + path)
			if err != nil {
				t.Fatal(err)
			}
			info, err := nixfmt.Parse(metadata, path)
			if err != nil {
				t.Fatal(err)
			}
			archive, err := os.ReadFile("../format/nix/testdata/cache/" + info.URL)
			if err != nil {
				t.Fatal(err)
			}
			w.up.Set("/"+path, fakeupstream.Resource{Body: metadata})
			w.up.Set("/"+info.URL, fakeupstream.Resource{Body: []byte("corrupt")})
			base := "/artifactory/api/nix/remote/"
			remoteStatus(t, w.request("GET", base+path, "denied", ""), 403)
			for _, key := range []string{"remote", "remote-cache"} {
				for _, method := range []string{"GET", "HEAD"} {
					for _, p := range []string{path, info.URL, info.URL + ".sha256"} {
						remoteStatus(
							t,
							w.request(method, "/artifactory/"+key+"/"+p, "reader", ""),
							400,
						)
					}
				}
			}
			if w.up.Count("", "") != 0 {
				t.Fatal("generic or denied path fetched upstream")
			}
			meta := w.request("GET", base+path, "reader", "")
			remoteStatus(t, meta, 200)
			if bytes.Contains(meta.Body.Bytes(), []byte("CA:")) {
				t.Fatal("unverified content address escaped")
			}
			native := base + "nar/" + path[:32] + "/" + strings.TrimPrefix(info.URL, "nar/")
			remoteStatus(t, w.request("GET", native, "reader", ""), 502)
			if _, err = w.rt.store.Items().
				Get(t.Context(), "remote", true, info.URL); !errors.Is(
				err,
				store.ErrNotFound,
			) {
				t.Fatalf("corrupt archive cached: %v", err)
			}
			w.up.Set(
				"/"+info.URL,
				fakeupstream.Resource{
					Header: http.Header{"Content-Type": {"application/x-nix-nar"}},
					Body:   archive,
				},
			)
			content := w.request("GET", native, "reader", "")
			remoteStatus(t, content, 200)
			if !bytes.Equal(content.Body.Bytes(), archive) {
				t.Fatal("changed archive")
			}
			for _, key := range []string{"remote", "remote-cache"} {
				remoteStatus(
					t,
					w.request("GET", "/artifactory/"+key+"/"+info.URL, "reader", ""),
					400,
				)
			}
			_, err = w.rt.store.Items().Get(t.Context(), "remote", true, info.URL)
			if cache && err != nil {
				t.Fatal(err)
			}
			if !cache && !errors.Is(err, store.ErrNotFound) {
				t.Fatalf("transient archive retained: %v", err)
			}
		})
	}
}
