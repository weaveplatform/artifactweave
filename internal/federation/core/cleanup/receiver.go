package cleanup

import (
	"bytes"
	"context"
	"fmt"
	"strings"
	"unicode/utf8"

	"github.com/weaveplatform/artifactweave/internal/federation/api"
	"github.com/weaveplatform/artifactweave/internal/federation/core/model"
	"github.com/weaveplatform/artifactweave/internal/kernel"
)

func validateVersions(versions []api.CleanupVersion) error {
	if len(versions) == 0 || len(versions) > api.CleanupBatchSize {
		return kernel.ErrInvalid
	}
	seen := map[api.CleanupVersion]bool{}
	for _, version := range versions {
		if version.Package == "" || version.Version == "" || len(version.Package) > 4096 || len(version.Version) > 4096 ||
			!utf8.ValidString(
				version.Package,
			) || !utf8.ValidString(version.Version) || strings.ContainsRune(version.Package, 0) || strings.ContainsRune(version.Version, 0) || seen[version] {
			return kernel.ErrInvalid
		}
		seen[version] = true
	}
	return nil
}

// Downloads answers an authenticated member's fresh batch. Receiving-only
// members can report reads; this grants no reverse event or configuration write.
func (s *Service) Downloads(ctx context.Context, caller model.Peer, key string, request Request) (Answer, error) {
	if err := validateVersions(request.Versions); err != nil {
		return Answer{}, err
	}
	if request.PackageType == "" || len(request.PackageType) > 64 || len(request.Challenge) < 16 || len(request.Challenge) > 128 || request.SourceRepository == "" ||
		len(request.SourceRepository) > 1024 {
		return Answer{}, kernel.ErrInvalid
	}
	var answer Answer
	err := s.Store.Tx(ctx, func(ctx context.Context) error {
		current, err := s.Store.Peers().Get(ctx, caller.InstanceID)
		if err != nil {
			return fmt.Errorf("read current observation member: %w", err)
		}
		if !bytes.Equal(current.PublicKey, caller.PublicKey) || !current.Compatible() || !federatedGrant(current) {
			return kernel.ErrForbidden
		}
		scope, err := s.Scope(ctx, key)
		if err != nil {
			return fmt.Errorf("read current observation member: %w", err)
		}
		if scope.PackageType != request.PackageType {
			return fmt.Errorf("cleanup member repository type differs: %w", kernel.ErrConflict)
		}
		found := false
		grant := ""
		for _, member := range scope.Members {
			if member.Instance == current.InstanceID && member.Repository == request.SourceRepository {
				found = true
				grant = member.Grant
				break
			}
		}
		if !found {
			return kernel.ErrForbidden
		}
		if err := s.guardScope(ctx, scope); err != nil {
			return fmt.Errorf("read current observation member: %w", err)
		}
		rows, err := s.localDownloads(ctx, scope, request.Versions)
		if err != nil {
			return fmt.Errorf("read current observation member: %w", err)
		}
		answer = Answer{
			PackageType: scope.PackageType,
			Instance:    scope.Self,
			Incarnation: scope.Incarnation,
			Generation:  scope.Generation,
			Grant:       grant,
			Challenge:   request.Challenge,
			Versions:    rows,
		}
		return nil
	})
	if err != nil {
		return Answer{}, fmt.Errorf("member cleanup observation: %w", err)
	}
	return answer, nil
}
