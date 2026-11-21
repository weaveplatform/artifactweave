package cleanup

import (
	"context"
	"fmt"
	"slices"
	"time"

	artifactsapi "github.com/weaveplatform/artifactweave/internal/artifacts/api"
	"github.com/weaveplatform/artifactweave/internal/artifacts/api/repo"
	"github.com/weaveplatform/artifactweave/internal/kernel"
	"github.com/weaveplatform/artifactweave/internal/retention/core/model"
)

// SelectionSnapshot records the rule and clock used to explain a selection.
// It is not permission to delete: CLN-04 rechecks current authority, item facts,
// comparator protection and its own run/lease state before effects.
type SelectionSnapshot struct {
	Policy model.CleanupPolicy
	At     time.Time
}

// Selection traverses only existing index metadata, in bounded pages. Its rule
// is immutable: editing/disabling a policy fences new run admission, not a run
// already admitted through WithPolicy. Each page still refreshes authority and
// canonical repository ownership/environment before disclosing metadata.
type Selection struct {
	service    Service
	snapshot   SelectionSnapshot
	criteria   versionCriteria
	properties map[string][]string
}

// Select starts a read-only evaluation of an authorized package policy. It
// starts no work and allows previewing an inactive policy. CLN-04 owns durable
// run admission; these descriptive results alone grant no execution authority.
func (s Service) Select(ctx context.Context, key string) (*Selection, error) {
	var out *Selection
	err := s.run(ctx, func(ctx context.Context, authority Policy) error {
		p, err := s.policy(ctx, key, authority)
		if err != nil {
			return err
		}
		if s.PackageIndex == nil {
			return fmt.Errorf("cleanup package index unavailable: %w", kernel.ErrStore)
		}
		if err := validateCriteria(p.ProjectKey, p.Criteria); err != nil {
			return err
		}
		at := s.now()
		out = &Selection{service: s, snapshot: SelectionSnapshot{Policy: kernel.Clone(p), At: at}, criteria: newVersionCriteria(p.Criteria, at)}
		out.properties = make(map[string][]string)
		for _, conditions := range []map[string][]string{p.Criteria.IncludedProperties, p.Criteria.ExcludedProperties} {
			for name, values := range conditions {
				out.properties[name] = append(out.properties[name], values...)
			}
		}
		return nil
	})
	if err != nil {
		return nil, err
	}
	return out, nil
}

// Snapshot returns a detached description, never a mutable policy handle.
func (s *Selection) Snapshot() SelectionSnapshot { return kernel.Clone(s.snapshot) }

func (s *Selection) run(ctx context.Context, fn func(context.Context) error) error {
	return s.service.run(ctx, func(ctx context.Context, authority Policy) error {
		if !authority.Allows(s.snapshot.Policy.ProjectKey) {
			return kernel.ErrForbidden
		}
		// Legacy previews describe a rule without granting execution. Newly
		// bound snapshots cannot silently move to a recreated project key.
		if s.snapshot.Policy.ProjectIncarnation != "" {
			if err := s.service.currentProject(ctx, s.snapshot.Policy); err != nil {
				return err
			}
		}
		return fn(ctx)
	})
}

func (s *Selection) repositoryQuery() artifactsapi.IndexRepositoryQuery {
	p, c := s.snapshot.Policy, s.snapshot.Policy.Criteria
	q := artifactsapi.IndexRepositoryQuery{
		Classes: []repo.Class{repo.Local, repo.Federated}, NoFederatedMembers: true,
		Repositories: slices.Clone(c.Repos), ExcludedRepositories: slices.Clone(c.ExcludedRepos),
		Projects: slices.Clone(c.IncludedProjects), AllProjects: c.IncludeAllProjects,
	}
	for _, kind := range c.PackageTypes {
		q.PackageTypes = append(q.PackageTypes, repo.PackageType(kind))
	}
	for i, project := range q.Projects {
		if project == "default" {
			q.Projects[i] = ""
		}
	}
	if p.ProjectKey != "" {
		q.Projects, q.AllProjects = []string{p.ProjectKey}, false
	}
	if c.IncludeAllStages != nil && !*c.IncludeAllStages {
		q.Environments = slices.Clone(c.IncludedStages)
	}
	return q
}

// Repositories applies project/type/environment and repository exclusions in
// the owning adapter before LIMIT; filtered foreign rows cannot consume pages.
func (s *Selection) Repositories(ctx context.Context, page kernel.Page) ([]artifactsapi.IndexRepository, string, error) {
	var out []artifactsapi.IndexRepository
	var next string
	err := s.run(ctx, func(ctx context.Context) error {
		var err error
		out, next, err = s.service.PackageIndex.Repositories(ctx, s.repositoryQuery(), page)
		return err //nolint:wrapcheck // run names the failed cleanup transaction
	})
	if err != nil {
		return nil, "", err
	}
	return out, next, nil
}

