package pkgindex

import (
	"context"
	"testing"

	"github.com/weaveplatform/artifactweave/internal/artifacts/api"
	"github.com/weaveplatform/artifactweave/internal/artifacts/api/repo"
	"github.com/weaveplatform/artifactweave/internal/artifacts/core/format"
	"github.com/weaveplatform/artifactweave/internal/artifacts/core/layout"
	"github.com/weaveplatform/artifactweave/internal/artifacts/core/model"
	"github.com/weaveplatform/artifactweave/internal/kernel"
)

type revisionLayout struct{ compiled *layout.Layout }

func (l *revisionLayout) Layout(context.Context, string) (*layout.Layout, error) {
	return l.compiled, nil
}

type revisionLayoutLead struct{ format.Handler }

func (revisionLayoutLead) CleanupLeadWithLayout(l *layout.Layout, _ model.PackageVersion) (format.CleanupLeadSelector, error) {
	pattern := l.Definition().DescriptorPathPattern
	return func(api.IndexFile) api.IndexLead { return api.IndexLead{Found: pattern != ""} }, nil
}

func TestUnitIndexVersionBindsCurrentDescriptorLayout(t *testing.T) {
	db, ref := indexReadFixture(t)
	original, err := layout.Lookup(repo.LayoutMaven2)
	if err != nil {
		t.Fatal(err)
	}
	layouts := &revisionLayout{compiled: original}
	reader := NewReader(db, func(repo.PackageType) format.Handler { return revisionLayoutLead{} }, layouts)
	before, err := reader.Version(t.Context(), ref, 0)
	if err != nil || before.MutationRevision == "" || before.LeadRevision == "" {
		t.Fatal("missing initial binding", err)
	}
	changed := original.Definition()
	changed.DescriptorPathPattern = "[orgPath]/[module]/[baseRev]/[module]-[baseRev].xml"
	layouts.compiled, err = layout.Compile(changed)
	if err != nil {
		t.Fatal(err)
	}
	after, err := reader.Version(t.Context(), before, 0)
	if err != nil || after.LeadRevision == before.LeadRevision || after.MutationRevision != before.MutationRevision {
		t.Fatal("layout change did not invalidate its independent lead binding", err)
	}
	files, _, err := reader.Files(t.Context(), before, nil, kernel.Page{Limit: 1})
	if err != nil || len(files) != 1 || files[0].Version.LeadRevision != after.LeadRevision || files[0].Version.MutationRevision != after.MutationRevision {
		t.Fatal("file page retained a caller's obsolete classifier binding", err)
	}
}
