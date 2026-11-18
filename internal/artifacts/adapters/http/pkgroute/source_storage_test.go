package pkgroute_test

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/http"
	"net/http/httptest"
	"reflect"
	"testing"
	"time"

	"github.com/weaveplatform/artifactweave/internal/artifacts/adapters/http/pkgroute"
	"github.com/weaveplatform/artifactweave/internal/artifacts/core/format"
	"github.com/weaveplatform/artifactweave/internal/artifacts/core/remote/source"
	"github.com/weaveplatform/artifactweave/internal/platform/httpkit"
	"github.com/weaveplatform/artifactweave/internal/platform/httpkit/apierror"
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

func TestUnitContentOpenMarkerKeepsOrdinarySourceHTTPAnswers(t *testing.T) {
	for _, cause := range []error{fmt.Errorf("cached file: %w", format.ErrStorageFault), source.ErrUnavailable, context.DeadlineExceeded, &source.HTTPError{Status: 503, RetryAfter: time.Second}} {
		ordinary, marked := httptest.NewRecorder(), httptest.NewRecorder()
		apierror.Write(ordinary, apierror.ProfileArtifactory, pkgroute.SourceError(cause, false))
		apierror.Write(marked, apierror.ProfileArtifactory, pkgroute.SourceError(&source.ContentOpenError{Err: cause}, false))
		if ordinary.Code != marked.Code || ordinary.Body.String() != marked.Body.String() || !reflect.DeepEqual(ordinary.Header(), marked.Header()) {
			t.Fatal("content marker changed ordinary source HTTP answer", ordinary, marked)
		}
	}
}
