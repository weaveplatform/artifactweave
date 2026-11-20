package notificationshttp

import (
	"encoding/json"
	"errors"
	"reflect"
	"testing"

	"github.com/weaveplatform/artifactweave/internal/kernel"
	"github.com/weaveplatform/artifactweave/internal/notifications/core/model"
	eventgen "github.com/weaveplatform/artifactweave/internal/platform/gen/event"
)

func TestUnitWebhookCriteriaWireAndStorageRemainDistinct(t *testing.T) {
	var filter eventgen.EventFilter
	if err := json.Unmarshal([]byte(`{
		"domain":"artifact", "event_types":["deployed"], "criteria":{
			"anyLocal":true,"anyRemote":true,"anyFederated":true,"anyBuild":true,"anyReleaseBundle":true,
			"repoKeys":["libs"],"selectedBuilds":["build"],"includePatterns":["**"],"excludePatterns":["private/**"],
			"registeredReleaseBundlesNames":["bundle"],"selectedReleaseBundles":{"bundle":["1"]},
			"selectedEnvironments":["PROD"],"applicationKeys":["app"],"stages":["stage"],
			"futureCondition":{"nested":["unchanged",true]}
		}}
	`), &filter); err != nil {
		t.Fatal(err)
	}
	stored, err := criteriaInput(filter)
	if err != nil {
		t.Fatal(err)
	}
	// Storage uses snake_case. Marshaling the generated request directly would
	// silently lose these selectors when the store later reads its own model.
	data, err := json.Marshal(stored)
	if err != nil {
		t.Fatal(err)
	}
	var restored model.WebhookCriteria
	if err := json.Unmarshal(data, &restored); err != nil {
		t.Fatal(err)
	}
	if got := criteriaRead(restored, model.DomainArtifact); !reflect.DeepEqual(got, filter.Criteria) {
		t.Fatalf("criteria changed across wire/storage round trip: got=%+v want=%+v", got, filter.Criteria)
	}
	filter.Criteria.RepoKeys = nil
	filter.Criteria.AdditionalProperties["futureCondition"].(map[string]any)["nested"].([]any)[0] = "changed"
	if restored.RepoKeys[0] != "libs" || stored.Extra["futureCondition"].(map[string]any)["nested"].([]any)[0] != "unchanged" {
		t.Fatal("criteria input aliases the request")
	}
	view := criteriaRead(restored, model.DomainArtifact)
	(*view.RepoKeys)[0] = "changed"
	view.AdditionalProperties["futureCondition"].(map[string]any)["nested"].([]any)[0] = "changed"
	if restored.RepoKeys[0] != "libs" || restored.Extra["futureCondition"].(map[string]any)["nested"].([]any)[0] != "unchanged" {
		t.Fatal("criteria output aliases storage")
	}
}

func TestUnitWebhookEmptyRepositorySelectorRemainsAnArray(t *testing.T) {
	for _, domain := range []model.WebhookDomain{model.DomainArtifact, model.DomainArtifactProperty, model.DomainDocker, model.DomainUser} {
		t.Run(string(domain), func(t *testing.T) {
			view := criteriaRead(model.WebhookCriteria{}, domain)
			if domain == model.DomainUser {
				if view.RepoKeys != nil {
					t.Fatal("unrelated domain acquired a repository selector")
				}
				return
			}
			data, err := json.Marshal(view)
			if err != nil {
				t.Fatal(err)
			}
			var got map[string]json.RawMessage
			if err := json.Unmarshal(data, &got); err != nil {
				t.Fatal(err)
			}
			if string(got["repoKeys"]) != "[]" {
				t.Fatal("empty required repository selector became null or absent")
			}
		})
	}
}

func TestUnitWebhookCriteriaPresenceIsDomainSpecific(t *testing.T) {
	for _, domain := range []string{"artifact", "artifact_property", "docker", "build", "release_bundle", "distribution", "artifactory_release_bundle", "destination", "release_bundle_v2", "release_bundle_v2_promotion", "artifact_lifecycle", "xray_scan_status", "app_trust", "user", "curation"} {
		t.Run(domain, func(t *testing.T) {
			filter := eventgen.EventFilter{Domain: domain}
			_, err := criteriaInput(filter)
			optional := domain == "user" || domain == "curation" || domain == "artifact_lifecycle"
			if optional && err != nil || !optional && !errors.Is(err, kernel.ErrInvalid) {
				t.Fatalf("missing criteria: %v", err)
			}
			filter.Criteria = &eventgen.EventFilterCriteria{}
			if _, err := criteriaInput(filter); err != nil {
				t.Fatalf("explicit empty criteria: %v", err)
			}
		})
	}
}
