package descriptor

import (
	"context"
	"errors"
	"slices"
	"strings"
	"testing"

	"github.com/weaveplatform/artifactweave/pkg/store/inmem"
	"github.com/weaveplatform/artifactweave/pkg/types"
)

// minSize reads the minimum redirect size the store holds.
func minSize(t *testing.T, db *inmem.Store) (int64, bool) {
	t.Helper()
	n, ok, err := DownloadRedirectMinSize(context.Background(), db.Configs())
	if err != nil {
		t.Fatal(err)
	}
	return n, ok
}

// downloadRedirectConfig.fileMinimumSize is applied in MB, as 7.x's General
// settings store it, and an apply that changes nothing reports it
// unchanged. Until a descriptor sets it there is no value, so the
// installation's download.redirect.min_size applies.
func TestApplyDownloadRedirectConfig(t *testing.T) {
	db := inmem.New()
	if _, ok := minSize(t, db); ok {
		t.Fatal("a value before any descriptor")
	}
	const doc = "downloadRedirectConfig:\n  fileMinimumSize: 5\n"
	res := mustApply(t, db, doc)
	if !slices.Equal(res.Created, []string{"downloadRedirectConfig.default"}) ||
		res.Changes != 1 || res.Merged() != "1 changes to config merged successfully" {
		t.Fatalf("%+v", res)
	}
	if n, ok := minSize(t, db); !ok || n != 5<<20 {
		t.Fatalf("%d %v", n, ok)
	}
	if res := mustApply(t, db, doc); len(res.Unchanged) != 1 || len(res.Updated) != 0 {
		t.Fatalf("again: %+v", res)
	}
	// 0 redirects every file; an empty mapping keeps what is stored.
	mustApply(t, db, "downloadRedirectConfig:\n  fileMinimumSize: 0\n")
	mustApply(t, db, "downloadRedirectConfig: {}\n")
	if n, ok := minSize(t, db); !ok || n != 0 {
		t.Fatalf("%d %v", n, ok)
	}
}

func TestParseRefusesBadDownloadRedirectConfig(t *testing.T) {
	for doc, want := range map[string]string{
		"downloadRedirectConfig: null\n":                               "downloadRedirectConfig cannot be null",
		"downloadRedirectConfig: 1\n":                                  "downloadRedirectConfig must be a mapping",
		"downloadRedirectConfig:\n  enabled: true\n":                   "downloadRedirectConfig: unknown field enabled",
		"downloadRedirectConfig:\n  fileMinimumSize: null\n":           "fileMinimumSize cannot be null",
		"downloadRedirectConfig:\n  fileMinimumSize: -1\n":             "must be a whole number of MB",
		"downloadRedirectConfig:\n  fileMinimumSize: 1.5\n":            "must be a whole number of MB",
		"downloadRedirectConfig:\n  fileMinimumSize: \"1\"\n":          "must be a whole number of MB",
		"downloadRedirectConfig:\n  fileMinimumSize: 1e300\n":          "must be a whole number of MB",
		"downloadRedirectConfig:\n  fileMinimumSize: 9e15\n":           "must be a whole number of MB",
		"downloadRedirectConfig:\n  fileMinimumSize: 99999999999999\n": "must be a whole number of MB",
	} {
		_, err := Parse([]byte(doc), noEnv)
		if !errors.Is(err, ErrInvalid) || !strings.Contains(err.Error(), want) {
			t.Errorf("%q: %v", doc, err)
		}
	}
}

// A stored document the store cannot return, or that is not the section's
// JSON, is an error; one without the field has no value.
func TestDownloadRedirectMinSizeStoreErrors(t *testing.T) {
	db := inmem.New()
	ctx := context.Background()
	_, _, err := DownloadRedirectMinSize(ctx, faulty{db, "config get"}.Configs())
	if !errors.Is(err, errBoom) {
		t.Fatalf("%v", err)
	}
	put := func(doc string) {
		t.Helper()
		if err := db.Configs().Put(ctx, types.ConfigDocument{
			Kind: downloadRedirectKind, Key: downloadRedirectKey, Document: []byte(doc),
		}); err != nil {
			t.Fatal(err)
		}
	}
	put(`{}`)
	if _, ok := minSize(t, db); ok {
		t.Fatal("a value from an empty document")
	}
	put(`{"fileMinimumSize":"five"}`)
	if _, _, err := DownloadRedirectMinSize(ctx, db.Configs()); err == nil {
		t.Fatal("no error for a document that is not the section's")
	}
}
