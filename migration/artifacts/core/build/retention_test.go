package build

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"strings"
	"testing"
	"time"

	"github.com/weaveplatform/artifactweave/pkg/types"
)

func TestUnitRetentionSettings(t *testing.T) {
	ms := time.Date(2026, 10, 15, 9, 30, 0, 0, time.UTC)
	// A document's buildRetention, read as the request's settings are, but
	// with a count of 0 keeping every run.
	for _, tc := range []struct {
		json     string
		expected types.BuildRetention
	}{
		{`{}`, types.BuildRetention{}},
		{
			`{"count":"3","buildNumbersNotToBeDiscarded":["1"," ",""]}`,
			types.BuildRetention{Count: 3, KeepNumbers: []string{"1"}},
		},
		{
			`{"count":3,"deleteBuildArtifacts":true}`,
			types.BuildRetention{Count: 3, DeleteArtifacts: true},
		},
		{`{"count":-1}`, types.BuildRetention{}},
		{`{"count":0}`, types.BuildRetention{}},
		{`{"minimumBuildDate":1792056600000}`, types.BuildRetention{MinimumBuildDate: ms}},
		{`{"minimumBuildDate":"1792056600000"}`, types.BuildRetention{MinimumBuildDate: ms}},
		{`{"minimumBuildDate":"` + started0 + `"}`, types.BuildRetention{MinimumBuildDate: ms}},
		{`{"minimumBuildDate":null}`, types.BuildRetention{}},
	} {
		got, ok, err := documentRetention([]byte(`{"buildRetention":` + tc.json + `}`))
		if err != nil || !ok || fmt.Sprint(got) != fmt.Sprint(tc.expected) {
			t.Errorf("%s: %+v, %v, %v; want %+v", tc.json, got, ok, err, tc.expected)
		}
	}
	for _, doc := range []string{`{}`, `{"buildRetention":null}`} {
		if _, ok, err := documentRetention([]byte(doc)); ok || err != nil {
			t.Errorf("%s: %v, %v", doc, ok, err)
		}
	}
	for _, doc := range []string{
		`[]`, `{"buildRetention":[]}`, `{"buildRetention":{"count":"x"}}`,
		`{"buildRetention":{"deleteBuildArtifacts":"yes"}}`,
	} {
		if _, _, err := documentRetention([]byte(doc)); !errors.Is(err, ErrInvalid) {
			t.Errorf("%s: %v", doc, err)
		}
	}
	for _, tc := range []struct {
		r        RetentionSettings
		expected string
	}{
		{RetentionSettings{Count: "ten"}, "not a whole number"},
		{RetentionSettings{Count: "2.5"}, "not a whole number"},
		{RetentionSettings{Count: "0"}, "max count retention needs to be a positive number"},
		{
			RetentionSettings{MinimumBuildDate: json.RawMessage(`"last week"`)},
			"neither epoch milliseconds nor a timestamp",
		},
		{
			RetentionSettings{MinimumBuildDate: json.RawMessage(`true`)},
			"neither epoch milliseconds nor a timestamp",
		},
	} {
		if _, err := tc.r.Retention(true); !errors.Is(err, ErrInvalid) ||
			!strings.Contains(err.Error(), tc.expected) {
			t.Errorf("%+v: %v, want %q", tc.r, err, tc.expected)
		}
	}
}

func TestUnitDiscards(t *testing.T) {
	base := time.Date(2026, 10, 15, 0, 0, 0, 0, time.UTC)
	// Newest first: numbers 5, 4, 3, 3, 2, 1 started on days 6 to 1.
	var runs []types.BuildRun
	for i, n := range []string{"5", "4", "3", "3", "2", "1"} {
		runs = append(runs, types.BuildRun{
			ID: int64(6 - i), Number: n, Started: base.AddDate(0, 0, 6-i),
		})
	}
	ids := func(rs []types.BuildRun) string {
		var out []string
		for _, r := range rs {
			out = append(out, fmt.Sprint(r.ID))
		}
		return strings.Join(out, ",")
	}
	for _, tc := range []struct {
		r        types.BuildRetention
		expected string
	}{
		{types.BuildRetention{}, ""},
		{types.BuildRetention{Count: 10}, ""},
		{types.BuildRetention{Count: 2}, "1,2,3,4"},
		// The kept number's runs count towards the two kept.
		{types.BuildRetention{Count: 2, KeepNumbers: []string{"5", "2"}}, "1,3,4"},
		{types.BuildRetention{MinimumBuildDate: base.AddDate(0, 0, 3)}, "1,2"},
		{types.BuildRetention{Count: 5, MinimumBuildDate: base.AddDate(0, 0, 2)}, "1"},
	} {
		if got := ids(discards(runs, tc.r)); got != tc.expected {
			t.Errorf("%+v: discards %s, want %s", tc.r, got, tc.expected)
		}
	}
}

