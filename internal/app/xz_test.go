package app

import (
	"bytes"
	"context"
	"errors"
	"io"
	"net/http"
	"net/http/httptest"
	"testing"
	"time"

	"github.com/ulikunitz/xz"

	"github.com/weaveplatform/artifactweave/internal/platform/xzfile"
)

// Behind xzRequests a request's xz readers are one holder: with a first
// file open and holding the whole budget, a second one goes in. Without
// it the second waits for the first, which only the waiting request can
// close.
func TestUnitXZRequestsMakeARequestOneHolder(t *testing.T) {
	xzfile.SetBudget(1 << 20)
	t.Cleanup(func() { xzfile.SetBudget(xzfile.DefaultBudget) })
	var file bytes.Buffer
	w, err := xz.WriterConfig{DictCap: 1 << 20}.NewWriter(&file)
	if err != nil {
		t.Fatal(err)
	}
	content := bytes.Repeat([]byte("xz "), 100)
	if _, err = w.Write(content); err != nil || w.Close() != nil {
		t.Fatal(err)
	}
	var second error
	handler := http.HandlerFunc(func(_ http.ResponseWriter, r *http.Request) {
		first, err := xzfile.NewReader(r.Context(), bytes.NewReader(file.Bytes()), 0)
		if err != nil {
			second = err
			return
		}
		defer func() { _ = first.Close() }()
		if _, second = io.ReadFull(first, make([]byte, 1)); second != nil {
			return
		}
		ctx, cancel := context.WithTimeout(r.Context(), 200*time.Millisecond)
		defer cancel()
		r2, err := xzfile.NewReader(ctx, bytes.NewReader(file.Bytes()), 0)
		if err != nil {
			second = err
			return
		}
		defer func() { _ = r2.Close() }()
		var got []byte
		if got, second = io.ReadAll(r2); second == nil && !bytes.Equal(got, content) {
			second = errors.New("the second file read wrong")
		}
	})
	xzRequests(
		handler,
	).ServeHTTP(httptest.NewRecorder(), httptest.NewRequest(http.MethodGet, "/", nil))
	if second != nil {
		t.Fatalf("a request's second xz file: %v", second)
	}
	handler.ServeHTTP(httptest.NewRecorder(), httptest.NewRequest(http.MethodGet, "/", nil))
	if !errors.Is(second, context.DeadlineExceeded) {
		t.Fatalf("without xzRequests the second file did not wait: %v", second)
	}
}
