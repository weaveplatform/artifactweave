package accessapitest

import (
	"slices"

	accessapi "github.com/weaveplatform/artifactweave/internal/access/api"
	"github.com/weaveplatform/artifactweave/internal/kernel"
)

var _ accessapi.Decider = (*Decider)(nil)

// Decider is a Decider whose fields set its answers. The zero value is a
// subject that holds nothing: no administrator, in no group, seeing no
// repository and allowed nothing. A question whose function field is nil is
// answered with Admin, so an administrator is allowed whatever no function
// narrows, as the evaluator allows an administrator whose token scope does
// not narrow it. Allows and AllowsFolder refuse a question that asks for no
// action before they consult a field, as every Decider does.
//
// Use a *Decider: a Decider value holds functions, and an interface holding
// such a value panics when compared, as tests compare the decider a context
// carries. Its methods only read its fields, so once built it may be used
// from several goroutines, as long as its functions may.
type Decider struct {
	// Admin is IsAdmin's answer, and the answer to every question whose
	// function field is nil.
	Admin bool
	// GroupNames is what Groups returns, copied.
	GroupNames []string
	// IsRepoVisibleFunc answers IsRepoVisible.
	IsRepoVisibleFunc func(repo accessapi.Repo) bool
	// AllowsFunc answers Allows for a question that asks for some action.
	AllowsFunc func(
		rt accessapi.ResourceType,
		repo accessapi.Repo,
		path string,
		want accessapi.Actions,
	) bool
	// AllowsFolderFunc answers AllowsFolder for a question that asks for
	// some action.
	AllowsFolderFunc func(
		rt accessapi.ResourceType,
		repo accessapi.Repo,
		path string,
		want accessapi.Actions,
	) bool
	// SystemFunc answers System.
	SystemFunc func(area string, want kernel.ScopeActions) bool
}

// IsAdmin reports Admin.
func (d *Decider) IsAdmin() bool { return d.Admin }

// Groups returns a copy of GroupNames, which the caller may change.
func (d *Decider) Groups() []string { return slices.Clone(d.GroupNames) }

// IsRepoVisible answers with IsRepoVisibleFunc, or with Admin when it is
// nil.
func (d *Decider) IsRepoVisible(repo accessapi.Repo) bool {
	if d.IsRepoVisibleFunc != nil {
		return d.IsRepoVisibleFunc(repo)
	}
	return d.Admin
}

// Allows refuses a question that asks for no action, and answers any other
// with AllowsFunc, or with Admin when it is nil.
func (d *Decider) Allows(
	rt accessapi.ResourceType,
	repo accessapi.Repo,
	path string,
	want accessapi.Actions,
) bool {
	switch {
	case want == 0:
		return false
	case d.AllowsFunc != nil:
		return d.AllowsFunc(rt, repo, path, want)
	default:
		return d.Admin
	}
}

// AllowsFolder refuses a question that asks for no action, and answers any
// other with AllowsFolderFunc, or with Admin when it is nil. It does not fall
// back to AllowsFunc: the evaluator also admits a folder above a permitted
// path, which a test that asks about folders states itself.
func (d *Decider) AllowsFolder(
	rt accessapi.ResourceType,
	repo accessapi.Repo,
	path string,
	want accessapi.Actions,
) bool {
	switch {
	case want == 0:
		return false
	case d.AllowsFolderFunc != nil:
		return d.AllowsFolderFunc(rt, repo, path, want)
	default:
		return d.Admin
	}
}

// System answers with SystemFunc, or with Admin when it is nil.
func (d *Decider) System(area string, want kernel.ScopeActions) bool {
	if d.SystemFunc != nil {
		return d.SystemFunc(area, want)
	}
	return d.Admin
}
