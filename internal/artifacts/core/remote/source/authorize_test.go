package source

import (
	"errors"
	"net/http"
	"reflect"
	"testing"

	accessapi "github.com/weaveplatform/artifactweave/internal/access/api"
	"github.com/weaveplatform/artifactweave/internal/access/api/accessapitest"
	"github.com/weaveplatform/artifactweave/internal/artifacts/api/repo"
	"github.com/weaveplatform/artifactweave/internal/kernel"
)

// pathRequest is a read of a.pkg in the remote repository up whose
// permission questions access answers.
func pathRequest(access accessapi.Decider) Request {
	return Request{
		Repo:   repo.Config{Key: "up", Class: repo.Remote},
		Access: access,
		Resource: Resource{
			Kind: ResourceKindArtifact, Path: "a.pkg", EscapedPath: "a.pkg", Method: http.MethodGet,
		},
	}
}

// TestUnitAuthorizePathRefusesWithoutDecider: a request that carries no
// decider is refused, whether Access was never set or holds what
// accessapi.DeciderFrom gives for a caller stored with a nil evaluator. That
// must be a nil interface: one holding a nil pointer would pass the check
// and fail on the first question.
func TestUnitAuthorizePathRefusesWithoutDecider(t *testing.T) {
	var evaluator *accessapitest.Decider
	caller := accessapi.WithCaller(t.Context(), kernel.Principal{Name: "reader"}, evaluator)
	stored, _ := accessapi.DeciderFrom(caller)
	for name, access := range map[string]accessapi.Decider{
		"unset":         nil,
		"nil evaluator": stored,
	} {
		t.Run(name, func(t *testing.T) {
			if err := authorizePath(pathRequest(access), "a.pkg"); !errors.Is(err, ErrDenied) {
				t.Fatalf("authorizePath = %v, want ErrDenied", err)
			}
		})
	}
}

// TestUnitAuthorizePathAsksTheDecider: any Decider answers, not only the
// evaluator. A file is asked of Allows and a folder of AllowsFolder, for read
// in the artifact section of the request's repository, and the answer
// decides: a decider that allows lets the path through, one that refuses
// denies it.
func TestUnitAuthorizePathAsksTheDecider(t *testing.T) {
	type question struct {
		folder bool
		rt     accessapi.ResourceType
		repo   accessapi.Repo
		path   string
		want   accessapi.Actions
	}
	for _, folder := range []bool{false, true} {
		for _, allow := range []bool{true, false} {
			var asked []question
			ask := func(folder bool) func(
				accessapi.ResourceType, accessapi.Repo, string, accessapi.Actions,
			) bool {
				return func(
					rt accessapi.ResourceType, r accessapi.Repo, path string, want accessapi.Actions,
				) bool {
					asked = append(asked, question{folder, rt, r, path, want})
					return allow
				}
			}
			q := pathRequest(&accessapitest.Decider{
				AllowsFunc: ask(false), AllowsFolderFunc: ask(true),
			})
			q.Resource.PermissionFolder = folder
			err := authorizePath(q, "a.pkg")
			if allow && err != nil || !allow && !errors.Is(err, ErrDenied) {
				t.Errorf("folder %v, allowed %v: authorizePath = %v", folder, allow, err)
			}
			want := []question{{
				folder, accessapi.ResourceArtifact,
				accessapi.Repo{Key: "up", Class: string(repo.Remote)},
				"a.pkg", accessapi.ActionRead,
			}}
			if !reflect.DeepEqual(asked, want) {
				t.Errorf("folder %v, allowed %v: asked %+v, want %+v", folder, allow, asked, want)
			}
		}
	}
}
