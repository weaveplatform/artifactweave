package remote

import (
	"context"
	"errors"
	"fmt"
	"time"

	"github.com/weaveplatform/artifactweave/internal/source"
	"github.com/weaveplatform/artifactweave/pkg/repo"
	"github.com/weaveplatform/artifactweave/pkg/store"
	"github.com/weaveplatform/artifactweave/pkg/types"
)

// SmartStatus is an artifactweave diagnostic, not an Artifactory storage wire
// document. A zero origin timestamp means unknown, never confirmed absent.
type SmartStatus struct {
	Repository      string                      `json:"repository"`
	Path            string                      `json:"path"`
	Representations []SmartRepresentationStatus `json:"representations"`
	Truncated       bool                        `json:"truncated"`
}

type SmartRepresentationStatus struct {
	PropertiesCheckedAt time.Time         `json:"properties_checked_at"`
	OriginCheckedAt     time.Time         `json:"origin_checked_at"`
	OriginAbsent        *bool             `json:"origin_absent,omitempty"`
	Downloads           int64             `json:"downloads"`
	Claimed             int64             `json:"claimed"`
	Uncertain           int64             `json:"uncertain"`
	Work                []SmartWorkStatus `json:"work"`
}

type SmartWorkStatus struct {
	Operation string          `json:"operation"`
	State     types.WorkState `json:"state"`
	Attempts  int             `json:"attempts"`
}

// Status reads one exact path under a source lifetime lock. Options resolves
// only stored settings: this endpoint never discovers or contacts a peer.
func (s *SmartRemote) Status(ctx context.Context, key, path string) (SmartStatus, error) {
	out := SmartStatus{Repository: key, Path: path, Representations: []SmartRepresentationStatus{}}
	err := s.policy.Store.Tx(ctx, func(tx context.Context) error {
		c, err := s.policy.Store.Repos().Get(tx, key)
		if err != nil {
			return smartError("operation", err)
		}
		if c.Class != repo.Remote {
			return source.ErrInvalidRequest
		}
		identity, err := s.policy.Store.RemoteCache().Source(tx, key)
		if err != nil {
			return smartError("operation", err)
		}
		// Source holds the repository lifetime lock. Re-read configuration
		// under that lock in case it changed after the initial class check.
		c, err = s.policy.Store.Repos().Get(tx, key)
		if err != nil {
			return smartError("current repository", err)
		}
		opts, err := s.policy.Options(tx, c)
		if err != nil {
			return smartError("operation", err)
		}
		entries, next, err := s.policy.Store.RemoteCache().
			Exact(tx, key, path, store.Page{Limit: 16})
		if err != nil {
			return smartError("operation", err)
		}
		out.Truncated = next != ""
		kind := "remote.smart"
		if c.PackageType == repo.Docker || c.PackageType == repo.OCI ||
			c.PackageType == repo.HelmOCI {
			kind = "remote.smart.registry"
		}
		for _, entry := range entries {
			if entry.Token.Source != identity ||
				entry.Token.Key.SettingsRevision != opts.Revision ||
				entry.Smart == nil {
				continue
			}
			it, e := s.policy.Store.Items().GetByID(tx, entry.ItemID)
			if errors.Is(e, store.ErrNotFound) {
				continue
			}
			if e != nil {
				return smartError("operation", e)
			}
			if it.Actual.SHA256 != entry.Blob.Checksums.SHA256 {
				continue
			}
			state := entry.Smart
			status := SmartRepresentationStatus{
				PropertiesCheckedAt: state.PropertiesAt,
				OriginCheckedAt:     state.OriginAt,
				Downloads:           state.Downloads,
				Claimed:             state.Claimed,
				Uncertain:           state.Uncertain,
				Work:                []SmartWorkStatus{},
			}
			if !state.OriginAt.IsZero() {
				status.OriginAbsent = new(state.OriginAbsent)
			}
			for _, statistics := range []bool{false, true} {
				job, e := s.policy.Store.WorkQueue().
					Get(tx, kind, smartWorkKey(entry.Token.Key, identity, statistics))
				if errors.Is(e, store.ErrNotFound) {
					continue
				}
				if e != nil {
					return smartError("operation", e)
				}
				operation := "refresh"
				if statistics {
					operation = "statistics"
				}
				status.Work = append(
					status.Work,
					SmartWorkStatus{Operation: operation, State: job.State, Attempts: job.Attempts},
				)
			}
			out.Representations = append(out.Representations, status)
		}
		return nil
	})
	return out, smartError("status", err)
}

func smartError(operation string, err error) error {
	if err == nil {
		return nil
	}
	return fmt.Errorf("smart remote %s: %w", operation, err)
}
