package federationhttp

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"math"
	"net/http"

	"github.com/weaveplatform/artifactweave/internal/federation/core/replication"
	"github.com/weaveplatform/artifactweave/internal/kernel"
	artgen "github.com/weaveplatform/artifactweave/internal/platform/gen/artifactory"
	"github.com/weaveplatform/artifactweave/internal/platform/httpkit"
	"github.com/weaveplatform/artifactweave/internal/platform/httpkit/apierror"
)

// ReplicationAPI serves single-target configuration and its enablement/global
// controls, stored-config execution and durable status. One-shot, multi-push and advanced replication
// retain their separate owners and generated responses.
type ReplicationAPI struct {
	Configurations *replication.Service
	Status         *replication.StatusReader
	Executions     *replication.Coordinator
}

func (s ReplicationAPI) service() (replication.Service, error) {
	if s.Configurations == nil {
		return replication.Service{}, apierror.ErrNotImplemented
	}
	return *s.Configurations, nil
}

func replicationError(ctx context.Context, err error) error {
	var response *apierror.Error
	switch {
	case err == nil:
		return nil
	case errors.As(err, &response):
		return err
	case errors.Is(err, kernel.ErrUnauthenticated),
		errors.Is(err, kernel.ErrBadCredentials),
		errors.Is(err, kernel.ErrForbidden),
		errors.Is(err, kernel.ErrPasswordExpired),
		errors.Is(err, kernel.ErrAPIRestricted):
		return err
	case errors.Is(err, replication.ErrMultipleTargets), errors.Is(err, replication.ErrAdvancedConfiguration):
		return apierror.ErrNotImplemented
	case errors.Is(err, kernel.ErrInvalid):
		return apierror.New(http.StatusBadRequest, err.Error())
	case errors.Is(err, kernel.ErrNotFound):
		return apierror.New(http.StatusNotFound, "Replication configuration or repository not found")
	case errors.Is(err, kernel.ErrConflict):
		return apierror.New(http.StatusConflict, "Replication configuration changed")
	default:
		httpkit.NoteCause(ctx, err)
		return apierror.New(http.StatusInternalServerError, "internal error")
	}
}

// GetRepositoryReplicationConfiguration returns one explicitly redacted object.
func (s ReplicationAPI) GetRepositoryReplicationConfiguration(
	ctx context.Context,
	req artgen.GetRepositoryReplicationConfigurationRequestObject,
) (artgen.GetRepositoryReplicationConfigurationResponseObject, error) {
	svc, err := s.service()
	if err != nil {
		return nil, err
	}
	value, err := svc.Get(ctx, req.RepoKey)
	if err != nil {
		return nil, replicationError(ctx, err)
	}
	body, err := configurationWire(value)
	if err != nil {
		return nil, replicationError(ctx, err)
	}
	var out artgen.GetRepositoryReplicationConfiguration200JSONResponse
	if err := out.FromReplicationConfigRequest(body); err != nil {
		return nil, replicationError(ctx, err)
	}
	return out, nil
}

// SetRepositoryReplicationConfiguration accepts one object or a one-element
// array and returns the authored 201 only after configuration commit.
func (s ReplicationAPI) SetRepositoryReplicationConfiguration(
	ctx context.Context,
	req artgen.SetRepositoryReplicationConfigurationRequestObject,
) (artgen.SetRepositoryReplicationConfigurationResponseObject, error) {
	svc, err := s.service()
	if err != nil {
		return nil, err
	}
	if req.Body == nil {
		return nil, httpkit.BadRequestf("a single replication configuration is required")
	}
	raw, err := req.Body.MarshalJSON()
	if err != nil {
		return nil, httpkit.BadRequestf("invalid replication configuration")
	}
	body, err := singleConfiguration(raw, true)
	if err != nil {
		return nil, err
	}
	if err := svc.Replace(ctx, req.RepoKey, configurationInput(body)); err != nil {
		return nil, replicationError(ctx, err)
	}
	return artgen.SetRepositoryReplicationConfiguration201Response{}, nil
}

// UpdateRepositoryReplicationConfiguration applies a partial target update, or
// the URL-filtered bulk control selected by the two reserved repository keys.
func (s ReplicationAPI) UpdateRepositoryReplicationConfiguration(
	ctx context.Context,
	req artgen.UpdateRepositoryReplicationConfigurationRequestObject,
) (artgen.UpdateRepositoryReplicationConfigurationResponseObject, error) {
	svc, err := s.service()
	if err != nil {
		return nil, err
	}
	if req.Body == nil {
		return nil, httpkit.BadRequestf("a replication update is required")
	}
	raw, err := req.Body.MarshalJSON()
	if err != nil {
		return nil, httpkit.BadRequestf("invalid replication update")
	}
	if req.RepoKey == "enable" || req.RepoKey == "disable" {
		var selection artgen.ReplicationEnableDisableRequest
		if !object(raw) {
			return nil, httpkit.BadRequestf("replication URL filters must be an object")
		}
		decoder := json.NewDecoder(bytes.NewReader(raw))
		decoder.DisallowUnknownFields()
		if err := decoder.Decode(&selection); err != nil {
			return nil, httpkit.BadRequestf("invalid replication URL filters")
		}
		err = svc.SetEnabled(ctx, req.RepoKey == "enable", httpkit.Deref(selection.Include), httpkit.Deref(selection.Exclude))
	} else {
		body, parseErr := singleConfiguration(raw, false)
		if parseErr != nil {
			return nil, parseErr
		}
		err = svc.Update(ctx, req.RepoKey, configurationInput(body))
	}
	if err != nil {
		return nil, replicationError(ctx, err)
	}
	return artgen.UpdateRepositoryReplicationConfiguration200Response{}, nil
}

