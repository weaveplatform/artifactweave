package cleanup_test

import (
	"errors"
	"reflect"
	"strings"
	"testing"

	"github.com/weaveplatform/artifactweave/internal/kernel"
	"github.com/weaveplatform/artifactweave/internal/retention/core/model"
)

func TestUnitPolicyValidationRefusesInvalidInputWithoutPersistence(t *testing.T) {
	for _, tc := range []struct {
		name string
		edit func(*model.CleanupPolicy)
	}{
		{"enabled create", func(p *model.CleanupPolicy) { p.Enabled = true }},
		{"key mismatch", func(p *model.CleanupPolicy) { p.Key = "other-key" }},
		{"wrong item type", func(p *model.CleanupPolicy) { p.ItemType = model.CleanupBuild }},
		{"long description", func(p *model.CleanupPolicy) { p.Description = strings.Repeat("é", 257) }},
		{"negative duration", func(p *model.CleanupPolicy) { p.DurationMinutes = -1 }},
		{"Unix cron", func(p *model.CleanupPolicy) { p.CronExp = "0 2 * * *" }},
		{"missing package types", func(p *model.CleanupPolicy) { p.Criteria.PackageTypes = nil }},
		{"unknown package type", func(p *model.CleanupPolicy) { p.Criteria.PackageTypes = []string{"invented"} }},
		{"missing repositories", func(p *model.CleanupPolicy) { p.Criteria.Repos = nil }},
		{"missing packages", func(p *model.CleanupPolicy) { p.Criteria.IncludedPackages = nil }},
		{"negative age", func(p *model.CleanupPolicy) { p.Criteria.LastDownloadedBeforeInDays = -1 }},
		{"empty entry", func(p *model.CleanupPolicy) { p.Criteria.Repos = []string{" "} }},
		{"wildcard exclusion", func(p *model.CleanupPolicy) { p.Criteria.ExcludedPackages = []string{"keep*"} }},
		{"stage mismatch", func(p *model.CleanupPolicy) { p.Criteria.IncludedStages = []string{"DEV"} }},
		{"missing stages", func(p *model.CleanupPolicy) { all := false; p.Criteria.IncludeAllStages = &all }},
		{"project widening", func(p *model.CleanupPolicy) { p.ProjectKey = "alpha"; p.Criteria.IncludeAllProjects = true }},
		{"foreign included project", func(p *model.CleanupPolicy) { p.ProjectKey = "alpha"; p.Criteria.IncludedProjects = []string{"beta"} }},
		{"unknown project", func(p *model.CleanupPolicy) { p.ProjectKey = "missing" }},
		{"unknown literal repository", func(p *model.CleanupPolicy) { p.Criteria.Repos = []string{"missing"} }},
	} {
		t.Run(tc.name, func(t *testing.T) {
			s, db := world(t, "*")
			p := input("validate-policy", "")
			tc.edit(&p)
			_, err := s.Create(t.Context(), "validate-policy", p)
			if !errors.Is(err, kernel.ErrInvalid) {
				t.Fatalf("invalid policy accepted or misclassified: %v", err)
			}
			_, err = db.Cleanup().Policy(t.Context(), "validate-policy")
			if !errors.Is(err, kernel.ErrNotFound) {
				t.Fatalf("invalid policy persisted: %v", err)
			}
		})
	}
}

func TestUnitPolicyAvailabilityDoesNotRewriteStoredCriteria(t *testing.T) {
	s, db := world(t, "*")
	ctx := t.Context()
	p := input("availability-policy", "alpha")
	p.Criteria.Repos = []string{"alpha-npm", "alpha-*"}
	p.Criteria.ExcludedRepos = []string{"alpha-npm", "old-*"}
	_, err := s.Create(ctx, p.Key, p)
	must(t, err)
	must(t, db.Repos().Delete(ctx, "alpha-npm"))
	view, err := s.Get(ctx, p.Key, true)
	must(t, err)
	if !reflect.DeepEqual(view.Criteria.Repos, []string{"alpha-*"}) || !reflect.DeepEqual(view.Criteria.ExcludedRepos, []string{"old-*"}) {
		t.Fatalf("availability projection differs: %+v", view.Criteria)
	}
	stored, err := s.Get(ctx, p.Key, false)
	must(t, err)
	if !reflect.DeepEqual(stored.Criteria, p.Criteria) {
		t.Fatal("availability checking changed stored criteria")
	}
}
