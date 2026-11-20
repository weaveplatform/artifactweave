package federationhttp

import (
	"context"

	artgen "github.com/weaveplatform/artifactweave/internal/platform/gen/artifactory"
	"github.com/weaveplatform/artifactweave/internal/platform/httpkit"
)

// GetGlobalSystemReplicationConfiguration reports the current pause flags.
func (s ReplicationAPI) GetGlobalSystemReplicationConfiguration(
	ctx context.Context,
	_ artgen.GetGlobalSystemReplicationConfigurationRequestObject,
) (artgen.GetGlobalSystemReplicationConfigurationResponseObject, error) {
	svc, err := s.service()
	if err != nil {
		return nil, err
	}
	value, err := svc.Global(ctx)
	if err != nil {
		return nil, replicationError(ctx, err)
	}
	return artgen.GetGlobalSystemReplicationConfiguration200JSONResponse{BlockPushReplications: &value.Push, BlockPullReplications: &value.Pull}, nil
}

// BlockSystemReplication pauses the selected directions without deleting targets.
func (s ReplicationAPI) BlockSystemReplication(ctx context.Context, req artgen.BlockSystemReplicationRequestObject) (artgen.BlockSystemReplicationResponseObject, error) {
	if err := s.block(ctx, true, req.Params.Push, req.Params.Pull); err != nil {
		return nil, err
	}
	return artgen.BlockSystemReplication200TextResponse("Successfully blocked selected replications"), nil
}

// UnblockSystemReplication resumes only the selected global directions.
func (s ReplicationAPI) UnblockSystemReplication(ctx context.Context, req artgen.UnblockSystemReplicationRequestObject) (artgen.UnblockSystemReplicationResponseObject, error) {
	if err := s.block(ctx, false, req.Params.Push, req.Params.Pull); err != nil {
		return nil, err
	}
	return artgen.UnblockSystemReplication200TextResponse("Successfully unblocked selected replications"), nil
}

func (s ReplicationAPI) block(ctx context.Context, blocked bool, push, pull *string) error {
	svc, err := s.service()
	if err != nil {
		return err
	}
	p, err := direction(push)
	if err != nil {
		return err
	}
	l, err := direction(pull)
	if err != nil {
		return err
	}
	return replicationError(ctx, svc.SetBlocked(ctx, blocked, p, l))
}

func direction(value *string) (bool, error) {
	if value == nil || *value == "true" {
		return true, nil
	}
	if *value == "false" {
		return false, nil
	}
	return false, httpkit.BadRequestf("replication direction parameters must be true or false")
}