// DeleteRepositoryReplicationConfiguration removes only the selected target.
func (s ReplicationAPI) DeleteRepositoryReplicationConfiguration(
	ctx context.Context,
	req artgen.DeleteRepositoryReplicationConfigurationRequestObject,
) (artgen.DeleteRepositoryReplicationConfigurationResponseObject, error) {
	svc, err := s.service()
	if err != nil {
		return nil, err
	}
	if err := svc.Delete(ctx, req.RepoKey, httpkit.Deref(req.Params.Url)); err != nil {
		return nil, replicationError(ctx, err)
	}
	return artgen.DeleteRepositoryReplicationConfiguration200Response{}, nil
}

func singleConfiguration(raw []byte, array bool) (artgen.ReplicationConfigRequest, error) {
	raw = bytes.TrimSpace(raw)
	if array && len(raw) > 0 && raw[0] == '[' {
		var entries []json.RawMessage
		if err := json.Unmarshal(raw, &entries); err != nil || len(entries) != 1 {
			return artgen.ReplicationConfigRequest{}, httpkit.BadRequestf("exactly one replication target is supported")
		}
		raw = entries[0]
	}
	if !object(raw) {
		return artgen.ReplicationConfigRequest{}, httpkit.BadRequestf("a replication configuration object is required")
	}
	var fields map[string]json.RawMessage
	if err := json.Unmarshal(raw, &fields); err != nil {
		return artgen.ReplicationConfigRequest{}, httpkit.BadRequestf("invalid replication configuration")
	}
	if _, ok := fields["include"]; ok {
		return artgen.ReplicationConfigRequest{}, httpkit.BadRequestf("URL filters require the reserved enable or disable operation")
	}
	if _, ok := fields["exclude"]; ok {
		return artgen.ReplicationConfigRequest{}, httpkit.BadRequestf("URL filters require the reserved enable or disable operation")
	}
	var out artgen.ReplicationConfigRequest
	if err := json.Unmarshal(raw, &out); err != nil {
		return out, httpkit.BadRequestf("invalid replication configuration fields")
	}
	return out, nil
}

func object(raw []byte) bool { raw = bytes.TrimSpace(raw); return len(raw) > 0 && raw[0] == '{' }

func configurationInput(b artgen.ReplicationConfigRequest) replication.Input {
	out := replication.Input{
		URL: b.Url, Username: b.Username, Password: b.Password, Enabled: b.Enabled, CronExp: b.CronExp,
		SyncDeletes: b.SyncDeletes, SyncProperties: b.SyncProperties, SyncStatistics: b.SyncStatistics,
		IncludePathPrefix: b.IncludePathPrefixPattern, ExcludePathPrefix: b.ExcludePathPrefixPattern,
		Proxy: b.Proxy, DisableProxy: b.DisableProxy, EventReplication: b.EnableEventReplication, CheckBinaryExistence: b.CheckBinaryExistenceInFilestore,
	}
	if b.SocketTimeoutMillis != nil {
		out.SocketTimeoutMillis = httpkit.PtrTo(int64(*b.SocketTimeoutMillis))
	}
	return out
}

func configurationWire(v replication.View) (artgen.ReplicationConfigRequest, error) {
	millis := v.SocketTimeout.Milliseconds()
	if millis < 1 || millis > math.MaxInt32 {
		return artgen.ReplicationConfigRequest{}, fmt.Errorf("stored replication timeout is outside the supported range: %w", kernel.ErrStore)
	}
	return artgen.ReplicationConfigRequest{
		RepoKey: &v.RepoKey, ReplicationKey: &v.Key, Url: &v.URL, Username: &v.Username, Password: &v.Password,
		Enabled: &v.Enabled, CronExp: &v.CronExp, SyncDeletes: &v.SyncDeletes, SyncProperties: &v.SyncProperties, SyncStatistics: &v.SyncStatistics,
		IncludePathPrefixPattern: &v.IncludePathPrefix, ExcludePathPrefixPattern: &v.ExcludePathPrefix, Proxy: &v.Proxy, ProxyRef: &v.Proxy, DisableProxy: &v.DisableProxy,
		EnableEventReplication: httpkit.PtrTo(false), CheckBinaryExistenceInFilestore: httpkit.PtrTo(false),
		SocketTimeoutMillis: httpkit.PtrTo(int32(millis)),
	}, nil
}
