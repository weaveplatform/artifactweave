package cleanup

import (
	"math"
	"testing"
	"time"

	"github.com/weaveplatform/artifactweave/internal/retention/core/model"
)

func TestUnitCleanupVersionSelectionProtectsNewestBeforeFiltering(t *testing.T) {
	criteria := model.CleanupCriteria{IncludedPackages: []string{"**"}, IncludedPackageVersions: []string{"2"}}
	matcher := newVersionCriteria(criteria, time.Now())
	for _, tc := range []struct {
		name, version string
		protected     bool
		want          bool
	}{
		{"newest always protected", "10", true, false},
		{"nonmatching version is not counted away", "9", false, false},
		{"older included version remains eligible", "2", false, true},
	} {
		t.Run(tc.name, func(t *testing.T) {
			if got := matcher.eligible(versionFacts{Package: "@scope/name", Version: tc.version, Protected: tc.protected}); got != tc.want {
				t.Fatalf("eligible = %t, want %t", got, tc.want)
			}
		})
	}
}

func TestUnitCleanupVersionSelectionExclusionsWin(t *testing.T) {
	facts := versionFacts{
		Package: "@scope/name", Version: "2.0.0-beta.1", Protected: false,
		LeadProperties: map[string][]string{"release": {"candidate"}, "protected": {"true"}},
	}
	for _, tc := range []struct {
		name     string
		criteria model.CleanupCriteria
		want     bool
	}{
		{"scoped package name", model.CleanupCriteria{IncludedPackages: []string{"@scope/*"}}, true},
		{"exact excluded package", model.CleanupCriteria{IncludedPackages: []string{"**"}, ExcludedPackages: []string{"@scope/name"}}, false},
		{"excluded version beats included", model.CleanupCriteria{IncludedPackages: []string{"**"}, IncludedPackageVersions: []string{"2.*"}, ExcludedPackageVersions: []string{"*-beta.?"}}, false},
		{"excluded property beats included", model.CleanupCriteria{IncludedPackages: []string{"**"}, IncludedProperties: map[string][]string{"release": {"candidate"}}, ExcludedProperties: map[string][]string{"protected": {"true"}}}, false},
		{"every included property required", model.CleanupCriteria{IncludedPackages: []string{"**"}, IncludedProperties: map[string][]string{"release": {"candidate"}, "missing": {"value"}}}, false},
	} {
		t.Run(tc.name, func(t *testing.T) {
			if got := newVersionCriteria(tc.criteria, time.Now()).eligible(facts); got != tc.want {
				t.Fatalf("eligible = %t, want %t", got, tc.want)
			}
		})
	}
}

func TestUnitCleanupSelectionAgeBoundaryAndNeverDownloaded(t *testing.T) {
	now := time.Date(2026, 11, 20, 12, 0, 0, 123, time.UTC)
	base := model.CleanupCriteria{IncludedPackages: []string{"**"}, CreatedBeforeInDays: 1, LastDownloadedBeforeInDays: 1}
	for _, tc := range []struct {
		name                string
		created, downloaded time.Time
		want                bool
	}{
		{"exact boundary", now.Add(-24 * time.Hour), now.Add(-24 * time.Hour), true},
		{"created just too new", now.Add(-24*time.Hour + time.Nanosecond), time.Time{}, false},
		{"download just too new", now.Add(-48 * time.Hour), now.Add(-24*time.Hour + time.Nanosecond), false},
		{"never downloaded uses creation", now.Add(-24 * time.Hour), time.Time{}, true},
		{"absent timestamps are not old", time.Time{}, time.Time{}, false},
		{"download racing evaluation", now.Add(-48 * time.Hour), now, false},
	} {
		t.Run(tc.name, func(t *testing.T) {
			facts := versionFacts{Package: "name", Version: "2", Protected: false, Created: tc.created, LastDownloaded: tc.downloaded}
			if got := newVersionCriteria(base, now).eligible(facts); got != tc.want {
				t.Fatalf("eligible = %t, want %t", got, tc.want)
			}
		})
	}
	if selectionAge(now.Add(-time.Hour), now, math.MaxInt, math.MaxInt) {
		t.Fatal("overflowed policy age selected fresh content")
	}
	if !selectionAge(now.Add(-24*time.Hour), now, 1, 10) {
		t.Fatal("month form overrode explicit day form")
	}
}
