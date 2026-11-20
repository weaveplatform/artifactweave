package replication

import (
	"context"
	"errors"
	"fmt"
	"math"
	"net/url"
	"time"

	artifactsapi "github.com/weaveplatform/artifactweave/internal/artifacts/api"
	"github.com/weaveplatform/artifactweave/internal/artifacts/api/repo"
	"github.com/weaveplatform/artifactweave/internal/federation/core/model"
	"github.com/weaveplatform/artifactweave/internal/kernel"
	"github.com/weaveplatform/artifactweave/internal/platform/crypt"
	"github.com/weaveplatform/artifactweave/internal/platform/schedule"
)

func (s Service) merge(ctx context.Context, repository artifactsapi.ProjectRepository, old model.Replication, next *model.Replication, in Input) error {
	if in.EventReplication != nil && *in.EventReplication || in.CheckBinaryExistence != nil && *in.CheckBinaryExistence {
		return fmt.Errorf("event and checksum replication are not supported: %w", kernel.ErrInvalid)
	}
	for _, pair := range []struct {
		from *string
		to   *string
	}{
		{in.URL, &next.URL},
		{in.Username, &next.Username},
		{in.CronExp, &next.CronExp},
		{in.IncludePathPrefix, &next.IncludePathPrefix},
		{in.ExcludePathPrefix, &next.ExcludePathPrefix},
		{in.Proxy, &next.Proxy},
	} {
		if pair.from != nil {
			*pair.to = *pair.from
		}
	}
	for _, pair := range []struct {
		from *bool
		to   *bool
	}{
		{in.Enabled, &next.Enabled},
		{in.SyncDeletes, &next.SyncDeletes},
		{in.SyncProperties, &next.SyncProperties},
		{in.SyncStatistics, &next.SyncStatistics},
		{in.DisableProxy, &next.DisableProxy},
		{in.EventReplication, &next.EventReplication},
		{in.CheckBinaryExistence, &next.CheckBinaryExistence},
	} {
		if pair.from != nil {
			*pair.to = *pair.from
		}
	}
	if next.EventReplication || next.CheckBinaryExistence {
		return fmt.Errorf("event and checksum replication are not supported: %w", kernel.ErrInvalid)
	}
	if in.Password != nil {
		next.Password = crypt.KeepIfMasked(*in.Password, old.Password)
	}
	if in.SocketTimeoutMillis != nil {
		if *in.SocketTimeoutMillis <= 0 || *in.SocketTimeoutMillis > math.MaxInt32 {
			return fmt.Errorf("socketTimeoutMillis must be a positive 32-bit integer: %w", kernel.ErrInvalid)
		}
		next.SocketTimeout = time.Duration(*in.SocketTimeoutMillis) * time.Millisecond
	}
	if next.SocketTimeout <= 0 {
		return fmt.Errorf("socketTimeoutMillis must be positive: %w", kernel.ErrInvalid)
	}
	if repository.Class == repo.Remote {
		if s.Repositories == nil {
			return fmt.Errorf("remote replication repository configuration is unavailable: %w", kernel.ErrStore)
		}
		configuration, err := s.Repositories.Get(ctx, repository.Key)
		if err != nil {
			return fmt.Errorf("read remote replication source: %w", err)
		}
		if in.URL != nil && *in.URL != "" && *in.URL != configuration.URL {
			return fmt.Errorf("pull replication uses the remote repository upstream URL: %w", kernel.ErrInvalid)
		}
		next.URL = configuration.URL
	}
	if err := validateURL(next.URL); err != nil {
		return err
	}
	if next.CronExp != "" {
		expression, err := schedule.Parse(next.CronExp, s.Zone)
		if err != nil {
			return fmt.Errorf("replication cronExp: %w", err)
		}
		next.CronExp = expression.String()
	}
	if next.Proxy != "" {
		if s.ProxyExists == nil {
			return fmt.Errorf("replication proxy lookup is unavailable: %w", kernel.ErrStore)
		}
		if err := s.ProxyExists(ctx, next.Proxy); err != nil {
			if errors.Is(err, kernel.ErrNotFound) {
				return fmt.Errorf("replication proxy does not exist: %w", kernel.ErrInvalid)
			}
			return fmt.Errorf("validate replication proxy: %w", err)
		}
	}
	return nil
}

func validateURL(raw string) error {
	u, err := url.Parse(raw)
	if err != nil || u.Scheme != "http" && u.Scheme != "https" || u.Hostname() == "" || u.User != nil || u.Fragment != "" || u.RawQuery != "" {
		// Do not return the parser's error: it can echo embedded credentials.
		return fmt.Errorf("replication url must be an absolute HTTP(S) repository URL without credentials, query or fragment: %w", kernel.ErrInvalid)
	}
	return nil
}
