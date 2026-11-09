package model_test

import (
	"encoding/json"
	"errors"
	"strings"
	"testing"

	"github.com/weaveplatform/artifactweave/pkg/types"
)

func TestUnitWebhookEventTypes(t *testing.T) {
	ok := []struct {
		d     types.WebhookDomain
		types []string
	}{
		{types.DomainArtifact, []string{"deployed", "cached"}},
		{types.DomainReleaseBundleV2, []string{"release_bundle_v2_started"}},
		{types.DomainAppTrust, []string{"anything"}},
	}
	for _, c := range ok {
		if err := types.ValidateEventTypes(c.d, c.types); err != nil {
			t.Errorf("%s %v: %v", c.d, c.types, err)
		}
	}
	bad := []struct {
		d     types.WebhookDomain
		types []string
	}{
		{"unknown", []string{"x"}},
		{types.DomainArtifact, nil},
		{types.DomainArtifact, []string{"pushed"}},
		{types.DomainReleaseBundleV2, []string{"started"}},
		{types.DomainCuration, []string{""}},
	}
	for _, c := range bad {
		if err := types.ValidateEventTypes(
			c.d,
			c.types,
		); !errors.Is(
			err,
			types.ErrInvalidEventType,
		) {
			t.Errorf("%s %v: %v", c.d, c.types, err)
		}
	}
	h := types.WebhookHandler{
		Type: types.HandlerWebhook, URL: "u", Secret: "s",
		Secrets: []types.NamedSecret{{Name: "n", Value: "v"}},
	}
	raw, err := json.Marshal(h)
	if err != nil || strings.Contains(string(raw), `"s"`) || strings.Contains(string(raw), `"v"`) {
		t.Errorf("secrets leaked into the document: %s %v", raw, err)
	}
}
