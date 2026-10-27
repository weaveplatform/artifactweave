package huggingface_test

import (
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
						r := f.request(http.MethodPost, "api/repos/create", "application/json", []byte(tc.body))
						body := readResponse(t, r)
						if r.StatusCode != http.StatusOK {
							t.Fatalf("create %d: %s", r.StatusCode, body)
						}
						var result struct {
							URL string `json:"url"`
						}
						if err := json.Unmarshal(body, &result); err != nil || !strings.HasSuffix(result.URL, tc.suffix) {
							t.Fatalf("create URL %q: %v", body, err)
						}
					}
				})
			}
		})
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
			r := f.requestAs(http.MethodPost, "api/repos/create", "application/json", []byte(tc.body), tc.user)
			body := readResponse(t, r)
			if r.StatusCode != tc.status {
				t.Fatalf("create status %d, want %d: %s", r.StatusCode, tc.status, body)
			}
		})
	}
}
