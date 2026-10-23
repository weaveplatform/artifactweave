package luarocks

import (
	"context"
	"errors"
	"strings"
	"testing"

	"github.com/weaveplatform/artifactweave/internal/source"
)

func TestRemoteRockRechecksManifestAndFileAuthorityAtDelivery(t *testing.T) {
	denied := errors.New("revoked")
	for _, tc := range []struct {
		name string
		fail string
		want string
	}{
		{"prior", "prior", "revoked"},
		{"manifest", "manifest", "luarocks manifest authority"},
		{"file", "file", "luarocks package authority"},
		{"allowed", "", ""},
	} {
		t.Run(tc.name, func(t *testing.T) {
			var called []string
			result := source.Result{Revalidate: func(context.Context) error {
				called = append(called, "prior")
				if tc.fail == "prior" {
					return denied
				}
				return nil
			}}
			metadata := source.Result{
				Revalidate: func(context.Context) error {
					called = append(called, "manifest")
					if tc.fail == "manifest" {
						return denied
					}
					return nil
				},
				CheckRead: func(_ context.Context, path string) error {
					called = append(called, path)
					if tc.fail == "file" {
						return denied
					}
					return nil
				},
			}
			fenceDisclosures(&result, metadata, []string{"visible-1.0-1.rockspec"})
			err := result.Revalidate(t.Context())
			if tc.want == "" && err != nil || tc.want != "" &&
				(err == nil || !strings.Contains(err.Error(), tc.want)) {
				t.Fatalf("revalidation %v, calls %v", err, called)
			}
			if len(called) == 0 || called[0] != "prior" {
				t.Fatalf("prior authority was not first: %v", called)
			}
		})
	}
}
