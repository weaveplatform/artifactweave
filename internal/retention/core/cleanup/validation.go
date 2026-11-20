package cleanup

import (
	"context"
	"errors"
	"fmt"
	"regexp"
	"slices"
	"strings"
	"unicode/utf8"

	"github.com/weaveplatform/artifactweave/internal/artifacts/api/repo"
	"github.com/weaveplatform/artifactweave/internal/kernel"
	"github.com/weaveplatform/artifactweave/internal/platform/schedule"
	"github.com/weaveplatform/artifactweave/internal/retention/core/model"
)

var policyKey = regexp.MustCompile(`^[A-Za-z0-9_-]{3,}$`)

func (s Service) validate(ctx context.Context, key string, p *model.CleanupPolicy) error {
	if p.Key != key || !policyKey.MatchString(key) || schedule.ValidateReference(schedule.Reference{Kind: ScheduleKind, Key: key}) != nil {
		return fmt.Errorf("policy key must match the path and contain at least three letters, digits, underscores or hyphens: %w", kernel.ErrInvalid)
	}
	if p.ItemType == "" {
		p.ItemType = model.CleanupPackage
	}
	if p.ItemType != model.CleanupPackage {
		return fmt.Errorf("itemType must be package: %w", kernel.ErrInvalid)
	}
	if !utf8.ValidString(p.Description) || utf8.RuneCountInString(p.Description) > 256 || p.DurationMinutes < 0 {
		return fmt.Errorf("description must have at most 256 characters and duration must be nonnegative: %w", kernel.ErrInvalid)
	}
	if p.CronExp != "" {
		if _, err := schedule.Parse(p.CronExp, s.Zone); err != nil {
			return fmt.Errorf("policy cronExp: %w", err)
		}
	}
	if p.ProjectKey != "" {
		if s.ProjectExists == nil {
			return fmt.Errorf("cleanup project reader is unavailable: %w", kernel.ErrStore)
		}
		if err := s.ProjectExists(ctx, p.ProjectKey); errors.Is(err, kernel.ErrNotFound) {
			return fmt.Errorf("policy project does not exist: %w", kernel.ErrInvalid)
		} else if err != nil {
			return fmt.Errorf("read cleanup project: %w", err)
		}
	}
	if err := validateCriteria(p.ProjectKey, p.Criteria); err != nil {
		return err
	}
	for _, list := range [][]string{p.Criteria.Repos, p.Criteria.ExcludedRepos} {
		for _, key := range list {
			if pattern(key) {
				continue
			}
			if s.Repositories == nil {
				return fmt.Errorf("cleanup repository reader is unavailable: %w", kernel.ErrStore)
			}
			r, err := s.Repositories.ProjectRepository(ctx, key)
			if errors.Is(err, kernel.ErrNotFound) {
				return fmt.Errorf("repository %q is not available in the policy scope: %w", key, kernel.ErrInvalid)
			}
			if err != nil {
				return fmt.Errorf("read cleanup repository: %w", err)
			}
			if p.ProjectKey != "" && r.ProjectKey != p.ProjectKey {
				return fmt.Errorf("repository criteria exceed the policy project: %w", kernel.ErrForbidden)
			}
		}
	}
	return nil
}

func validateCriteria(project string, c model.CleanupCriteria) error {
	if len(c.PackageTypes) == 0 || len(c.Repos) == 0 || len(c.IncludedPackages) == 0 {
		return fmt.Errorf("packageTypes, repos and includedPackages must each contain a value: %w", kernel.ErrInvalid)
	}
	for _, name := range c.PackageTypes {
		if !slices.Contains(repo.AllowedPackageTypes(repo.Local), repo.PackageType(name)) && !slices.Contains(repo.AllowedPackageTypes(repo.Remote), repo.PackageType(name)) {
			return fmt.Errorf("unknown package type %q: %w", name, kernel.ErrInvalid)
		}
	}
	for _, n := range []int{c.CreatedBeforeInDays, c.CreatedBeforeInMonths, c.LastDownloadedBeforeInDays, c.LastDownloadedBeforeInMonths, c.KeepLastNVersions} {
		if n < 0 {
			return fmt.Errorf("age and version-count criteria must be nonnegative: %w", kernel.ErrInvalid)
		}
	}
	for _, list := range [][]string{c.PackageTypes, c.Repos, c.ExcludedRepos, c.IncludedPackages, c.ExcludedPackages, c.IncludedProjects, c.IncludedStages, c.IncludedPackageVersions, c.ExcludedPackageVersions} {
		for _, value := range list {
			if strings.TrimSpace(value) == "" || !utf8.ValidString(value) || strings.ContainsRune(value, 0) {
				return fmt.Errorf("criteria entries must be nonempty strings: %w", kernel.ErrInvalid)
			}
		}
	}
	for _, value := range c.ExcludedPackages {
		if pattern(value) {
			return fmt.Errorf("excludedPackages accepts exact names only: %w", kernel.ErrInvalid)
		}
	}
	allStages := c.IncludeAllStages == nil || *c.IncludeAllStages
	if allStages && len(c.IncludedStages) != 0 || !allStages && len(c.IncludedStages) == 0 {
		return fmt.Errorf("includedStages is required only when includeAllStages is false: %w", kernel.ErrInvalid)
	}
	if project != "" {
		if c.IncludeAllProjects {
			return fmt.Errorf("a project policy cannot include all projects: %w", kernel.ErrInvalid)
		}
		for _, key := range c.IncludedProjects {
			if key != project {
				return fmt.Errorf("includedProjects exceeds the policy project: %w", kernel.ErrInvalid)
			}
		}
	}
	return nil
}

func pattern(value string) bool { return strings.ContainsAny(value, "*?") }

func (s Service) available(ctx context.Context, project string, keys []string) ([]string, error) {
	out := make([]string, 0, len(keys))
	for _, key := range keys {
		if pattern(key) {
			out = append(out, key)
			continue
		}
		if s.Repositories == nil {
			return nil, fmt.Errorf("cleanup repository reader is unavailable: %w", kernel.ErrStore)
		}
		r, err := s.Repositories.ProjectRepository(ctx, key)
		if errors.Is(err, kernel.ErrNotFound) {
			continue
		}
		if err != nil {
			return nil, fmt.Errorf("check cleanup repository availability: %w", err)
		}
		if project == "" || r.ProjectKey == project {
			out = append(out, key)
		}
	}
	return out, nil
}
