package huggingface_test

import (
	"bytes"
	"encoding/base64"
	"io"
	"net/http"
	"testing"

	"github.com/weaveplatform/artifactweave/internal/source"
	"github.com/weaveplatform/artifactweave/pkg/repo"
)

func TestUnitVirtualKeepsOneRevisionMember(t *testing.T) {
	f := newFixture(t, repo.Virtual, func(f *fixture) *source.Service {
		for _, key := range []string{"empty", "first", "second"} {
			c := *repo.Defaults(&repo.Config{Key: key, Class: repo.Local, PackageType: repo.HuggingFaceML})
			if e := f.db.Repos().Create(t.Context(), c); e != nil {
				t.Fatal(e)
			}
		}
		c, e := f.db.Repos().Get(t.Context(), "hf")
		if e != nil {
			t.Fatal(e)
		}
		c.Repositories = []string{"removed", "empty", "first", "second"}
		if e := f.db.Repos().Update(t.Context(), c); e != nil {
			t.Fatal(e)
		}
		return nil
	})
	commit := func(key, file string) {
		t.Helper()
		body := `{"key":"header","value":{"summary":"publish"}}` + "\n" + `{"key":"file","value":{"path":"` + file + `","content":"` + base64.StdEncoding.EncodeToString(
			[]byte(file),
		) + `","encoding":"base64"}}` + "\n"
		req, e := http.NewRequest(
			"POST",
			f.server.URL+"/artifactory/api/huggingfaceml/"+key+"/api/models/acme/demo/commit/"+sha,
			bytes.NewBufferString(body),
		)
		if e != nil {
			t.Fatal(e)
		}
		req.Header.Set("Content-Type", "application/x-ndjson")
		r, e := f.server.Client().Do(req)
		if e != nil {
			t.Fatal(e)
		}
		b := readResponse(t, r)
		if r.StatusCode != 200 {
			t.Fatalf("%s commit %d %s", key, r.StatusCode, b)
		}
	}
	commit("first", "one.txt")
	commit("second", "two.txt")
	r := f.request("GET", "api/models/acme/demo/revision/"+sha, "", nil)
	b := readResponse(t, r)
	if r.StatusCode != 200 || !bytes.Contains(b, []byte("one.txt")) ||
		bytes.Contains(b, []byte("two.txt")) {
		t.Fatalf("virtual info %d %s", r.StatusCode, b)
	}
	r = f.request("GET", "acme/demo/resolve/"+sha+"/one.txt", "", nil)
	b = readResponse(t, r)
	if r.StatusCode != 200 || string(b) != "one.txt" {
		t.Fatalf("first member %d %s", r.StatusCode, b)
	}
	r = f.request("GET", "acme/demo/resolve/"+sha+"/two.txt", "", nil)
	_, _ = io.Copy(io.Discard, r.Body)
	_ = r.Body.Close()
	if r.StatusCode != 404 {
		t.Fatalf("mixed second member: %d", r.StatusCode)
	}
}

func TestUnitVirtualRejectsAbsentAndWrongFormatMembers(t *testing.T) {
	for _, tc := range []struct {
		name            string
		members         []string
		createWrongType bool
		want            int
	}{
		{"empty virtual", nil, false, 404},
		{"removed member", []string{"removed"}, false, 404},
		{"wrong package type", []string{"wrong"}, true, 400},
	} {
		t.Run(tc.name, func(t *testing.T) {
			f := newFixture(t, repo.Virtual, func(f *fixture) *source.Service {
				if tc.createWrongType {
					wrong := *repo.Defaults(&repo.Config{Key: "wrong", Class: repo.Local, PackageType: repo.NPM})
					if err := f.db.Repos().Create(t.Context(), wrong); err != nil {
						t.Fatal(err)
					}
				}
				virtual, err := f.db.Repos().Get(t.Context(), "hf")
				if err != nil {
					t.Fatal(err)
				}
				virtual.Repositories = tc.members
				if err := f.db.Repos().Update(t.Context(), virtual); err != nil {
					t.Fatal(err)
				}
				return nil
			})
			r := f.request("GET", "api/models/acme/demo/revision/main", "", nil)
			b := readResponse(t, r)
			if r.StatusCode != tc.want {
				t.Fatalf("virtual member policy: %d %s", r.StatusCode, b)
			}
		})
	}
}
