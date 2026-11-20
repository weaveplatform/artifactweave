package notificationshttp

import (
	"fmt"

	"github.com/weaveplatform/artifactweave/internal/kernel"
	"github.com/weaveplatform/artifactweave/internal/notifications/core/model"
	eventgen "github.com/weaveplatform/artifactweave/internal/platform/gen/event"
	"github.com/weaveplatform/artifactweave/internal/platform/httpkit"
)

func criteriaInput(filter eventgen.EventFilter) (model.WebhookCriteria, error) {
	b := filter.Criteria
	if b == nil {
		if filter.Domain != string(model.DomainUser) && filter.Domain != string(model.DomainCuration) {
			return model.WebhookCriteria{}, fmt.Errorf("criteria are required for this event domain: %w", kernel.ErrInvalid)
		}
		return model.WebhookCriteria{}, nil
	}
	return model.WebhookCriteria{
		AnyLocal: httpkit.Deref(b.AnyLocal), AnyRemote: httpkit.Deref(b.AnyRemote),
		AnyFederated: httpkit.Deref(b.AnyFederated), AnyBuild: httpkit.Deref(b.AnyBuild),
		AnyReleaseBundle: httpkit.Deref(b.AnyReleaseBundle), RepoKeys: kernel.Clone(httpkit.Deref(b.RepoKeys)),
		SelectedBuilds:  kernel.Clone(httpkit.Deref(b.SelectedBuilds)),
		IncludePatterns: kernel.Clone(httpkit.Deref(b.IncludePatterns)), ExcludePatterns: kernel.Clone(httpkit.Deref(b.ExcludePatterns)),
		RegisteredReleaseBundleNames: kernel.Clone(httpkit.Deref(b.RegisteredReleaseBundlesNames)),
		SelectedReleaseBundles:       kernel.Clone(httpkit.Deref(b.SelectedReleaseBundles)),
		SelectedEnvironments:         kernel.Clone(httpkit.Deref(b.SelectedEnvironments)),
		ApplicationKeys:              kernel.Clone(httpkit.Deref(b.ApplicationKeys)), Stages: kernel.Clone(httpkit.Deref(b.Stages)),
		Extra: kernel.Clone(b.AdditionalProperties),
	}, nil
}

func criteriaRead(b model.WebhookCriteria) *eventgen.EventFilterCriteria {
	return &eventgen.EventFilterCriteria{
		AnyLocal: httpkit.PtrTo(b.AnyLocal), AnyRemote: httpkit.PtrTo(b.AnyRemote),
		AnyFederated: httpkit.PtrTo(b.AnyFederated), AnyBuild: httpkit.PtrTo(b.AnyBuild),
		AnyReleaseBundle: httpkit.PtrTo(b.AnyReleaseBundle), RepoKeys: httpkit.PtrTo(kernel.Clone(b.RepoKeys)),
		SelectedBuilds:  httpkit.PtrTo(kernel.Clone(b.SelectedBuilds)),
		IncludePatterns: httpkit.PtrTo(kernel.Clone(b.IncludePatterns)), ExcludePatterns: httpkit.PtrTo(kernel.Clone(b.ExcludePatterns)),
		RegisteredReleaseBundlesNames: httpkit.PtrTo(kernel.Clone(b.RegisteredReleaseBundleNames)),
		SelectedReleaseBundles:        httpkit.PtrTo(kernel.Clone(b.SelectedReleaseBundles)),
		SelectedEnvironments:          httpkit.PtrTo(kernel.Clone(b.SelectedEnvironments)),
		ApplicationKeys:               httpkit.PtrTo(kernel.Clone(b.ApplicationKeys)), Stages: httpkit.PtrTo(kernel.Clone(b.Stages)),
		AdditionalProperties: kernel.Clone(b.Extra),
	}
}
