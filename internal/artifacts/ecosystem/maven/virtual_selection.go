package maven

import (
	"context"
	"fmt"
	"strings"
	"time"

	"github.com/weaveplatform/artifactweave/internal/artifacts/api/repo"
	"github.com/weaveplatform/artifactweave/internal/artifacts/core/format"
	"github.com/weaveplatform/artifactweave/internal/artifacts/core/layout"
)

// SelectVirtual follows ArtifactoryVirtualDownloadStrategy's mode decision on
// the original path and the virtual's layout. Member resolution stays separate.
func (Handler) SelectVirtual(ctx context.Context, env format.Env, c repo.Config, path string) (*format.VirtualSelection, error) {
	r, err := readerOf(ctx, env, c)
	if err != nil {
		return nil, err
	}
	co, valid := r.read(path)
	snapshot := co.Integration() || !valid && (IsSnapshot(path) || strings.Contains(path, tokenIntegration))
	if !snapshot && !strings.Contains(path, tokenRelease) {
		return nil, nil //nolint:nilnil // ordinary paths keep the engine's first-hit walk
	}
	s := &format.VirtualSelection{DirectRemotes: snapshot, Missing: "Could not find resource"}
	if snapshot {
		s.Missing = "Artifact not found: " + path
	}
	s.Skip = func(member repo.Config) []string {
		if snapshot && !handles(member.HandleSnapshots) {
			return []string{member.Key + " doesn't handle snapshot resources", "Skipping " + member.Key}
		}
		if !snapshot && valid && !handles(member.HandleReleases) {
			return []string{"Skipping " + member.Key + " - doesn't handle releases"}
		}
		return nil
	}
	unique := IsUniqueSnapshot(path) && !strings.Contains(path, tokenIntegration)
	s.Choose = func(ctx context.Context, current format.VirtualCandidate, kept *format.VirtualCandidate) (format.VirtualChoice, error) {
		first := kept == nil
		exact := !first && !kept.Exact && current.Exact
		if snapshot {
			steps := []string{"Resource last modified time - " + current.Modified.Format(time.RFC3339Nano)}
			if unique && current.Exact {
				return format.VirtualChoice{Take: true, Stop: true, Steps: append(steps, "Selecting a unique snapshot")}, nil
			}
			newer := !first && current.Modified.After(kept.Modified)
			steps = append(
				steps,
				fmt.Sprintf(
					"Current found resource is the first candidate = %t, is an exact match query while the former candidate isn't = %t, has later modified time than former = %t",
					first,
					exact,
					newer,
				),
			)
			take := first || exact || newer
			if take {
				steps = append(steps, "Selecting current found resource as best candidate")
			}
			return format.VirtualChoice{Take: take, Steps: steps}, nil
		}
		newer := false
		if !first {
			a, err := virtualCoordinates(ctx, env, current)
			if err != nil {
				return format.VirtualChoice{}, err
			}
			b, err := virtualCoordinates(ctx, env, *kept)
			if err != nil {
				return format.VirtualChoice{}, err
			}
			newer = compareLatest(a, b) > 0
		}
		return format.VirtualChoice{
			Take: first || exact || newer,
			Steps: []string{
				fmt.Sprintf(
					"Resource was found in %s, is the first candidate = %t, is not the first candidate and is the first exact match = %t, is more recent = %t",
					current.Repo.Key,
					first,
					exact,
					newer,
				),
			},
		}, nil
	}
	s.Returning = func(c format.VirtualCandidate) string {
		if snapshot {
			return "Returning found resource from " + c.Repo.Key + ":" + c.Path
		}
		return "Returning latest release found " + c.Repo.Key + ":" + c.Path
	}
	return s, nil
}

func virtualCoordinates(ctx context.Context, env format.Env, c format.VirtualCandidate) (co layout.Coordinates, err error) {
	r, err := readerOf(ctx, env, c.Repo)
	if err != nil {
		return co, err
	}
	parsed, _ := r.read(c.Path)
	return parsed.Coordinates, nil
}
