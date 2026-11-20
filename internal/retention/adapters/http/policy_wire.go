package retentionhttp

import (
	"github.com/weaveplatform/artifactweave/internal/kernel"
	artgen "github.com/weaveplatform/artifactweave/internal/platform/gen/artifactory"
	"github.com/weaveplatform/artifactweave/internal/platform/httpkit"
	"github.com/weaveplatform/artifactweave/internal/retention/core/model"
)

func policyInput(b artgen.PackageCleanupPolicyRequest) model.CleanupPolicy {
	c := b.SearchCriteria
	return model.CleanupPolicy{
		Key: b.Key, Description: httpkit.Deref(b.Description), CronExp: httpkit.Deref(b.CronExp),
		ProjectKey: httpkit.Deref(b.ProjectKey), ItemType: model.CleanupItemType(httpkit.Deref(b.ItemType)),
		DurationMinutes: httpkit.Deref(b.DurationInMinutes), Enabled: httpkit.Deref(b.Enabled), SkipTrashcan: httpkit.Deref(b.SkipTrashcan),
		Criteria: model.CleanupCriteria{
			PackageTypes: kernel.Clone(c.PackageTypes), Repos: kernel.Clone(c.Repos), IncludedPackages: kernel.Clone(c.IncludedPackages),
			ExcludedRepos: kernel.Clone(httpkit.Deref(c.ExcludedRepos)), ExcludedPackages: kernel.Clone(httpkit.Deref(c.ExcludedPackages)),
			IncludeAllProjects: httpkit.Deref(c.IncludeAllProjects), IncludedProjects: kernel.Clone(httpkit.Deref(c.IncludedProjects)),
			IncludeAllStages: kernel.Clone(c.IncludeAllStages), IncludedStages: kernel.Clone(httpkit.Deref(c.IncludedStages)),
			CreatedBeforeInDays: httpkit.Deref(c.CreatedBeforeInDays), CreatedBeforeInMonths: httpkit.Deref(c.CreatedBeforeInMonths),
			LastDownloadedBeforeInDays: httpkit.Deref(c.LastDownloadedBeforeInDays), LastDownloadedBeforeInMonths: httpkit.Deref(c.LastDownloadedBeforeInMonths),
			KeepLastNVersions:  httpkit.Deref(c.KeepLastNVersions),
			IncludedProperties: kernel.Clone(httpkit.Deref(c.IncludedProperties)), ExcludedProperties: kernel.Clone(httpkit.Deref(c.ExcludedProperties)),
			IncludedPackageVersions: kernel.Clone(httpkit.Deref(c.IncludedPackageVersions)), ExcludedPackageVersions: kernel.Clone(httpkit.Deref(c.ExcludedPackageVersions)),
		},
	}
}

func policyWire(p model.CleanupPolicy) artgen.PackageCleanupPolicyResponse {
	c := p.Criteria
	stages := c.IncludeAllStages == nil || *c.IncludeAllStages
	criteria := artgen.PackageCleanupSearchCriteria{
		PackageTypes: kernel.Clone(c.PackageTypes), Repos: kernel.Clone(c.Repos), IncludedPackages: kernel.Clone(c.IncludedPackages),
		ExcludedRepos: optionalList(c.ExcludedRepos), ExcludedPackages: optionalList(c.ExcludedPackages),
		IncludeAllProjects: httpkit.PtrTo(c.IncludeAllProjects), IncludedProjects: optionalList(c.IncludedProjects),
		IncludeAllStages: &stages, IncludedStages: optionalList(c.IncludedStages),
		CreatedBeforeInDays: optionalInt(c.CreatedBeforeInDays), CreatedBeforeInMonths: optionalInt(c.CreatedBeforeInMonths),
		LastDownloadedBeforeInDays: optionalInt(c.LastDownloadedBeforeInDays), LastDownloadedBeforeInMonths: optionalInt(c.LastDownloadedBeforeInMonths),
		KeepLastNVersions:       optionalInt(c.KeepLastNVersions),
		IncludedPackageVersions: optionalList(c.IncludedPackageVersions), ExcludedPackageVersions: optionalList(c.ExcludedPackageVersions),
	}
	if len(c.IncludedProperties) != 0 {
		criteria.IncludedProperties = httpkit.PtrTo(kernel.Clone(c.IncludedProperties))
	}
	if len(c.ExcludedProperties) != 0 {
		criteria.ExcludedProperties = httpkit.PtrTo(kernel.Clone(c.ExcludedProperties))
	}
	return artgen.PackageCleanupPolicyResponse{
		Key: &p.Key, ItemType: httpkit.PtrTo(string(p.ItemType)), ProjectKey: optionalString(p.ProjectKey),
		Description: optionalString(p.Description), CronExp: optionalString(p.CronExp),
		DurationInMinutes: optionalInt(p.DurationMinutes), Enabled: &p.Enabled, SkipTrashcan: &p.SkipTrashcan, SearchCriteria: &criteria,
	}
}

func optionalString(value string) *string {
	if value == "" {
		return nil
	}
	return &value
}

func optionalInt(value int) *int {
	if value == 0 {
		return nil
	}
	return &value
}

func optionalList(value []string) *[]string {
	if len(value) == 0 {
		return nil
	}
	return httpkit.PtrTo(kernel.Clone(value))
}
