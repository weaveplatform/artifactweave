package types

import (
	"errors"
	"fmt"
	"slices"
	"time"
)

// ErrInvalidEventType reports an event type the domain does not define.
var ErrInvalidEventType = errors.New("invalid webhook event type")

// WebhookDomain is the event family a subscription listens to.
type WebhookDomain string

// Webhook domains (research 07).
const (
	DomainArtifact                 WebhookDomain = "artifact"
	DomainArtifactProperty         WebhookDomain = "artifact_property"
	DomainDocker                   WebhookDomain = "docker"
	DomainBuild                    WebhookDomain = "build"
	DomainUser                     WebhookDomain = "user"
	DomainReleaseBundle            WebhookDomain = "release_bundle"
	DomainDistribution             WebhookDomain = "distribution"
	DomainArtifactoryReleaseBundle WebhookDomain = "artifactory_release_bundle"
	DomainDestination              WebhookDomain = "destination"
	DomainReleaseBundleV2          WebhookDomain = "release_bundle_v2"
	DomainReleaseBundleV2Promotion WebhookDomain = "release_bundle_v2_promotion"
	DomainArtifactLifecycle        WebhookDomain = "artifact_lifecycle"
	DomainXrayScanStatus           WebhookDomain = "xray_scan_status"
	DomainAppTrust                 WebhookDomain = "app_trust"
	DomainCuration                 WebhookDomain = "curation"
)

// webhookEventTypes lists each domain's event types as JFrog names them. The
// Terraform provider validates these client-side and expects them echoed
// verbatim. Domains with a nil entry (AppTrust, Curation) have undocumented
// names and are never emitted, so any non-empty name is stored as given.
var webhookEventTypes = map[WebhookDomain][]string{
	DomainArtifact:         {"deployed", "deleted", "moved", "copied", "cached"},
	DomainArtifactProperty: {"added", "deleted"},
	DomainDocker:           {"pushed", "deleted", "promoted"},
	DomainBuild:            {"uploaded", "deleted", "promoted"},
	DomainUser:             {"locked"},
	DomainReleaseBundle:    {"created", "signed", "deleted"},
	DomainDistribution: {
		"distribute_started",
		"distribute_completed",
		"distribute_aborted",
		"distribute_failed",
		"delete_started",
		"delete_completed",
		"delete_failed",
	},
	DomainArtifactoryReleaseBundle: {
		"received",
		"delete_started",
		"delete_completed",
		"delete_failed",
	},
	DomainDestination: {
		"received",
		"delete_started",
		"delete_completed",
		"delete_failed",
	},
	DomainReleaseBundleV2: {
		"release_bundle_v2_started",
		"release_bundle_v2_failed",
		"release_bundle_v2_completed",
	},
	DomainReleaseBundleV2Promotion: {
		"release_bundle_v2_promotion_started",
		"release_bundle_v2_promotion_failed",
		"release_bundle_v2_promotion_completed",
	},
	DomainArtifactLifecycle: {"archive", "restore"},
	DomainXrayScanStatus:    {"done", "failed", "partial", "not_supported"},
	DomainAppTrust:          nil,
	DomainCuration:          nil,
}

// ValidateEventTypes checks that every event type belongs to the domain.
func ValidateEventTypes(d WebhookDomain, types []string) error {
	known, ok := webhookEventTypes[d]
	if !ok {
		return fmt.Errorf("%w: unknown domain %q", ErrInvalidEventType, d)
	}
	if len(types) == 0 {
		return fmt.Errorf("%w: no event types for %q", ErrInvalidEventType, d)
	}
	for _, t := range types {
		if t == "" || (known != nil && !slices.Contains(known, t)) {
			return fmt.Errorf("%w: %q is not a %s event", ErrInvalidEventType, t, d)
		}
	}
	return nil
}

// WebhookCriteria select which events of the domain fire. Stored with the
// subscription as one document.
type WebhookCriteria struct {
	AnyLocal                     bool                `json:"any_local,omitempty"`
	AnyRemote                    bool                `json:"any_remote,omitempty"`
	AnyFederated                 bool                `json:"any_federated,omitempty"`
	AnyBuild                     bool                `json:"any_build,omitempty"`
	AnyReleaseBundle             bool                `json:"any_release_bundle,omitempty"`
	RepoKeys                     []string            `json:"repo_keys,omitempty"`
	SelectedBuilds               []string            `json:"selected_builds,omitempty"`
	IncludePatterns              []string            `json:"include_patterns,omitempty"`
	ExcludePatterns              []string            `json:"exclude_patterns,omitempty"`
	RegisteredReleaseBundleNames []string            `json:"registered_release_bundle_names,omitempty"`
	SelectedReleaseBundles       map[string][]string `json:"selected_release_bundles,omitempty"`
	SelectedEnvironments         []string            `json:"selected_environments,omitempty"`
	ApplicationKeys              []string            `json:"application_keys,omitempty"`
	Stages                       []string            `json:"stages,omitempty"`
}

// HandlerType is the delivery mechanism of a webhook handler.
type HandlerType string

// Handler types.
const (
	HandlerWebhook       HandlerType = "webhook"
	HandlerCustomWebhook HandlerType = "custom-webhook"
)

// Header is one HTTP header a handler adds.
type Header struct {
	Name  string `json:"name"`
	Value string `json:"value"`
}

// NamedSecret is a custom webhook secret. The value is sealed in its own
// table (decision 0017) and never written into the document.
type NamedSecret struct {
	Name  string `json:"name"`
	Value string `json:"-"`
}

// WebhookHandler delivers an event. Secret is sealed like NamedSecret.Value.
type WebhookHandler struct {
	Type                HandlerType   `json:"type"`
	URL                 string        `json:"url"`
	Proxy               string        `json:"proxy,omitempty"`
	Secret              string        `json:"-"`
	UseSecretForSigning bool          `json:"use_secret_for_signing,omitempty"`
	Headers             []Header      `json:"headers,omitempty"`
	Method              string        `json:"method,omitempty"`
	Payload             string        `json:"payload,omitempty"`
	Secrets             []NamedSecret `json:"secrets,omitempty"`
}

// WebhookSubscription is one subscription. Key and ProjectKey are immutable.
type WebhookSubscription struct {
	Key         string
	ProjectKey  string
	Description string
	Enabled     bool
	Debug       bool
	Domain      WebhookDomain
	EventTypes  []string
	Criteria    WebhookCriteria
	Handlers    []WebhookHandler
	Created     time.Time
	Updated     time.Time
}

// WebhookDelivery is one recorded delivery attempt for the troubleshooting
// endpoint. Request and Response are snapshots kept as documents.
type WebhookDelivery struct {
	ID          int64
	WebhookKey  string
	EventID     string
	EventType   string
	AttemptedAt time.Time
	Status      int
	Elapsed     time.Duration
	Request     []byte
	Response    []byte
}
