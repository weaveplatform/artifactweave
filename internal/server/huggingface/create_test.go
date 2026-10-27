package huggingface_test

import (
	"bytes"
	"encoding/json"
	"net/http"
	"strings"
	"testing"

	"github.com/weaveplatform/artifactweave/pkg/repo"
)

func TestUnitHubCreateRepoForNativeFolderUpload(t *testing.T) {
	for _, class := range []repo.Class{repo.Local, repo.Federated} {
		t.Run(string(class), func(t *testing.T) {
			f := newFixture(t, class)
			for _, tc := range []struct {
				name, body, suffix string
			}{
				{"model", `{"name":"demo","organization":"acme","type":"model"}`, "/acme/demo"},
				{"dataset", `{"name":"demo","organization":"acme","type":"dataset"}`, "/datasets/acme/demo"},
			} {
				t.Run(tc.name, func(t *testing.T) {
					for range 2 { // Hub upload uses exist_ok=True; both calls must succeed.
						r := f.request(
							http.MethodPost,
							"api/repos/create",
							"application/json",
							[]byte(tc.body),
						)
						body := readResponse(t, r)
						if r.StatusCode != http.StatusOK {
							t.Fatalf("create %d: %s", r.StatusCode, body)
						}
						var result struct {
							URL string `json:"url"`
						}
						if err := json.Unmarshal(
							body,
							&result,
						); err != nil ||
							!strings.HasSuffix(result.URL, tc.suffix) {
							t.Fatalf("create URL %q: %v", body, err)
						}
					}
				})
			}
		})
	}
}

func TestUnitHubCreateBranchFromMainForNativeFolderUpload(t *testing.T) {
	f := newFixture(t, repo.Local)
	commit := []byte("{\"key\":\"header\",\"value\":{\"summary\":\"seed\"}}\n" +
		"{\"key\":\"file\",\"value\":{\"path\":\"config.json\",\"content\":\"e30=\",\"encoding\":\"base64\"}}\n")
	r := f.request(
		http.MethodPost,
		"api/models/acme/demo/commit/main",
		"application/x-ndjson",
		commit,
	)
	if body := readResponse(t, r); r.StatusCode != http.StatusOK {
		t.Fatalf("seed commit %d: %s", r.StatusCode, body)
	}
	r = f.request(http.MethodGet, "api/models/acme/demo/revision/v1", "", nil)
	if body := readResponse(
		t,
		r,
	); r.StatusCode != http.StatusNotFound ||
		r.Header.Get("X-Error-Code") != "RevisionNotFound" {
		t.Fatalf("missing branch %d %q: %s", r.StatusCode, r.Header.Get("X-Error-Code"), body)
	}
	r = f.request(
		http.MethodPost,
		"api/models/acme/demo/branch/v1",
		"application/json",
		[]byte(`{}`),
	)
	if body := readResponse(t, r); r.StatusCode != http.StatusOK {
		t.Fatalf("create branch %d: %s", r.StatusCode, body)
	}
	r = f.request(http.MethodGet, "api/models/acme/demo/revision/v1", "", nil)
	if body := readResponse(
		t,
		r,
	); r.StatusCode != http.StatusOK ||
		!bytes.Contains(body, []byte(`"rfilename":"config.json"`)) {
		t.Fatalf("branch info %d: %s", r.StatusCode, body)
	}
	r = f.request(
		http.MethodPost,
		"api/models/acme/demo/branch/v1",
		"application/json",
		[]byte(`{}`),
	)
	if body := readResponse(t, r); r.StatusCode != http.StatusConflict {
		t.Fatalf("duplicate branch %d: %s", r.StatusCode, body)
	}
}

func TestUnitHubCreateRepoRejectsInvalidOrUnauthorizedRequests(t *testing.T) {
	f := newFixture(t, repo.Local)
	for _, tc := range []struct {
		name, body, user string
		status           int
	}{
		{"invalid identity", `{"name":"../demo","organization":"acme","type":"model"}`, "", http.StatusBadRequest},
		{"unsupported kind", `{"name":"demo","organization":"acme","type":"space"}`, "", http.StatusBadRequest},
		{"private visibility", `{"name":"demo","organization":"acme","type":"model","visibility":"private"}`, "", http.StatusBadRequest},
		{"unknown field", `{"name":"demo","organization":"acme","type":"model","ignored":true}`, "", http.StatusBadRequest},
		{"denied writer", `{"name":"demo","organization":"acme","type":"model"}`, "intruder", http.StatusForbidden},
	} {
		t.Run(tc.name, func(t *testing.T) {
			r := f.requestAs(
				http.MethodPost,
				"api/repos/create",
				"application/json",
				[]byte(tc.body),
				tc.user,
			)
			body := readResponse(t, r)
			if r.StatusCode != tc.status {
				t.Fatalf("create status %d, want %d: %s", r.StatusCode, tc.status, body)
			}
		})
	}
}

func TestUnitHubCreateRepoFollowsRepositoryWritePolicy(t *testing.T) {
	for _, tc := range []struct {
		class  repo.Class
		status int
	}{
		{repo.Remote, http.StatusNotImplemented},
		{repo.Virtual, http.StatusMethodNotAllowed},
	} {
		t.Run(string(tc.class), func(t *testing.T) {
			f := newFixture(t, tc.class)
			r := f.request(http.MethodPost, "api/repos/create", "application/json",
				[]byte(`{"name":"demo","organization":"acme","type":"model"}`))
			body := readResponse(t, r)
			if r.StatusCode != tc.status {
				t.Fatalf("create on %s: %d %s", tc.class, r.StatusCode, body)
			}
		})
	}
}
