package vagranthttp_test

import (
	"encoding/json"
	"net/http/httptest"
	"testing"

	"github.com/weaveplatform/artifactweave/internal/access/accesstest"
	"github.com/weaveplatform/artifactweave/internal/access/api"
	"github.com/weaveplatform/artifactweave/internal/artifacts/api/repo"
)

// wantError fails unless r answers status with message as the one error of
// its Artifactory error envelope.
func wantError(t *testing.T, r *httptest.ResponseRecorder, status int, message string) {
	t.Helper()
	var envelope struct {
		Errors []struct {
			Status  int    `json:"status"`
			Message string `json:"message"`
		} `json:"errors"`
	}
	if r.Code != status || json.Unmarshal(r.Body.Bytes(), &envelope) != nil ||
		len(envelope.Errors) != 1 || envelope.Errors[0].Status != status ||
		envelope.Errors[0].Message != message {
		t.Fatalf("got %d %s, want %d %q", r.Code, r.Body.String(), status, message)
	}
}

// A blacked-out local or federated repository refuses a box's catalog with
// the blackout 404, naming the box the request names without its matrix
// parameters, to a caller who would see one of its entries without the
// blackout (R2a, R9, R10). Read permission comes first (DV-057): a caller
// who may read none of the entries, or whose matrix parameters match none,
// answers "Vagrant box not found" with or without the blackout, as for an
// unknown box, and one who may read nothing in the repository answers 403.
func TestAccBlackoutRefusesTheCatalogToCallersWhoSeeABox(t *testing.T) {
	const notFound = "Vagrant box not found"
	const blackedOut = "The repository 'boxes' is blacked out and cannot serve artifact " +
		"'boxes:acme/widget'."
	for _, class := range []repo.Class{repo.Local, repo.Federated} {
		t.Run(string(class), func(t *testing.T) {
			f := newFixture(t, class)
			f.publish("visible/a.box", "acme/widget", "1.0", "virtualbox")
			f.publish("hidden/a.box", "acme/widget", "2.0", "docker")
			for user, include := range map[string]string{
				"reader": "visible/**", "nonreader": "other/**",
			} {
				permission := accesstest.Permission{
					Name: user,
					Resources: map[api.ResourceType]accesstest.Resource{
						api.ResourceArtifact: {
							Actions: accesstest.Grants{
								Users: map[string]api.Actions{user: api.ActionRead},
							},
							Targets: map[string]accesstest.Target{
								"boxes": {Include: []string{include}},
							},
						},
					},
				}
				if err := f.db.Permissions().Create(t.Context(), permission); err != nil {
					t.Fatal(err)
				}
			}
			wantError(t, f.do("GET", base+"acme/widget", "nonreader", nil), 404, notFound)

			f.c.BlackedOut = new(true)
			if err := f.db.Repos().Update(t.Context(), f.c); err != nil {
				t.Fatal(err)
			}
			for _, user := range []string{"reader", "admin"} {
				wantError(t, f.do("GET", base+"acme/widget", user, nil), 404, blackedOut)
			}
			wantError(t, f.do("GET", base+"acme/widget;box_provider=virtualbox", "reader", nil),
				404, blackedOut)
			if r := f.do("HEAD", base+"acme/widget", "reader", nil); r.Code != 404 {
				t.Fatalf("HEAD: %d %s", r.Code, r.Body.String())
			}
			wantError(t, f.do("GET", base+"acme/widget", "nonreader", nil), 404, notFound)
			wantError(t, f.do("GET", base+"acme/widget;box_provider=docker", "reader", nil),
				404, notFound)
			wantError(t, f.do("GET", base+"missing", "admin", nil), 404, notFound)
			if r := f.do("GET", base+"acme/widget", "outsider", nil); r.Code != 403 {
				t.Fatalf("outsider: %d %s", r.Code, r.Body.String())
			}

			f.c.BlackedOut = nil
			if err := f.db.Repos().Update(t.Context(), f.c); err != nil {
				t.Fatal(err)
			}
			if c := f.catalog("acme/widget", "reader"); len(c.Versions) != 1 ||
				c.Versions[0].Version != "1.0" {
				t.Fatalf("restored: %+v", c)
			}
		})
	}
}
