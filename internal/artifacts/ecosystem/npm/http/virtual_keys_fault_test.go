package npmhttp_test

import (
	"context"
	"fmt"
	"net/http"
	"strings"
	"testing"
	"time"

	"github.com/weaveplatform/artifactweave/internal/access/api/accessapitest"
	"github.com/weaveplatform/artifactweave/internal/artifacts/core/format"
	"github.com/weaveplatform/artifactweave/internal/artifacts/core/remote/source"
	"github.com/weaveplatform/artifactweave/internal/kernel"
)

func TestAccNPMVirtualKeysSourceFencesPreserveFailures(t *testing.T) {
	for _, tc := range []struct {
		name    string
		failure error
		status  int
	}{
		{"caller-denied", source.ErrDenied, 200},
		{"missing", kernel.ErrNotFound, 200},
		{"offline", source.ErrAssumedOffline, 200},
		{"store", fmt.Errorf("keys cache: %w", format.ErrStorageFault), 500},
		{"unavailable", source.ErrUnavailable, 502},
		{"deadline", context.DeadlineExceeded, 504},
		{"superseded", source.ErrSuperseded, 503},
		{"upstream-forbidden", &source.HTTPError{Status: 403}, 403},
	} {
		t.Run(tc.name, func(t *testing.T) {
			f, _ := npmVirtualKeysFixture(t)
			original := f.svc.HTTP
			checks := 0
			f.svc.HTTP = virtualBackend(func(ctx context.Context, q source.Request, p source.Permit) (source.Result, error) {
				result, err := original.Read(ctx, q, p)
				if err == nil {
					previous := result.Revalidate
					result.Revalidate = func(ctx context.Context) error {
						checks++
						if checks >= 2 {
							return tc.failure
						}
						if previous != nil {
							return previous(ctx)
						}
						return nil
					}
				}
				return result, err
			})
			response := f.do("GET", virtualNPMKeysPath, "reader")
			wantStatus(t, response, tc.status)
			if checks < 2 {
				t.Fatal("final member fence was not exercised", checks)
			}
			if tc.status == 200 && strings.Contains(response.Body.String(), `"keyid":"remote"`) {
				t.Fatal("refused member disclosed", response.Body.String())
			}
			if tc.name == "superseded" && response.Header().Get("Retry-After") != "1" {
				t.Fatal(response.Header())
			}
			if tc.name == "upstream-forbidden" && response.Header().Get("WWW-Authenticate") != "" {
				t.Fatal(response.Header())
			}
		})
	}
}

func TestAccNPMVirtualKeysDoNotHideKeyStoreFault(t *testing.T) {
	f, keys := npmVirtualKeysFixture(t)
	keys.Keys = &accessapitest.Keys{Err: fmt.Errorf("keypair database: %w", kernel.ErrStore)}
	wantStatus(t, f.do("GET", virtualNPMKeysPath, "reader"), http.StatusInternalServerError)
	if keys.calls != 1 || len(f.up.Requests()) != 0 {
		t.Fatal("fault was passed over", keys.calls, f.up.Requests())
	}
}

