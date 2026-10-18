package build

import (
	"context"
	"errors"
	"fmt"
	"slices"
	"strings"
	"testing"
	"time"

	"github.com/weaveplatform/artifactweave/pkg/types"
)

// entry is a build-info artifact or dependency of content, with id, name and
// path as given (empty leaves them out).
func entry(id, name, path, content string) string {
	var fields []string
	for _, f := range [][2]string{{"id", id}, {"name", name}, {"path", path}} {
		if f[1] != "" {
			fields = append(fields, fmt.Sprintf("%q:%q", f[0], f[1]))
		}
	}
	if content != "" {
		fields = append(fields, fmt.Sprintf(`"sha1":%q`, sha1Hex(content)))
	}
	return "{" + strings.Join(fields, ",") + "}"
}

func moduleOf(id string, artifacts, dependencies []string) string {
	return fmt.Sprintf(`{"id":%q,"artifacts":[%s],"dependencies":[%s]}`,
		id, strings.Join(artifacts, ","), strings.Join(dependencies, ","))
}

// TestDuplicateEntries refuses, by default, a publish whose module lists
// one file twice, as Artifactory 7.x does (RTDEV-47671), and accepts what
// only looks alike.
func TestDuplicateEntries(t *testing.T) {
	a := entry("", "a.bin", "app/a.bin", "a")
	upper := strings.Replace(a, sha1Hex("a"), strings.ToUpper(sha1Hex("a")), 1)
	d := entry("org:d:1", "", "", "d")
	for _, tc := range []struct {
		name    string
		modules []string
		want    string // the refusal; empty is accepted
	}{
		{
			"artifact twice",
			[]string{moduleOf("m", []string{a, a}, nil)},
			"the build-info module 'm' lists the artifact 'a.bin' more than once",
		},
		{
			"checksums in another case",
			[]string{moduleOf("m", []string{a, upper}, nil)},
			"lists the artifact 'a.bin'",
		},
		{
			"dependency twice",
			[]string{moduleOf("m", nil, []string{d, d})},
			"the build-info module 'm' lists the dependency 'org:d:1' more than once",
		},
		{"in the second module", []string{
			moduleOf("m1", []string{a}, nil), moduleOf("m2", nil, []string{d, d}),
		}, "module 'm2'"},
		{"a nameless artifact is named by its path", []string{moduleOf("m", []string{
			entry("", "", "app/a.bin", "a"), entry("", "", "app/a.bin", "a"),
		}, nil)}, "lists the artifact 'app/a.bin'"},
		// One build may hold several files with one checksum (7.161.15).
		{"one checksum at another path", []string{moduleOf("m", []string{
			a, entry("", "a.bin", "copy/a.bin", "a"),
		}, nil)}, ""},
		{"one checksum under another name", []string{moduleOf("m", []string{
			a, entry("", "b.bin", "app/b.bin", "a"),
		}, nil)}, ""},
		{"one name with other content", []string{moduleOf("m", []string{
			a, entry("", "a.bin", "app/a.bin", "b"),
		}, nil)}, ""},
		{"one checksum under another id", []string{moduleOf("m", nil, []string{
			d, entry("org:e:1", "", "", "d"),
		})}, ""},
		// A multi-module build lists a shared dependency in each module.
		{"a dependency of two modules", []string{
			moduleOf("m1", nil, []string{d}), moduleOf("m2", nil, []string{d}),
		}, ""},
		{"an artifact the module depends on", []string{moduleOf("m", []string{a}, []string{
			entry("a.bin", "", "", "a"),
		})}, ""},
		{"entries without checksums", []string{moduleOf("m", []string{
			entry("", "dir", "app/dir", ""), entry("", "dir", "app/dir", ""),
		}, nil)}, ""},
	} {
		t.Run(tc.name, func(t *testing.T) {
			w := newWorld(t)
			ctx := context.Background()
			d := doc("app", "1", started0, tc.modules...)
			_, err := w.svc.Publish(ctx, "", []byte(d), "admin", everyone)
			switch {
			case tc.want == "" && err != nil:
				t.Fatalf("refused: %v", err)
			case tc.want != "" && (!errors.Is(err, ErrInvalid) || !strings.Contains(err.Error(), tc.want)):
				t.Fatalf("got %v, want %q", err, tc.want)
			case tc.want == "":
				return
			}
			if _, err := w.db.Builds().Get(ctx, "", "app", "1", time.Time{}); !errors.Is(
				err, ErrNotFound) {
				t.Errorf("a refused publish stored its run: %v", err)
			}
			// build.block_duplicate_entries false accepts it.
			w.svc.AllowDuplicateEntries = true
			w.publish(d)
		})
	}
}

// TestAppendDuplicateEntries checks the modules an append adds, and not
// the run's own, which may have been published while duplicates were
// allowed.
func TestAppendDuplicateEntries(t *testing.T) {
	w := newWorld(t)
	ctx := context.Background()
	a := entry("", "a.bin", "app/a.bin", "a")
	w.svc.AllowDuplicateEntries = true
	w.publish(doc("app", "1", started0, moduleOf("m1", []string{a, a}, nil)))
	w.svc.AllowDuplicateEntries = false
	appendModules := func(modules ...string) error {
		return w.svc.Append(ctx, Append{
			Name: "app", Number: "1", Modules: []byte("[" + strings.Join(modules, ",") + "]"),
			By: "admin",
		}, everyone)
	}
	if err := appendModules(moduleOf("m2", []string{a}, nil)); err != nil {
		t.Fatalf("a clean module after duplicates: %v", err)
	}
	err := appendModules(moduleOf("m3", nil, []string{a, a}))
	if !errors.Is(err, ErrInvalid) || !strings.Contains(err.Error(), "module 'm3'") {
		t.Fatalf("duplicate in an added module: %v", err)
	}
	b := must(w.db.Builds().Get(ctx, "", "app", "1", time.Time{}))
	if len(b.Modules) != 2 {
		t.Errorf("a refused append changed the run: %d modules", len(b.Modules))
	}
	w.svc.AllowDuplicateEntries = true
	if err := appendModules(moduleOf("m3", nil, []string{a, a})); err != nil {
		t.Errorf("allowed: %v", err)
	}
}