// TestUnitDiscard deletes the runs retention does not keep, the oldest of a
// shared number first, with their documents and, when asked, artifacts.
func TestUnitDiscard(t *testing.T) {
	w := newWorld(t)
	ctx := context.Background()
	for i, n := range []string{"1", "2", "2", "3"} {
		w.publish(doc("app", n, at(i)))
	}
	w.deploy("app/a.bin", "artifact", buildProps("app", "4"))
	w.publish(doc("app", "4", at(4), artifactModule("artifact")))

	gone, err := w.svc.Discard(ctx, Discard{
		Name: "app", Retention: types.BuildRetention{Count: 3, KeepNumbers: []string{"1"}},
	}, everyone)
	if err != nil {
		t.Fatal(err)
	}
	if len(gone) != 1 || gone[0].Number != "2" || !gone[0].Started.Equal(mustStarted(at(1))) {
		t.Errorf("discarded %+v, want the first run of 2", gone)
	}
	if w.has(types.BuildInfoRepository, runPath("app", "2", at(1))) ||
		!w.has(types.BuildInfoRepository, runPath("app", "2", at(2))) {
		t.Error("the wrong document went")
	}
	gone, err = w.svc.Discard(ctx, Discard{
		Name: "app", Retention: types.BuildRetention{Count: 1, DeleteArtifacts: true},
	}, everyone)
	if err != nil || len(gone) != 3 {
		t.Fatalf("discarded %+v, %v", gone, err)
	}
	if !w.has("libs-local", "app/a.bin") {
		t.Error("the kept run's artifact went")
	}
	// Nothing left to discard is no error.
	if gone, err := w.svc.Discard(ctx, Discard{
		Name: "app", Retention: types.BuildRetention{Count: 1},
	}, everyone); err != nil || len(gone) != 0 {
		t.Errorf("discarded %+v, %v", gone, err)
	}
	if _, err := w.svc.Discard(ctx, Discard{Name: "absent"}, everyone); !errors.Is(
		err, ErrNotFound) {
		t.Errorf("unknown build: %v", err)
	}
}

// TestUnitDiscardPermissions needs delete on the build and on each run it
// discards.
func TestUnitDiscardPermissions(t *testing.T) {
	w := newWorld(t)
	ctx := context.Background()
	w.publish(doc("app", "1", at(0)))
	w.publish(doc("app", "2", at(1)))
	d := Discard{Name: "app", Retention: types.BuildRetention{Count: 1}}
	if _, err := w.svc.Discards(ctx, d, only(types.ActionRead)); !errors.Is(err, ErrForbidden) {
		t.Errorf("without delete: %v", err)
	}
	notOne := perms{build: func(_, path string, _ bool, _ types.Actions) bool {
		return !strings.HasPrefix(path, "app/1-")
	}}
	if _, err := w.svc.Discard(ctx, d, notOne); !errors.Is(err, ErrForbidden) {
		t.Errorf("without delete on the run discarded: %v", err)
	}
	if runs, err := w.svc.Runs(ctx, "", "app", everyone); err != nil || len(runs) != 2 {
		t.Errorf("runs %+v, %v", runs, err)
	}
}

// TestUnitPublishRetention applies a document's buildRetention as the
// publisher, and keeps it as the build name's.
func TestUnitPublishRetention(t *testing.T) {
	w := newWorld(t)
	ctx := context.Background()
	w.publish(doc("app", "1", at(0)))
	w.publish(doc("app", "2", at(1)))
	retained := `{"name":"app","number":"3","started":"` + at(2) + `",` +
		`"buildRetention":{"count":2,"buildNumbersNotToBeDiscarded":["9"]}}`

	// A publisher without delete publishes, and nothing is discarded.
	p, err := w.svc.Publish(ctx, "", []byte(retained), "ci", only(types.ActionWrite))
	if err != nil || !errors.Is(p.Retention, ErrForbidden) {
		t.Fatalf("%v, retention %v", err, p.Retention)
	}
	if runs, _ := w.svc.Runs(ctx, "", "app", everyone); len(runs) != 3 {
		t.Errorf("runs %+v", runs)
	}
	stored, err := w.db.Builds().Retention(ctx, "", "app")
	if err != nil || stored.Count != 2 || fmt.Sprint(stored.KeepNumbers) != "[9]" {
		t.Errorf("stored retention %+v, %v", stored, err)
	}

	p = w.publish(strings.Replace(retained, `"number":"3"`, `"number":"4"`, 1))
	if p.Retention != nil || len(p.Discarded) != 2 {
		t.Errorf("discarded %+v, %v", p.Discarded, p.Retention)
	}
	// A retention that discards nothing is kept all the same.
	w.publish(`{"name":"other","number":"1","started":"` + at(0) +
		`","buildRetention":{"deleteBuildArtifacts":true}}`)
	if r, err := w.db.Builds().Retention(ctx, "", "other"); err != nil || !r.DeleteArtifacts {
		t.Errorf("stored retention %+v, %v", r, err)
	}
	for _, bad := range []string{`{"count":"x"}`, `[]`} {
		if _, err := w.svc.Publish(ctx, "", []byte(`{"name":"app","number":"5","started":"`+
			at(5)+`","buildRetention":`+bad+`}`), "ci", everyone); !errors.Is(err, ErrInvalid) {
			t.Errorf("buildRetention %s: %v", bad, err)
		}
	}
}

func mustStarted(s string) time.Time {
	t, err := ParseStarted(s)
	if err != nil {
		panic(err)
	}
	return t
}
