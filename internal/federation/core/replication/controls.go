package replication

import (
	"context"
	"crypto/rand"
	"errors"
	"fmt"
	"regexp"
	"strings"

	"github.com/weaveplatform/artifactweave/internal/kernel"
)

const settingBlocks = "replication.blocks"

// Blocks pauses a direction without erasing individual configurations.
type Blocks struct {
	Push bool `json:"push"`
	Pull bool `json:"pull"`
}

func (s Service) blocks(ctx context.Context) (Blocks, error) {
	if s.Settings == nil {
		return Blocks{}, fmt.Errorf("replication settings are unavailable: %w", kernel.ErrStore)
	}
	var out Blocks
	err := s.Settings.Get(ctx, settingBlocks, &out)
	if errors.Is(err, kernel.ErrNotFound) {
		return Blocks{}, nil
	}
	if err != nil {
		return Blocks{}, fmt.Errorf("read global replication controls: %w", err)
	}
	return out, nil
}

// Global returns the current instance-wide pause flags to platform admins.
func (s Service) Global(ctx context.Context) (Blocks, error) {
	var out Blocks
	err := s.run(ctx, func(ctx context.Context, policy Policy) error {
		if !policy.Platform() {
			return kernel.ErrForbidden
		}
		var err error
		out, err = s.blocks(ctx)
		return err
	})
	if err != nil {
		return Blocks{}, err
	}
	return out, nil
}

// SetBlocked changes only selected directions. The HTTP block/unblock adapter
// supplies true for omitted direction selectors, as the public contract says.
func (s Service) SetBlocked(ctx context.Context, blocked, push, pull bool) error {
	return s.run(ctx, func(ctx context.Context, policy Policy) error {
		if !policy.Platform() {
			return kernel.ErrForbidden
		}
		out, err := s.blocks(ctx)
		if err != nil {
			return err
		}
		if push {
			out.Push = blocked
		}
		if pull {
			out.Pull = blocked
		}
		if err := s.Settings.Put(ctx, settingBlocks, out); err != nil {
			return fmt.Errorf("write global replication controls: %w", err)
		}
		return nil
	})
}

// SetEnabled applies URL wildcard filters atomically for a platform admin.
// Every actual enablement change rotates worker authority; identical retries
// preserve it. A malformed/unsupported stored target cannot be enabled as a
// silent no-op capability.
func (s Service) SetEnabled(ctx context.Context, enabled bool, include, exclude []string) error {
	return s.run(ctx, func(ctx context.Context, policy Policy) error {
		if !policy.Platform() {
			return kernel.ErrForbidden
		}
		rows, err := s.Store.Replications().List(ctx)
		if err != nil {
			return fmt.Errorf("list replication controls: %w", err)
		}
		for _, r := range rows {
			if len(include) > 0 && !matchesURL(include, r.URL) || matchesURL(exclude, r.URL) {
				continue
			}
			if r.Enabled == enabled && (!enabled || r.CronExp == "") {
				continue
			}
			previous := r
			if enabled {
				repository, err := s.repository(ctx, r.RepoKey, policy)
				if err != nil {
					return err
				}
				if _, err := s.target(ctx, r.RepoKey); err != nil {
					return err
				}
				if err := s.merge(ctx, repository, r, &r, Input{}); err != nil {
					return err
				}
			}
			r.Enabled = enabled
			if err := s.bindScheduleAuthor(ctx, &r); err != nil {
				return err
			}
			if r == previous {
				continue
			}
			r.Revision = rand.Text()
			if err := s.Store.Replications().Put(ctx, r); err != nil {
				return fmt.Errorf("write replication enablement: %w", err)
			}
			if err := s.publishSchedule(ctx, r); err != nil {
				return err
			}
		}
		return nil
	})
}

func matchesURL(patterns []string, target string) bool {
	for _, pattern := range patterns {
		// Only '*' is a public wildcard. Quote every other regexp operator;
		// '*' spans URL slashes, unlike a filesystem path matcher.
		pattern = "^" + strings.ReplaceAll(regexp.QuoteMeta(pattern), `\*`, ".*") + "$"
		if regexp.MustCompile(pattern).MatchString(target) {
			return true
		}
	}
	return false
}