// TestPromoteFilesSharingAChecksum promotes every dependency, even those
// sharing a SHA with another, as Artifactory 7.x does from 7.125.4, and
// every artifact of a run held at different paths with one checksum
// (7.161.15), while one file listed by several modules stays one file.
func TestPromoteFilesSharingAChecksum(t *testing.T) {
	w := promotionWorld(t)
	ctx := context.Background()
	w.put("libs-local", false, "deps/x.jar", "same", nil)
	w.put("libs-local", false, "deps/y.jar", "same", nil)
	w.put("libs-local", false, "unrelated/z.jar", "same", nil)
	w.put("libs-local", false, "one/a.bin", "a", buildProps("app", "1"))
	w.put("libs-local", false, "two/a.bin", "a", buildProps("app", "1"))
	w.publish(doc("app", "1", started0,
		moduleOf("m1",
			[]string{entry("", "a.bin", "one/a.bin", "a"), entry("", "a.bin", "two/a.bin", "a")},
			[]string{entry("org:x:1", "", "", "same"), entry("org:y:1", "", "", "same")}),
		moduleOf("m2", nil, []string{entry("org:x:1", "", "", "same")})))

	res, err := w.svc.Promote(ctx, Promotion{
		Name: "app", Number: "1", TargetRepo: "release-local", Copy: true,
		Artifacts: true, Dependencies: true, FailFast: true, By: "admin",
	}, everyone)
	if err != nil || res.Aborted {
		t.Fatalf("%+v, %v", res, err)
	}
	var promoted []string
	for _, p := range []string{
		"deps/x.jar", "deps/y.jar", "unrelated/z.jar", "one/a.bin", "two/a.bin",
	} {
		if w.has("release-local", p) {
			promoted = append(promoted, p)
		}
	}
	if want := []string{"deps/x.jar", "deps/y.jar", "one/a.bin", "two/a.bin"}; !slices.Equal(
		promoted, want) {
		t.Errorf("promoted %v, want %v\n%s", promoted, want, texts(res.Messages))
	}

	// The build artifacts search finds files as the promotion does.
	run := must(w.svc.Run(ctx, "", "app", "1", time.Time{}, everyone))
	var found []string
	for _, it := range must(w.svc.SearchArtifacts(ctx, run, func(it types.Item) bool {
		return it.RepoKey == "libs-local"
	}, everyone)) {
		found = append(found, it.FullPath())
	}
	if want := []string{"one/a.bin", "two/a.bin"}; !slices.Equal(found, want) {
		t.Errorf("search found %v, want %v", found, want)
	}
}

// TestFolderEntries ignores build-info entries that name a folder, as
// Artifactory 7.x does from 7.161.15: a folder has no checksum, so no file
// is found for it and nothing fails.
func TestFolderEntries(t *testing.T) {
	w := promotionWorld(t)
	ctx := context.Background()
	w.put("libs-local", false, "app/a.bin", "a", buildProps("app", "1"))
	w.put("libs-local", false, "app/dir/kept.bin", "kept", nil)
	folder := entry("", "dir", "app/dir", "")
	w.publish(doc("app", "1", at(0), moduleOf("m",
		[]string{entry("", "a.bin", "app/a.bin", "a"), folder},
		[]string{entry("app/dir", "", "", "")})))
	w.publish(doc("app", "2", at(1)))

	run := must(w.svc.Run(ctx, "", "app", "1", time.Time{}, everyone))
	if got := must(w.svc.SearchArtifacts(ctx, run, nil, everyone)); len(got) != 1 ||
		got[0].FullPath() != "app/a.bin" {
		t.Errorf("search found %+v", got)
	}
	res, err := w.svc.Promote(ctx, Promotion{
		Name: "app", Number: "1", TargetRepo: "release-local",
		Artifacts: true, Dependencies: true, FailFast: true, By: "admin",
	}, everyone)
	if err != nil || res.Aborted || !w.has("release-local", "app/a.bin") {
		t.Fatalf("promotion: %+v, %v", res, err)
	}
	if w.has("release-local", "app/dir") || !w.has("libs-local", "app/dir/kept.bin") {
		t.Error("the promotion moved the folder an entry names")
	}

	// Discarding the run with its artifacts deletes its files, not the
	// folder.
	w.put("release-local", false, "app/dir/kept.bin", "kept", nil)
	gone, err := w.svc.Discard(ctx, Discard{
		Name: "app", Retention: types.BuildRetention{Count: 1, DeleteArtifacts: true},
	}, everyone)
	if err != nil || len(gone) != 1 || gone[0].Number != "1" {
		t.Fatalf("discarded %+v, %v", gone, err)
	}
	if w.has("release-local", "app/a.bin") || !w.has("release-local", "app/dir/kept.bin") ||
		!w.has("libs-local", "app/dir/kept.bin") {
		t.Error("the discard deleted the wrong items")
	}
}
