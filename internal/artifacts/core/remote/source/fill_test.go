package source_test

import (
	"context"
	"errors"
	"testing"

	accessapi "github.com/weaveplatform/artifactweave/internal/access/api"
	"github.com/weaveplatform/artifactweave/internal/access/api/accessapitest"
	"github.com/weaveplatform/artifactweave/internal/artifacts/core/remote/source"
	"github.com/weaveplatform/artifactweave/internal/kernel"
)

// TestUnitAuthorizeFillAsksDeployOnTheCachePath (#1587): filling a
// remote's cache needs Deploy/Cache at the path 7.x keeps the file, the
// policy path when the resource names one, or Annotate for a
// "<file>:<metadata>" path; the refusal carries 7.x's reason for the
// remote's cache and is a miss to every route but the item route.
func TestUnitAuthorizeFillAsksDeployOnTheCachePath(t *testing.T) {
	asked := map[string]accessapi.Actions{}
	grant := accessapi.ActionRead
	q := request(t, "")
	q.Access = &accessapitest.Decider{AllowsFunc: func(
		_ accessapi.ResourceType, r accessapi.Repo, path string, want accessapi.Actions,
	) bool {
		if r.Key != "up" || r.Class != "remote" {
			t.Fatalf("asked about %+v", r)
		}
		asked[path] = want
		return grant.Has(want)
	}}
	q.Resource.Path = "pkg/a.tgz"
	err := source.AuthorizeFill(q)
	var denied *source.CacheFillDenied
	if !errors.As(err, &denied) || !errors.Is(err, kernel.ErrNotFound) ||
		denied.Message() != "User reader is not permitted to deploy 'pkg/a.tgz' into 'up-cache:pkg/a.tgz'." ||
		err.Error() != denied.Message() || asked["pkg/a.tgz"] != accessapi.ActionWrite {
		t.Fatalf("Read alone: %v, asked %v", err, asked)
	}

	q.Resource.PolicyPath = ".npm/a/package.json"
	if err = source.AuthorizeFill(q); !errors.As(err, &denied) ||
		denied.Path != ".npm/a/package.json" || asked[".npm/a/package.json"] != accessapi.ActionWrite {
		t.Fatalf("policy path: %v, asked %v", err, asked)
	}

	q.Resource.PolicyPath = "a/b.jar:properties"
	if err = source.AuthorizeFill(q); !errors.As(err, &denied) ||
		denied.Message() != "User reader is not permitted to annotate 'a/b.jar:properties' on 'up-cache:a/b.jar:properties'." ||
		asked["a/b.jar:properties"] != accessapi.ActionAnnotate {
		t.Fatalf("metadata path: %v, asked %v", err, asked)
	}

	grant |= accessapi.ActionWrite | accessapi.ActionAnnotate
	for _, p := range []string{"", "a/b.jar:properties"} {
		q.Resource.PolicyPath = p
		if err = source.AuthorizeFill(q); err != nil {
			t.Fatalf("granted %q: %v", p, err)
		}
	}
	q.Access = nil
	if err = source.AuthorizeFill(q); !errors.As(err, &denied) {
		t.Fatalf("no decider: %v", err)
	}
}

// TestUnitMetadataPath: 7.x's NamingUtils.isMetadata, which asks Annotate
// in place of Deploy, names only the "<resource>:<metadata>" form; a plain
// maven-metadata.xml is a file like any other.
func TestUnitMetadataPath(t *testing.T) {
	for p, want := range map[string]bool{
		"a/b.jar:properties":                        true,
		"a/b.jar:statistics":                        true,
		"g/a/1.0-SNAPSHOT/a.pom:maven-metadata.xml": true,
		"g/a/maven-metadata.xml":                    false,
		"a/b.jar":                                   false,
		"a:b/c.jar":                                 false,
		"a/b:other":                                 false,
	} {
		if got := source.MetadataPath(p); got != want {
			t.Errorf("MetadataPath(%q) = %v, want %v", p, got, want)
		}
	}
}

// TestUnitReadPassesFillAndOfflineAnswers (#1587, #1601): the source
// service hands a backend's refused fill on as it is, for the item route
// to word, and an assumed-offline miss as ErrAssumedOffline, which is
// still a miss.
func TestUnitReadPassesFillAndOfflineAnswers(t *testing.T) {
	denied := &source.CacheFillDenied{User: "reader", Repo: "up", Path: "a.pkg"}
	for name, failure := range map[string]error{
		"fill":    denied,
		"offline": source.ErrAssumedOffline,
	} {
		s := &source.Service{Resolve: identity, HTTP: backend(
			func(context.Context, source.Request, source.Permit) (source.Result, error) {
				return source.Result{}, failure
			},
		)}
		_, err := s.Read(t.Context(), request(t, ""))
		if !errors.Is(err, failure) || !errors.Is(err, kernel.ErrNotFound) {
			t.Fatalf("%s: %v, want %v", name, err, failure)
		}
		if name == "fill" {
			var got *source.CacheFillDenied
			if !errors.As(err, &got) || got != denied {
				t.Fatalf("fill: %v lost its reason", err)
			}
		}
	}
}
