package pkgroute_test

import (
	"context"
	"errors"
	"io"
	"net/http"
	"strings"
	"testing"

	"github.com/weaveplatform/artifactweave/internal/artifacts/adapters/http/pkgroute"
	"github.com/weaveplatform/artifactweave/internal/artifacts/core/format"
	"github.com/weaveplatform/artifactweave/internal/platform/httpkit/apierror"
)

var errRead = errors.New("the store dropped the connection")

type failedRead struct{}

func (failedRead) Read([]byte) (int, error) { return 0, errRead }

// A served copy that reads is returned whole, or one byte over limit so the
// caller can tell; one whose read fails is the server's storage fault,
// which SourceError leaves for the router's 500; and one whose read fails
// because the request ended is the route's own answer (#1462).
func TestUnitReadServed(t *testing.T) {
	ctx := t.Context()
	if data, err := pkgroute.ReadServed(ctx, strings.NewReader("0123456789"), 4, nil); err != nil ||
		string(data) != "01234" {
		t.Fatalf("over the limit: %q %v", data, err)
	}
	otherwise := apierror.New(http.StatusBadGateway, "invalid upstream metadata")
	_, err := pkgroute.ReadServed(ctx, failedRead{}, 4, otherwise)
	if !errors.Is(err, format.ErrStorageFault) || !errors.Is(err, errRead) ||
		!errors.Is(pkgroute.SourceError(err, false), format.ErrStorageFault) {
		t.Fatalf("failed read: %v", err)
	}
	done, cancel := context.WithCancel(ctx)
	cancel()
	if _, err = pkgroute.ReadServed(done, failedRead{}, 4, otherwise); !errors.Is(err, otherwise) {
		t.Fatalf("ended request: %v", err)
	}
	if err = pkgroute.ServedReadFault(
		done,
		io.ErrUnexpectedEOF,
	); err != io.ErrUnexpectedEOF { //nolint:errorlint // the error itself
		t.Fatalf("ended request's fault: %v", err)
	}
}
