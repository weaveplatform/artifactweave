package cleanup

import (
	"math/big"
	"regexp"
	"slices"
	"strings"
	"time"

	"github.com/weaveplatform/artifactweave/internal/kernel"
	"github.com/weaveplatform/artifactweave/internal/retention/core/model"
)

// versionFacts is the bounded index projection used after repository/project
// admission. Protected comes from the requested keep-count cutoff under the
// package index's comparator and tie order in the same read. Inclusion filters
// must never change that protection decision.
type versionFacts struct {
	Package, Version        string
	Protected               bool
	Created, LastDownloaded time.Time
	LeadProperties          map[string][]string
}

type versionCriteria struct {
	criteria                             model.CleanupCriteria
	now                                  time.Time
	packages, versions, excludedVersions []*regexp.Regexp
}

func newVersionCriteria(c model.CleanupCriteria, now time.Time) versionCriteria {
	c = kernel.Clone(c)
	return versionCriteria{
		criteria: c, now: now,
		packages:         compileSelectionPatterns(c.IncludedPackages),
		versions:         compileSelectionPatterns(c.IncludedPackageVersions),
		excludedVersions: compileSelectionPatterns(c.ExcludedPackageVersions),
	}
}

func compileSelectionPatterns(patterns []string) []*regexp.Regexp {
	out := make([]*regexp.Regexp, 0, len(patterns))
	for _, pattern := range patterns {
		// Package names are opaque ecosystem names, including npm's slash.
		// These wildcards are not filesystem/Ant path conditions.
		quoted := regexp.QuoteMeta(pattern)
		quoted = strings.ReplaceAll(quoted, `\*`, `.*`)
		quoted = strings.ReplaceAll(quoted, `\?`, `.`)
		out = append(out, regexp.MustCompile("^(?:"+quoted+")$"))
	}
	return out
}

func matchesSelection(patterns []*regexp.Regexp, value string) bool {
	return slices.ContainsFunc(patterns, func(pattern *regexp.Regexp) bool { return pattern.MatchString(value) })
}

func (c versionCriteria) eligible(v versionFacts) bool {
	if v.Protected || slices.Contains(c.criteria.ExcludedPackages, v.Package) ||
		!matchesSelection(c.packages, v.Package) || matchesSelection(c.excludedVersions, v.Version) {
		return false
	}
	if len(c.versions) > 0 && !matchesSelection(c.versions, v.Version) {
		return false
	}
	for key, excluded := range c.criteria.ExcludedProperties {
		if slices.ContainsFunc(v.LeadProperties[key], func(value string) bool { return slices.Contains(excluded, value) }) {
			return false
		}
	}
	for key, included := range c.criteria.IncludedProperties {
		if !slices.ContainsFunc(v.LeadProperties[key], func(value string) bool { return slices.Contains(included, value) }) {
			return false
		}
	}
	if !selectionAge(v.Created, c.now, c.criteria.CreatedBeforeInDays, c.criteria.CreatedBeforeInMonths) {
		return false
	}
	downloaded := v.LastDownloaded
	if downloaded.IsZero() {
		downloaded = v.Created
	}
	return selectionAge(downloaded, c.now, c.criteria.LastDownloadedBeforeInDays, c.criteria.LastDownloadedBeforeInMonths)
}

func selectionAge(at, now time.Time, days, months int) bool {
	if days == 0 && months == 0 {
		return true
	}
	if at.IsZero() {
		return false
	}
	// Policy counts can exceed time.Duration's range. Comparing whole seconds
	// before the fractional boundary prevents an enormous age from wrapping
	// into a recent cutoff and selecting fresh content.
	required := big.NewInt(int64(days))
	if days == 0 {
		required.Mul(big.NewInt(int64(months)), big.NewInt(30))
	}
	required.Mul(required, big.NewInt(24*60*60))
	elapsed := new(big.Int).Sub(big.NewInt(now.Unix()), big.NewInt(at.Unix()))
	comparison := elapsed.Cmp(required)
	return comparison > 0 || comparison == 0 && now.Nanosecond() >= at.Nanosecond()
}
