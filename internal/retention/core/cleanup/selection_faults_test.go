package cleanup_test

import (
	"context"
	"errors"
	"strconv"
	"testing"

	artifactsapi "github.com/weaveplatform/artifactweave/internal/artifacts/api"
	"github.com/weaveplatform/artifactweave/internal/artifacts/api/repo"
	"github.com/weaveplatform/artifactweave/internal/kernel"
	"github.com/weaveplatform/artifactweave/internal/retention/core/cleanup"
)

// This owner-API fixture isolates failure classification from storage. Actual
// index snapshots, mutation races and deletion effects run in the server suite.
type selectionBoundaryIndex struct {
	artifactsapi.PackageIndex
	version    artifactsapi.IndexVersion
	file       artifactsapi.IndexFile
	operation  string
	nth, calls int
}

func (i *selectionBoundaryIndex) fault(operation string) error {
	if operation == i.operation {
		i.calls++
		if i.calls == i.nth {
			return errCleanupBoundary
		}
	}
	return nil
}

func (i *selectionBoundaryIndex) Repositories(context.Context, artifactsapi.IndexRepositoryQuery, kernel.Page) ([]artifactsapi.IndexRepository, string, error) {
	return []artifactsapi.IndexRepository{i.version.Package.Repository}, "", i.fault("Repositories")
}

func (i *selectionBoundaryIndex) Packages(context.Context, artifactsapi.IndexRepository, kernel.Page) ([]artifactsapi.IndexPackage, string, error) {
	return []artifactsapi.IndexPackage{i.version.Package}, "", i.fault("Packages")
}

func (i *selectionBoundaryIndex) Versions(context.Context, artifactsapi.IndexPackage, int, kernel.Page) ([]artifactsapi.IndexVersion, string, error) {
	return []artifactsapi.IndexVersion{i.version}, "", i.fault("Versions")
}

func (i *selectionBoundaryIndex) Version(context.Context, artifactsapi.IndexVersion, int) (artifactsapi.IndexVersion, error) {
	return i.version, i.fault("Version")
}

func (i *selectionBoundaryIndex) Files(context.Context, artifactsapi.IndexVersion, map[string][]string, kernel.Page) ([]artifactsapi.IndexFile, string, error) {
	return []artifactsapi.IndexFile{i.file}, "", i.fault("Files")
}

func TestUnitCleanupSelectionFailuresNeverBecomeSuccessfulEmptyPages(t *testing.T) {
	for _, tc := range []struct {
		operation string
		nth       int
	}{
		{operation: "Repositories", nth: 1},
		{operation: "Packages", nth: 1},
		{operation: "Versions", nth: 1},
		{operation: "Version", nth: 1},
		{operation: "Version", nth: 2},
		{operation: "Files", nth: 1},
		{operation: "Files", nth: 2},
		{operation: "Files", nth: 3},
	} {
		t.Run(tc.operation+strconv.Itoa(tc.nth), func(t *testing.T) {
			s, _ := world(t, "*")
			ctx := t.Context()
			p, err := s.Create(ctx, "selection-boundary", input("selection-boundary", "alpha"))
			must(t, err)
			index := &selectionBoundaryIndex{
				operation: tc.operation,
				nth:       tc.nth,
				version: artifactsapi.IndexVersion{
					ID:               1,
					Version:          "1",
					MutationRevision: "one",
					LeadRevision:     "native-npm",
					Package: artifactsapi.IndexPackage{
						ID:         1,
						Name:       "@scope/eligible",
						Repository: artifactsapi.IndexRepository{Key: "alpha-npm", Project: "alpha", Class: repo.Local, PackageType: repo.NPM},
					},
				},
			}
			index.file = artifactsapi.IndexFile{
				ID:         1,
				Version:    index.version,
				Created:    s.Now().AddDate(-1, 0, 0),
				Path:       "eligible.tgz",
				Properties: map[string][]string{"retention": {"clean"}},
				Lead:       &artifactsapi.IndexLead{Found: true, Single: true},
			}
			s.PackageIndex = index
			selection, err := s.Select(ctx, p.Key)
			must(t, err)
			switch tc.operation {
			case "Repositories":
				rows, next, err := selection.Packages(ctx, index.version.Package.Repository, kernel.Page{})
				if !errors.Is(err, errCleanupBoundary) || len(rows) != 0 || next != "" {
					t.Fatal("repository failure disclosed a successful page", err)
				}
			case "Packages":
				rows, next, err := selection.Packages(ctx, index.version.Package.Repository, kernel.Page{})
				if !errors.Is(err, errCleanupBoundary) || len(rows) != 0 || next != "" {
					t.Fatal("package failure disclosed a successful page", err)
				}
			case "Versions":
				rows, next, err := selection.Versions(ctx, index.version.Package, kernel.Page{})
				if !errors.Is(err, errCleanupBoundary) || len(rows) != 0 || next != "" {
					t.Fatal("version failure disclosed a successful page", err)
				}
			default:
				page, err := selection.Candidates(ctx, index.version, kernel.Page{})
				if !errors.Is(err, errCleanupBoundary) || len(page.Items) != 0 {
					t.Fatal("candidate read failure became an empty success", err)
				}
			}
			if index.calls != tc.nth {
				t.Fatal("test missed its index boundary", index.calls)
			}
			index.operation = ""
			page, err := selection.Candidates(ctx, index.version, kernel.Page{})
			must(t, err)
			if page.Reason != cleanup.ReasonSelected || len(page.Items) != 1 {
				t.Fatal("restored index did not recover the eligible candidate", page.Reason)
			}
			_, err = selection.Candidates(ctx, index.version, kernel.Page{After: "malformed!"})
			if !errors.Is(err, kernel.ErrInvalidCursor) {
				t.Fatal("invalid candidate page was accepted", err)
			}
		})
	}
}