func (s *Selection) repository(ctx context.Context, ref artifactsapi.IndexRepository) error {
	c := s.snapshot.Policy.Criteria
	if !matchesSelection(compileSelectionPatterns(c.Repos), ref.Key) || matchesSelection(compileSelectionPatterns(c.ExcludedRepos), ref.Key) {
		return kernel.ErrForbidden
	}
	q := s.repositoryQuery()
	q.Repositories = []string{ref.Key}
	rows, _, err := s.service.PackageIndex.Repositories(ctx, q, kernel.Page{Limit: 1})
	if err != nil {
		return fmt.Errorf("current cleanup repository scope: %w", err)
	}
	if len(rows) != 1 || rows[0].Key != ref.Key {
		return kernel.ErrForbidden
	}
	actual := rows[0]
	if actual.Project != ref.Project || actual.Class != ref.Class || actual.PackageType != ref.PackageType || actual.HasFederatedMembers != ref.HasFederatedMembers ||
		!slices.Equal(actual.Environments, ref.Environments) {
		return fmt.Errorf("cleanup repository context changed: %w", kernel.ErrConflict)
	}
	return nil
}

func (s *Selection) packageAllowed(ref artifactsapi.IndexPackage) bool {
	return matchesSelection(s.criteria.packages, ref.Name) && !slices.Contains(s.snapshot.Policy.Criteria.ExcludedPackages, ref.Name)
}

// Packages preserves the index's continuation when a bounded page contains
// only excluded names. Package names remain opaque, including npm scopes.
func (s *Selection) Packages(ctx context.Context, ref artifactsapi.IndexRepository, page kernel.Page) ([]artifactsapi.IndexPackage, string, error) {
	var out []artifactsapi.IndexPackage
	var next string
	err := s.run(ctx, func(ctx context.Context) error {
		if err := s.repository(ctx, ref); err != nil {
			return err
		}
		var err error
		out, next, err = s.service.PackageIndex.Packages(ctx, ref, page)
		return err //nolint:wrapcheck // run names the failed cleanup transaction
	})
	if err != nil {
		return nil, "", err
	}
	out = slices.DeleteFunc(out, func(p artifactsapi.IndexPackage) bool { return !s.packageAllowed(p) })
	return out, next, nil
}

// Versions obtains keep-count protection from the entire comparator set before
// version inclusion filters. Traversal uses immutable version strings, not rank.
func (s *Selection) Versions(ctx context.Context, ref artifactsapi.IndexPackage, page kernel.Page) ([]artifactsapi.IndexVersion, string, error) {
	var out []artifactsapi.IndexVersion
	var next string
	err := s.run(ctx, func(ctx context.Context) error {
		if err := s.repository(ctx, ref.Repository); err != nil {
			return err
		}
		if !s.packageAllowed(ref) {
			return kernel.ErrForbidden
		}
		var err error
		out, next, err = s.service.PackageIndex.Versions(ctx, ref, s.snapshot.Policy.Criteria.KeepLastNVersions, page)
		return err //nolint:wrapcheck // run names the failed cleanup transaction
	})
	if err != nil {
		return nil, "", err
	}
	return out, next, nil
}

func (s *Selection) currentVersion(ctx context.Context, ref artifactsapi.IndexVersion) (artifactsapi.IndexVersion, error) {
	var out artifactsapi.IndexVersion
	err := s.run(ctx, func(ctx context.Context) error {
		if err := s.repository(ctx, ref.Package.Repository); err != nil {
			return err
		}
		if !s.packageAllowed(ref.Package) {
			return kernel.ErrForbidden
		}
		var err error
		out, err = s.service.PackageIndex.Version(ctx, ref, s.snapshot.Policy.Criteria.KeepLastNVersions)
		return err //nolint:wrapcheck // run names the failed cleanup transaction
	})
	return out, err
}

func (s *Selection) files(ctx context.Context, ref artifactsapi.IndexVersion, page kernel.Page) ([]artifactsapi.IndexFile, string, error) {
	var out []artifactsapi.IndexFile
	var next string
	err := s.run(ctx, func(ctx context.Context) error {
		if err := s.repository(ctx, ref.Package.Repository); err != nil {
			return err
		}
		if !s.packageAllowed(ref.Package) {
			return kernel.ErrForbidden
		}
		var err error
		out, next, err = s.service.PackageIndex.Files(ctx, ref, s.properties, page)
		return err //nolint:wrapcheck // run names the failed cleanup transaction
	})
	if err != nil {
		return nil, "", err
	}
	return out, next, nil
}
