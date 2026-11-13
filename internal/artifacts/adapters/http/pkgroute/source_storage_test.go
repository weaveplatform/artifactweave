package pkgroute_test

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/http"
	"net/http/httptest"
	"testing"

	"github.com/weaveplatform/artifactweave/internal/artifacts/adapters/http/pkgroute"
	"github.com/weaveplatform/artifactweave/internal/artifacts/core/format"
	"github.com/weaveplatform/artifactweave/internal/platform/httpkit"
)

// A failed read of the server's own copy under check is the router's
// internal fault, 500 "internal error", whatever the store's error wraps:
// a deadline, a cancellation or a short read the source errors otherwise
// answer 504 or 502.
func TestAccSourceErrorAnswersAStorageFault500(t *testing.T) {
	errDropped := errors.New("the store dropped the connection")
	for name, read := range map[string]error{
		"dropped":  errDropped,
		"deadline": fmt.Errorf("%w: %w", errDropped, context.DeadlineExceeded),
		"canceled": fmt.Errorf("%w: %w", errDropped, context.Canceled),
		"short":    fmt.Errorf("%w: %w", errDropped, io.ErrUnexpectedEOF),
	} {
		fault := fmt.Errorf("cache check: %w: %w", format.ErrStorageFault, read)
		got := pkgroute.SourceError(fault, false)
		if got != fault {
			t.Fatalf("%s: %v", name, got)
		}
		res := httptest.NewRecorder()
		httpkit.WriteErr(res, httptest.NewRequest(http.MethodGet, "/", nil), got)
		var body struct {
			Errors []struct {
				Status  int    `json:"status"`
				Message string `json:"message"`
			} `json:"errors"`
		}
		if err := json.Unmarshal(res.Body.Bytes(), &body); err != nil {
			t.Fatal(err)
		}
		if res.Code != http.StatusInternalServerError || len(body.Errors) != 1 ||
			body.Errors[0].Status != 500 || body.Errors[0].Message != "internal error" {
			t.Fatalf("%s: %d %s", name, res.Code, res.Body)
		}
	}
}
