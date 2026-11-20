package item

import (
	"context"
	"errors"
	"io"
	"strings"
	"testing"

	accessapi "github.com/weaveplatform/artifactweave/internal/access/api"
	"github.com/weaveplatform/artifactweave/internal/kernel"
	"github.com/weaveplatform/artifactweave/internal/platform/blob"
)

func TestUnitChecksumDeployRechecksSourceReadAtPublication(t *testing.T) {
	for _, tc := range []struct {
		name    string
		allowed bool
		fault   error
	}{
		{name: "current source read", allowed: true},
		{name: "source read revoked"},
		{name: "source policy unavailable", fault: errDown},
	} {
		t.Run(tc.name, func(t *testing.T) {
			w := newWorld(t)
			if _, err := w.deploy("source", "private content", kernel.Checksums{}); err != nil {
				t.Fatal(err)
			}
			checks := 0
			_, err := w.svc.DeployByChecksum(t.Context(), Deploy{
				Target: Target{Repo: w.loc, Path: "destination/file", CheckCurrent: func(context.Context, string, bool, accessapi.Actions) error {
					return nil
				}},
				Declared: sums("private content"),
				Reusable: func(ctx context.Context, _ blob.Blob) (bool, error) {
					checks++
					if checks == 1 {
						if w.db.InTx(ctx) {
							t.Fatal("initial lookup unexpectedly holds a metadata transaction")
						}
						return true, nil
					}
					if !w.db.InTx(ctx) {
						t.Fatal("source Read was not rechecked in publication transaction")
					}
					return tc.allowed, tc.fault
				},
			})
			want := tc.fault
			if !tc.allowed && want == nil {
				want = ErrNoBlob
			}
			if checks != 2 || !errors.Is(err, want) {
				t.Fatalf("source Read checks=%d err=%v want=%v", checks, err, want)
			}
			_, err = w.db.Items().Get(t.Context(), w.loc.Key, false, "destination/file")
			if tc.allowed && err != nil || !tc.allowed && !errors.Is(err, kernel.ErrNotFound) {
				t.Fatalf("destination after source Read check: %v", err)
			}
		})
	}
}

type publicationEOF func()

func (f publicationEOF) Read([]byte) (int, error) {
	f()
	return 0, io.EOF
}

func TestUnitDeployRechecksAuthorityAfterStreaming(t *testing.T) {
	w := newWorld(t)
	allowed, checked := true, false
	_, err := w.svc.Deploy(t.Context(), Deploy{
		Target: Target{Repo: w.loc, Path: "nested/file", CheckCurrent: func(ctx context.Context, path string, folder bool, want accessapi.Actions) error {
			checked = true
			if !w.db.InTx(ctx) || path != "nested/file" || folder || want != accessapi.ActionWrite {
				t.Fatal("publication check lost its transaction or actual target")
			}
			if !allowed {
				return kernel.ErrForbidden
			}
			return nil
		}},
		Body: io.MultiReader(strings.NewReader("payload"), publicationEOF(func() {
			if checked {
				t.Fatal("authority was only checked before streaming completed")
			}
			allowed = false
		})),
	})
	if !checked || !errors.Is(err, kernel.ErrForbidden) {
		t.Fatalf("revoked publication: checked=%v err=%v", checked, err)
	}
	for _, path := range []string{"nested", "nested/file"} {
		if _, err := w.db.Items().Get(t.Context(), w.loc.Key, false, path); !errors.Is(err, kernel.ErrNotFound) {
			t.Fatal("revoked upload published metadata", path, err)
		}
	}
	if _, err := w.db.Blobs().Get(t.Context(), sums("payload").SHA256); !errors.Is(err, kernel.ErrNotFound) {
		t.Fatalf("revoked upload published a blob reference: %v", err)
	}
}

func TestUnitOverwriteUsesCurrentAuthorityAndKeepsIdenticalException(t *testing.T) {
	for _, tc := range []struct {
		name, body string
		refusal    error
		want       error
	}{
		{name: "new grant overrides old refusal", body: "changed"},
		{name: "delete revoked for changed bytes", body: "changed", refusal: kernel.ErrForbidden},
		{name: "identical keeps Write-only exception", body: "original", refusal: kernel.ErrForbidden},
		{name: "anonymous identical keeps Write-only exception", body: "original", refusal: kernel.ErrUnauthenticated},
		{name: "ended credential cannot borrow exception", body: "original", refusal: kernel.ErrBadCredentials, want: kernel.ErrBadCredentials},
		{name: "failed policy cannot borrow exception", body: "original", refusal: errDown, want: errDown},
	} {
		t.Run(tc.name, func(t *testing.T) {
			w := newWorld(t)
			if _, err := w.deploy("file", "original", kernel.Checksums{}); err != nil {
				t.Fatal(err)
			}
			_, err := w.svc.Deploy(t.Context(), Deploy{
				Target: Target{Repo: w.loc, Path: "file", CheckCurrent: func(_ context.Context, _ string, _ bool, want accessapi.Actions) error {
					if want == accessapi.ActionDelete {
						if errors.Is(tc.refusal, kernel.ErrForbidden) || errors.Is(tc.refusal, kernel.ErrUnauthenticated) {
							return errors.Join(ErrPermissionDenied, tc.refusal)
						}
						return tc.refusal
					}
					return nil
				}},
				Body: strings.NewReader(tc.body), Declared: sums(tc.body), OverwriteIdentical: true,
				CanOverwritePath: func(string) bool { return false },
			})
			var overwrite *OverwriteDeniedError
			denied := tc.body == "changed" && errors.Is(tc.refusal, kernel.ErrForbidden)
			if denied && !errors.As(err, &overwrite) || !denied && !errors.Is(err, tc.want) {
				t.Fatalf("overwrite authority: %v", err)
			}
			stored, readErr := w.db.Items().Get(t.Context(), w.loc.Key, false, "file")
			if readErr != nil {
				t.Fatal(readErr)
			}
			body := tc.body
			if err != nil {
				body = "original"
			}
			if stored.Actual.SHA256 != sums(body).SHA256 {
				t.Fatal("refused publication replaced the original content")
			}
		})
	}
}
