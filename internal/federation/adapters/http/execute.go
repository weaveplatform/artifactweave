package federationhttp

import (
	"bytes"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/http"
	"strings"

	artgen "github.com/weaveplatform/artifactweave/internal/platform/gen/artifactory"
	"github.com/weaveplatform/artifactweave/internal/platform/httpkit"
	"github.com/weaveplatform/artifactweave/internal/platform/httpkit/apierror"
	"github.com/weaveplatform/artifactweave/internal/platform/httpkit/middleware"
)

func executionBody(body io.Reader) error {
	if body == nil {
		return nil
	}
	decoder := json.NewDecoder(body)
	decoder.DisallowUnknownFields()
	var entries []json.RawMessage
	err := decoder.Decode(&entries)
	var oversized *http.MaxBytesError
	if errors.As(err, &oversized) {
		return fmt.Errorf("replication execution body limit: %w", middleware.BodyTooLarge(oversized.Limit))
	}
	if errors.Is(err, io.EOF) {
		return nil
	} else if err != nil || entries == nil || len(entries) > 1 {
		return httpkit.BadRequestf("replication execution requires an array with at most one target")
	}
	if err := decoder.Decode(new(any)); !errors.Is(err, io.EOF) {
		if errors.As(err, &oversized) {
			return fmt.Errorf("replication execution body limit: %w", middleware.BodyTooLarge(oversized.Limit))
		}
		return httpkit.BadRequestf("invalid trailing replication execution body")
	}
	if len(entries) == 1 {
		if !object(entries[0]) {
			return httpkit.BadRequestf("a replication target must be an object")
		}
		var options artgen.ReplicationRequest
		entryDecoder := json.NewDecoder(bytes.NewReader(entries[0]))
		entryDecoder.DisallowUnknownFields()
		if err := entryDecoder.Decode(&options); err != nil {
			return httpkit.BadRequestf("invalid replication execution options")
		}
		var fields map[string]json.RawMessage
		if err := json.Unmarshal(entries[0], &fields); err != nil {
			return httpkit.BadRequestf("invalid replication execution options")
		}
		// Presence matters, including explicit null: acknowledging a supplied
		// override would otherwise silently execute different stored options.
		if len(fields) != 0 {
			return apierror.ErrNotImplemented
		}
	}
	return nil
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
	if err := executionBody(r.Body); err != nil {
		httpkit.WriteErr(w, r, err)
		return
	}
	if _, err := s.Executions.Execute(r.Context(), key, root); err != nil {
		httpkit.WriteErr(w, r, replicationError(r.Context(), err))
		return
	}
	w.WriteHeader(http.StatusAccepted)
}
