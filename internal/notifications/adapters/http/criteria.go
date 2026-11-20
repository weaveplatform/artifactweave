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
		AnyReleaseBundle: httpkit.PtrTo(b.AnyReleaseBundle), RepoKeys: optionalCriteriaList(b.RepoKeys),
		SelectedBuilds:  optionalCriteriaList(b.SelectedBuilds),
		IncludePatterns: optionalCriteriaList(b.IncludePatterns), ExcludePatterns: optionalCriteriaList(b.ExcludePatterns),
		RegisteredReleaseBundlesNames: optionalCriteriaList(b.RegisteredReleaseBundleNames),
		SelectedReleaseBundles:        bundleVersionsRead(b.SelectedReleaseBundles),
		SelectedEnvironments:          optionalCriteriaList(b.SelectedEnvironments),
		ApplicationKeys:               optionalCriteriaList(b.ApplicationKeys), Stages: optionalCriteriaList(b.Stages),
		AdditionalProperties: kernel.Clone(b.Extra),
	}
}

func optionalCriteriaList(values []string) *[]string {
	if len(values) == 0 {
		return nil
	}
	return httpkit.PtrTo(kernel.Clone(values))
}

func bundleVersionsRead(values map[string][]string) *map[string][]string {
	if len(values) == 0 {
		return nil
	}
	out := kernel.Clone(values)
	for name, versions := range out {
		if len(versions) == 0 {
			out[name] = []string{}
		}
	}
	return &out
}
