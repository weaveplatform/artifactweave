package inmem

import (
	"cmp"
	"context"
	"encoding/json"
	"fmt"
	"slices"
	"strings"

	"github.com/weaveplatform/artifactweave/internal/artifacts/core/model"
	"github.com/weaveplatform/artifactweave/internal/artifacts/core/port"
)

func (p packages) CountVersions(ctx context.Context, packageID int64) (int, error) {
	var n int
	err := p.s.Do(ctx, func() error {
		n = len(p.sorted(packageID))
		return nil
	})
	return n, err
}

func (p packages) VersionNamesAt(
	ctx context.Context,
	packageID int64,
	newestFirst bool,
	offset, limit int,
) ([]string, error) {
	var out []string
	err := p.s.Do(ctx, func() error {
		vs := p.sorted(packageID)
		if newestFirst {
			slices.Reverse(vs)
		}
		for _, v := range window(vs, offset, limit) {
			out = append(out, v.Version)
		}
		return nil
	})
	return out, err
}

// window is the part of all at offset of limit.
func window[T any](all []T, offset, limit int) []T {
	start := min(max(offset, 0), len(all))
	return all[start : start+min(max(limit, 0), len(all)-start)]
}

// inRange returns r's packages by name, as the SQL stores select them;
// call it under the lock. A version's metadata that is not JSON fails it,
// as it fails the SQL stores' reads.
func (p packages) inRange(r port.PackageRange) ([]model.Package, error) {
	versions, err := p.rangeVersions(r)
	var out []model.Package
	for _, v := range versions {
		if len(out) == 0 || out[len(out)-1].ID != v.PackageID {
			out = append(out, p.t().Rows[v.PackageID])
		}
	}
	return out, err
}

// rangeVersions returns the versions of r's packages that r admits, by
// package name and then in Versions' order, as the SQL stores page them;
// call it under the lock.
func (p packages) rangeVersions(r port.PackageRange) ([]port.ReleaseName, error) {
	if strings.ContainsRune(r.Prefix, 0) {
		return nil, nil
	}
	rows := p.t().All(
		func(_ int64, v model.Package) bool {
			return v.RepoKey == r.RepoKey && strings.HasPrefix(v.Name, r.Prefix)
		},
		func(a, b model.Package) int { return cmp.Compare(a.Name, b.Name) },
	)
	var out []port.ReleaseName
	for _, pkg := range rows {
		for _, v := range p.sorted(pkg.ID) {
			ok, err := shown(r.Shown, v.Metadata)
			if err != nil {
				return nil, err
			}
			if ok {
				out = append(
					out,
					port.ReleaseName{PackageID: pkg.ID, Package: pkg.Name, Version: v.Version},
				)
			}
		}
	}
	return out, nil
}

// shown reports whether s admits a version whose metadata is metadata
// (port.NuGetShown).
func shown(s *port.NuGetShown, metadata []byte) (bool, error) {
	if s == nil {
		return true, nil
	}
	if metadata == nil {
		return false, nil
	}
	var m map[string]json.RawMessage
	if err := json.Unmarshal(metadata, &m); err != nil {
		return false, fmt.Errorf("read NuGet version metadata: %w", err)
	}
	var (
		path, version string
		semVer2       bool
	)
	_ = json.Unmarshal(m[port.NuGetPathKey], &path)
	_ = json.Unmarshal(m[port.NuGetVersionKey], &version)
	_ = json.Unmarshal(m[port.NuGetSemVer2Key], &semVer2)
	return path != "" && (s.Prerelease || !strings.Contains(version, "-")) &&
		(s.SemVer2 || !semVer2), nil
}

func (p packages) CountPackages(ctx context.Context, r port.PackageRange) (int, error) {
	var n int
	err := p.s.Do(ctx, func() error {
		rows, err := p.inRange(r)
		n = len(rows)
		return err
	})
	return n, err
}

func (p packages) PackagesAt(
	ctx context.Context,
	r port.PackageRange,
	offset, limit int,
) ([]model.Package, error) {
	var out []model.Package
	err := p.s.Do(ctx, func() error {
		rows, err := p.inRange(r)
		out = slices.Clone(window(rows, offset, limit))
		return err
	})
	return out, err
}

func (p packages) CountRangeVersions(ctx context.Context, r port.PackageRange) (int, error) {
	var n int
	err := p.s.Do(ctx, func() error {
		rows, err := p.rangeVersions(r)
		n = len(rows)
		return err
	})
	return n, err
}

func (p packages) RangeVersionsAt(
	ctx context.Context,
	r port.PackageRange,
	offset, limit int,
) ([]port.ReleaseName, error) {
	var out []port.ReleaseName
	err := p.s.Do(ctx, func() error {
		rows, err := p.rangeVersions(r)
		out = slices.Clone(window(rows, offset, limit))
		return err
	})
	return out, err
}

func (p packages) Candidates(
	ctx context.Context,
	packages []model.Package,
) ([]port.SearchCandidate, error) {
	var out []port.SearchCandidate
	err := p.s.Do(ctx, func() error {
		for _, pkg := range packages {
			if vs := p.sorted(pkg.ID); len(vs) > 0 {
				out = append(out, port.SearchCandidate{Package: pkg, Versions: vs})
			}
		}
		return nil
	})
	return out, err
}

func (p packages) TallyVersions(
	ctx context.Context,
	repoKey string,
	limit int,
) (port.VersionTally, error) {
	var t port.VersionTally
	err := p.s.Do(ctx, func() error {
		rows := p.t().All(func(_ int64, v model.Package) bool { return v.RepoKey == repoKey },
			func(a, b model.Package) int { return cmp.Compare(a.Name, b.Name) })
		for _, pkg := range window(rows, 0, limit) {
			n := len(p.sorted(pkg.ID))
			t.Packages++
			t.Versions += n
			t.MostVersions = max(t.MostVersions, n)
		}
		return nil
	})
	return t, err
}

func (p packages) ReleasesAt(
	ctx context.Context,
	repoKey string,
	offset, limit int,
) ([]port.ReleaseName, error) {
	var out []port.ReleaseName
	err := p.s.Do(ctx, func() error {
		rows := p.t().All(func(_ int64, v model.Package) bool { return v.RepoKey == repoKey },
			func(a, b model.Package) int { return cmp.Compare(a.Name, b.Name) })
		var all []port.ReleaseName
		for _, pkg := range rows {
			vs := p.sorted(pkg.ID)
			slices.Reverse(vs)
			for _, v := range vs {
				all = append(
					all,
					port.ReleaseName{PackageID: pkg.ID, Package: pkg.Name, Version: v.Version},
				)
			}
		}
		out = slices.Clone(window(all, offset, limit))
		return nil
	})
	return out, err
}