func TestAccNPMVirtualKeysFinalMembershipAndRotationAreLive(t *testing.T) {
	for _, change := range []string{"removed", "local-rotation", "root-pattern", "nested-pattern"} {
		t.Run(change, func(t *testing.T) {
			f, _ := npmVirtualKeysFixture(t)
			original := f.svc.HTTP
			changed := false
			f.svc.HTTP = virtualBackend(func(ctx context.Context, q source.Request, p source.Permit) (source.Result, error) {
				result, err := original.Read(ctx, q, p)
				if err == nil && !changed {
					changed = true
					key := "npm-all"
					if change == "local-rotation" {
						key = "npm-local"
					}
					if change == "nested-pattern" {
						key = "inner"
					}
					c, e := f.db.Repos().Get(ctx, key)
					if e != nil {
						return result, e
					}
					switch change {
					case "removed":
						c.Repositories = []string{"inner"}
					case "local-rotation":
						c.PrimaryKeyPairRef = ""
					default:
						c.ExcludesPattern = new(".npm/keys.json")
					}
					if e = f.db.Repos().Update(ctx, c); e != nil {
						return result, e
					}
				}
				return result, err
			})
			response := f.do("GET", virtualNPMKeysPath, "reader")
			want := 200
			if change == "root-pattern" {
				want = 404
			}
			wantStatus(t, response, want)
			if !changed {
				t.Fatal("source callback was not reached")
			}
			if change == "removed" && strings.Contains(response.Body.String(), `"keyid":"remote"`) {
				t.Fatal(response.Body.String())
			}
			if change == "local-rotation" || change == "nested-pattern" {
				if !strings.Contains(response.Body.String(), `"keyid":"remote"`) || strings.Count(response.Body.String(), `"keyid"`) != 1 {
					t.Fatal(response.Body.String())
				}
			}
		})
	}
}

func TestAccNPMVirtualKeysReuseOnlyRemoteRetrievalCache(t *testing.T) {
	f, _ := npmVirtualKeysFixture(t)
	first := f.do("GET", virtualNPMKeysPath, "reader")
	wantStatus(t, first, 200)
	count := f.up.Count("GET", upstreamKeys)
	f.setKeys(`{"keys":[{"keyid":"rotated","key":"new"}]}`, "application/json")
	warm := f.do("GET", virtualNPMKeysPath, "reader")
	wantStatus(t, warm, 200)
	if !strings.Contains(warm.Body.String(), `"keyid":"remote"`) || f.up.Count("GET", upstreamKeys) != count {
		t.Fatal("raw freshness changed", warm.Body.String())
	}
	f.clock.Advance(6 * time.Second)
	expired := f.do("GET", virtualNPMKeysPath, "reader")
	wantStatus(t, expired, 200)
	if !strings.Contains(expired.Body.String(), `"keyid":"rotated"`) || f.up.Count("GET", upstreamKeys) != count+1 {
		t.Fatal("virtual cache hid source refresh", expired.Body.String())
	}
	// Negative retrieval remains remote-owned while signing locals still contribute.
	f.clock.Advance(6 * time.Second)
	f.up.Delete(upstreamKeys)
	wantStatus(t, f.do("GET", virtualNPMKeysPath, "reader"), 200)
	missed := f.up.Count("GET", upstreamKeys)
	f.setKeys(`{"keys":[{"keyid":"after-miss","key":"new"}]}`, "application/json")
	response := f.do("GET", virtualNPMKeysPath, "reader")
	wantStatus(t, response, 200)
	if strings.Contains(response.Body.String(), "after-miss") || f.up.Count("GET", upstreamKeys) != missed {
		t.Fatal("negative cache bypassed", response.Body.String())
	}
}

func TestAccNPMVirtualKeysIgnoreVirtualSigningReferences(t *testing.T) {
	f, keys := npmVirtualKeysFixture(t)
	c, err := f.db.Repos().Get(t.Context(), "npm-local")
	if err != nil {
		t.Fatal(err)
	}
	c.PrimaryKeyPairRef = ""
	if err := f.db.Repos().Update(t.Context(), c); err != nil {
		t.Fatal(err)
	}
	root, err := f.db.Repos().Get(t.Context(), "npm-all")
	if err != nil {
		t.Fatal(err)
	}
	root.PrimaryKeyPairRef = "not-a-member-key"
	if err := f.db.Repos().Update(t.Context(), root); err != nil {
		t.Fatal(err)
	}
	keys.Keys = &accessapitest.Keys{Err: fmt.Errorf("virtual pair must never be consulted: %w", kernel.ErrStore)}
	f.up.Delete(upstreamKeys)
	wantStatus(t, f.do("GET", virtualNPMKeysPath, "reader"), http.StatusNotFound)
	if keys.calls != 0 {
		t.Fatal("virtual configured keypair was used", keys.calls)
	}
}
