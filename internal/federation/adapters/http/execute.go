package federationhttp

import (
	"bytes"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/http"
	"strings"

	"github.com/weaveplatform/artifactweave/internal/federation/core/replication"
	artgen "github.com/weaveplatform/artifactweave/internal/platform/gen/artifactory"
	"github.com/weaveplatform/artifactweave/internal/platform/httpkit"
	"github.com/weaveplatform/artifactweave/internal/platform/httpkit/apierror"
	"github.com/weaveplatform/artifactweave/internal/platform/httpkit/middleware"
)

func executionBody(body io.Reader) (*replication.ExecutionInput, error) {
	if body == nil {
		return nil, nil //nolint:nilnil // No options object selects the saved configuration.
	}
	decoder := json.NewDecoder(body)
	decoder.DisallowUnknownFields()
	var entries []json.RawMessage
	err := decoder.Decode(&entries)
	var oversized *http.MaxBytesError
	if errors.As(err, &oversized) {
		return nil, fmt.Errorf("replication execution body limit: %w", middleware.BodyTooLarge(oversized.Limit))
	}
	if errors.Is(err, io.EOF) {
		return nil, nil //nolint:nilnil // No options object selects the saved configuration.
	} else if err != nil || entries == nil || len(entries) > 1 {
		return nil, httpkit.BadRequestf("replication execution requires an array with at most one target")
	}
	if err := decoder.Decode(new(any)); !errors.Is(err, io.EOF) {
		if errors.As(err, &oversized) {
			return nil, fmt.Errorf("replication execution body limit: %w", middleware.BodyTooLarge(oversized.Limit))
		}
		return nil, httpkit.BadRequestf("invalid trailing replication execution body")
	}
	if len(entries) == 1 {
		if !object(entries[0]) {
			return nil, httpkit.BadRequestf("a replication target must be an object")
		}
		var options artgen.ReplicationRequest
		entryDecoder := json.NewDecoder(bytes.NewReader(entries[0]))
		entryDecoder.DisallowUnknownFields()
		if err := entryDecoder.Decode(&options); err != nil {
			return nil, httpkit.BadRequestf("invalid replication execution options")
		}
		var fields map[string]json.RawMessage
		if err := json.Unmarshal(entries[0], &fields); err != nil {
			return nil, httpkit.BadRequestf("invalid replication execution options")
		}
		for _, value := range fields {
			if bytes.Equal(bytes.TrimSpace(value), []byte("null")) {
				return nil, httpkit.BadRequestf("replication execution options cannot be null")
			}
		}
		return &replication.ExecutionInput{
			URL: options.Url, Username: options.Username, Password: options.Password,
			Proxy: options.Proxy, DisableProxy: options.DisableProxy,
			Include: options.IncludePathPrefixPattern, Exclude: options.ExcludePathPrefixPattern,
			SyncDeletes: options.Delete, SyncProperties: options.Properties, CheckBinaryExistence: options.CheckBinaryExistenceInFilestore,
		}, nil
	}
	return nil, nil //nolint:nilnil // No options object selects the saved configuration.
}

func (s ReplicationAPI) execute(w http.ResponseWriter, r *http.Request) {
	if s.Executions == nil {
		httpkit.WriteErr(w, r, apierror.ErrNotImplemented)
		return
	}
	key, root, err := replicationPath(strings.TrimPrefix(r.URL.EscapedPath(), "/artifactory/api/replication/execute/"))
	if err != nil {
		httpkit.WriteErr(w, r, err)
		return
	}
	input, err := executionBody(r.Body)
	if err != nil {
		httpkit.WriteErr(w, r, err)
		return
	}
	if input == nil {
		_, err = s.Executions.Execute(r.Context(), key, root)
	} else {
		_, err = s.Executions.ExecuteWithOptions(r.Context(), key, root, *input)
	}
	if err != nil {
		httpkit.WriteErr(w, r, replicationError(r.Context(), err))
		return
	}
	w.WriteHeader(http.StatusAccepted)
}
