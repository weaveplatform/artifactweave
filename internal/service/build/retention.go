package build

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"slices"
	"strconv"
	"strings"
	"time"

	"github.com/weaveplatform/artifactweave/pkg/types"
)

// RetentionSettings are retention settings as clients send them, in a
// retention request or a build-info document's buildRetention. count may be
// a number or a numeric string: the JFrog CLI sends a string. Artifactory
// reads minimumBuildDate as a java.util.Date, which takes epoch
// milliseconds or a timestamp, so both are accepted.
type RetentionSettings struct {
	Count                        json.Number
	MinimumBuildDate             json.RawMessage
	DeleteBuildArtifacts         bool
	BuildNumbersNotToBeDiscarded []string
}

// Retention reads the settings. A negative count keeps every run, as an
// absent one does: build-info's BuildRetention defaults count to -1. A
// count of 0 keeps every run too unless strict, which refuses it as
// Artifactory's retention endpoint does ("Max count retention needs to be
// a positive number").
func (r RetentionSettings) Retention(strict bool) (types.BuildRetention, error) {
	var out types.BuildRetention
	if r.Count != "" {
		n, err := strconv.ParseInt(r.Count.String(), 10, 32)
		switch {
		case err != nil:
			return out, fmt.Errorf("%w: count %q is not a whole number", ErrInvalid, r.Count)
		case n == 0 && strict:
			return out, fmt.Errorf(
				"%w: max count retention needs to be a positive number",
				ErrInvalid,
			)
		case n > 0:
			out.Count = int(n)
		}
	}
	if raw := bytes.TrimSpace(
		r.MinimumBuildDate,
	); len(raw) > 0 &&
		!bytes.Equal(raw, []byte("null")) {
		t, err := minimumBuildDate(raw)
		if err != nil {
			return out, err
		}
		out.MinimumBuildDate = t
	}
	out.DeleteArtifacts = r.DeleteBuildArtifacts
	for _, n := range r.BuildNumbersNotToBeDiscarded {
		if n = strings.TrimSpace(n); n != "" {
			out.KeepNumbers = append(out.KeepNumbers, n)
		}
	}
	return out, nil
}

// minimumBuildDate reads epoch milliseconds, as a number or a numeric
// string, or a timestamp in ParseStarted's layouts.
func minimumBuildDate(raw json.RawMessage) (time.Time, error) {
	var s string
	if json.Unmarshal(raw, &s) != nil {
		s = string(raw)
	}
	if ms, err := strconv.ParseInt(s, 10, 64); err == nil {
		return time.UnixMilli(ms).UTC(), nil
	}
	t, err := ParseStarted(s)
	if err != nil {
		return t, fmt.Errorf(
			"%w: minimumBuildDate %s is neither epoch milliseconds nor a timestamp",
			ErrInvalid,
			raw,
		)
	}
	return t, nil
}

// documentRetention reads a build-info document's buildRetention; ok is
// false when it has none. The document is read by key, as JFrog names
// them, rather than through a tagged struct.
func documentRetention(raw []byte) (r types.BuildRetention, ok bool, err error) {
	var doc, fields map[string]json.RawMessage
	if err := json.Unmarshal(raw, &doc); err != nil {
		return r, false, fmt.Errorf("%w: the build-info document: %w", ErrInvalid, err)
	}
	section := doc["buildRetention"]
	if len(section) == 0 || bytes.Equal(section, []byte("null")) {
		return r, false, nil
	}
	if err := json.Unmarshal(section, &fields); err != nil {
		return r, false, fmt.Errorf("%w: buildRetention: %w", ErrInvalid, err)
	}
	settings := RetentionSettings{MinimumBuildDate: fields["minimumBuildDate"]}
	for key, into := range map[string]any{
		"count":                        &settings.Count,
		"deleteBuildArtifacts":         &settings.DeleteBuildArtifacts,
		"buildNumbersNotToBeDiscarded": &settings.BuildNumbersNotToBeDiscarded,
	} {
		if v, ok := fields[key]; ok {
			if err := json.Unmarshal(v, into); err != nil {
				return r, false, fmt.Errorf("%w: buildRetention.%s: %w", ErrInvalid, key, err)
			}
		}
	}
	r, err = settings.Retention(false)
	return r, err == nil, err
}

// Discard applies retention settings to the runs of a build.
type Discard struct {
	ProjectKey, Name string
	Retention        types.BuildRetention
	By               string
}

// Discards returns the runs d discards, oldest first, having checked that
// the caller may: it needs delete on the build and on each run discarded.
// ErrNotFound when the build has no runs.
func (s *Service) Discards(ctx context.Context, d Discard, a Access) ([]types.BuildRun, error) {
	projectKey := ProjectKey(d.ProjectKey)
	runs, err := s.runs(ctx, projectKey, d.Name)
	if err != nil {
		return nil, err
	}
	if len(runs) == 0 {
		return nil, fmt.Errorf("build: runs of %s: %w", d.Name, ErrNotFound)
	}
	repoKey := types.BuildInfoRepo(projectKey)
	if !a.Build(repoKey, types.EncodeBuildInfoSegment(d.Name), true, types.ActionDelete) {
		return nil, ErrForbidden
	}
	out := discards(runs, d.Retention)
	for _, r := range out {
		if !a.Build(repoKey, documentPath(r), false, types.ActionDelete) {
			return nil, ErrForbidden
		}
	}
	return out, nil
}

// Discard deletes the runs of a build that d does not keep, as a delete of
// them would (Delete): with their documents, and with their artifacts when
// d asks. It returns the deleted runs.
func (s *Service) Discard(ctx context.Context, d Discard, a Access) ([]types.BuildRun, error) {
	var gone []types.BuildRun
	err := s.Store.Tx(ctx, func(ctx context.Context) error {
		runs, err := s.Discards(ctx, d, a)
		if err != nil || len(runs) == 0 {
			return err
		}
		// The runs a retention discards are the oldest of each of their
		// numbers, which is what Delete removes for each occurrence of a
		// number.
		numbers := make([]string, 0, len(runs))
		for _, r := range runs {
			numbers = append(numbers, r.Number)
		}
		gone, err = s.Delete(ctx, Delete{
			ProjectKey: d.ProjectKey, Name: d.Name, Numbers: numbers,
			Artifacts: d.Retention.DeleteArtifacts, By: d.By,
		}, a)
		return err
	})
	if err != nil {
		return nil, err //nolint:wrapcheck // the transaction returns ours
	}
	return gone, nil
}

// discards returns the runs r discards of runs, which are newest first:
// those past the newest r.Count and those started before
// r.MinimumBuildDate, less every run of a number r keeps. A kept number's
// runs still count towards r.Count, as the builds Jenkins keeps forever
// count in its log rotation, which build retention mirrors. They are
// returned oldest first.
func discards(runs []types.BuildRun, r types.BuildRetention) []types.BuildRun {
	var out []types.BuildRun
	for i, run := range slices.Backward(runs) {
		if slices.Contains(r.KeepNumbers, run.Number) {
			continue
		}
		if (r.Count > 0 && i >= r.Count) ||
			(!r.MinimumBuildDate.IsZero() && run.Started.Before(r.MinimumBuildDate)) {
			out = append(out, run)
		}
	}
	return out
}
