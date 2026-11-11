package model_test

import (
	"encoding/json"
	"errors"
	"strings"
	"testing"

	"github.com/weaveplatform/artifactweave/internal/notifications/core/model"
)

func TestUnitWebhookEventTypes(t *testing.T) {
	ok := []struct {
		d     model.WebhookDomain
		types []string
	}{
		{model.DomainArtifact, []string{"deployed", "cached"}},
		{model.DomainReleaseBundleV2, []string{"release_bundle_v2_started"}},
		{model.DomainAppTrust, []string{"anything"}},
	}
	for _, c := range ok {
		if err := model.ValidateEventTypes(c.d, c.types); err != nil {
			t.Errorf("%s %v: %v", c.d, c.types, err)
		}
	}
	bad := []struct {
		d     model.WebhookDomain
		types []string
	}{
		{"unknown", []string{"x"}},
		{model.DomainArtifact, nil},
		{model.DomainArtifact, []string{"pushed"}},
		{model.DomainReleaseBundleV2, []string{"started"}},
		{model.DomainCuration, []string{""}},
	}
	for _, c := range bad {
		if err := model.ValidateEventTypes(
			c.d,
			c.types,
		); !errors.Is(
			err,
			model.ErrInvalidEventType,
		) {
			t.Errorf("%s %v: %v", c.d, c.types, err)
		}
	}
	h := model.WebhookHandler{
		Type: model.HandlerWebhook, URL: "u", Secret: "s",
		Secrets: []model.NamedSecret{{Name: "n", Value: "v"}},
	}
	raw, err := json.Marshal(h)
	if err != nil || strings.Contains(string(raw), `"s"`) || strings.Contains(string(raw), `"v"`) {
		t.Errorf("secrets leaked into the document: %s %v", raw, err)
	}
}
