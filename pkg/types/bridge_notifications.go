// Bridge (P9d): declared in migration/notifications/core/model; PRO-02 deletes it.

package types

import ntfmodel "github.com/weaveplatform/artifactweave/migration/notifications/core/model"

var ErrInvalidEventType = ntfmodel.ErrInvalidEventType

var ValidateEventTypes = ntfmodel.ValidateEventTypes

type (
	WebhookDomain       = ntfmodel.WebhookDomain
	WebhookCriteria     = ntfmodel.WebhookCriteria
	HandlerType         = ntfmodel.HandlerType
	Header              = ntfmodel.Header
	NamedSecret         = ntfmodel.NamedSecret
	WebhookHandler      = ntfmodel.WebhookHandler
	WebhookSubscription = ntfmodel.WebhookSubscription
	WebhookDelivery     = ntfmodel.WebhookDelivery
)

const (
	DomainArtifact                 = ntfmodel.DomainArtifact
	DomainArtifactProperty         = ntfmodel.DomainArtifactProperty
	DomainDocker                   = ntfmodel.DomainDocker
	DomainBuild                    = ntfmodel.DomainBuild
	DomainUser                     = ntfmodel.DomainUser
	DomainReleaseBundle            = ntfmodel.DomainReleaseBundle
	DomainDistribution             = ntfmodel.DomainDistribution
	DomainArtifactoryReleaseBundle = ntfmodel.DomainArtifactoryReleaseBundle
	DomainDestination              = ntfmodel.DomainDestination
	DomainReleaseBundleV2          = ntfmodel.DomainReleaseBundleV2
	DomainReleaseBundleV2Promotion = ntfmodel.DomainReleaseBundleV2Promotion
	DomainArtifactLifecycle        = ntfmodel.DomainArtifactLifecycle
	DomainXrayScanStatus           = ntfmodel.DomainXrayScanStatus
	DomainAppTrust                 = ntfmodel.DomainAppTrust
	DomainCuration                 = ntfmodel.DomainCuration
	HandlerWebhook                 = ntfmodel.HandlerWebhook
	HandlerCustomWebhook           = ntfmodel.HandlerCustomWebhook
)
