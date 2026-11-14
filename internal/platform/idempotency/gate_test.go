package idempotency_test

import (
	"context"
	"errors"
	"net/http"
	"net/http/httptest"
	"slices"
	"strings"
	"testing"

	"github.com/weaveplatform/artifactweave/internal/platform/gen/operations"
	"github.com/weaveplatform/artifactweave/internal/platform/httpkit"
	"github.com/weaveplatform/artifactweave/internal/platform/httpkit/apierror"
	"github.com/weaveplatform/artifactweave/internal/platform/idempotency"
)

const (
	appendOnly = `{"errors":[{"message":"Idempotency-Key is supported only once, on build append","status":400}]}` + "\n"
	rule       = `{"errors":[{"message":"Idempotency-Key is supported only once, on an operation whose API description declares it","status":400}]}` + "\n"
)

// gated sends method and target through the gate over tbl with the given
// key values, behind the Artifactory error envelope, and returns the answer;
// 204 means the gate let the request through.
func gated(
	t *testing.T,
	tbl []operations.Operation,
	method, target string,
	keys ...string,
) *httptest.ResponseRecorder {
	t.Helper()
	gate, err := idempotency.Gate(tbl)
	if err != nil {
		t.Fatal(err)
	}
	h := httpkit.ErrorProfiles(
		nil,
	)(
		gate(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
			w.WriteHeader(http.StatusNoContent)
		})),
	)
	r := httptest.NewRequestWithContext(context.Background(), method, target, nil)
	for _, k := range keys {
		r.Header.Add(idempotency.Header, k)
	}
	rec := httptest.NewRecorder()
	h.ServeHTTP(rec, r)
	return rec
}

// declared is the keyed set written out by hand, so that the table-driven
// gate is shown to accept a key on exactly the operations whose API
// description declares it: build append, promotion and both deletes, each
// with every path segment present.
func declared(r *http.Request) bool {
	values, present := r.Header[idempotency.Header]
	if !present {
		return true
	}
	path := r.URL.EscapedPath()
	segments := func(prefix string, n int) bool {
		if !strings.HasPrefix(path, prefix) {
			return false
		}
		parts := strings.Split(strings.TrimPrefix(path, prefix), "/")
		return len(parts) == n && !slices.Contains(parts, "")
	}
	var supported bool
	switch r.Method {
	case http.MethodPost:
		supported = segments("/artifactory/api/build/append/", 2) ||
			segments("/artifactory/api/build/promote/", 2) ||
			path == "/artifactory/api/build/delete"
	case http.MethodDelete:
		supported = segments("/artifactory/api/build/", 1)
	}
	return supported && len(values) == 1
}

func TestUnitGateAcceptsDeclaredOperations(t *testing.T) {
	targets := []string{
		"/artifactory/api/build/append/app/1",
		"/artifactory/api/build/append/team%2Fapp/1",
		"/artifactory/api/build/append/app/1?started=x&project=p",
		"/artifactory/api/build/append//1",
		"/artifactory/api/build/append/app/",
		"/artifactory/api/build/append/app/1/",
		"/artifactory/api/build/append/app/1/extra",
		"/artifactory/api/build/append/app",
		"/artifactory/api/build/append/",
		"/artifactory/api/build/append",
		"/artifactory/api/build",
		"/artifactory/api/build/",
		"/artifactory/api/build/app",
		"/artifactory/api/build/team%2Fapp?buildNumbers=1,2",
		"/artifactory/api/build/app/1",
		"/artifactory/api/build/delete",
		"/artifactory/api/build/delete/",
		"/artifactory/api/build/promote/app/1",
		"/artifactory/api/build/promote/app/1?project=p",
		"/artifactory/api/build/promote//1",
		"/artifactory/api/build/promote/app",
		"/artifactory/api/security/token",
		"/access/api/v1/tokens",
		"/artifactory/libs-local/a.jar",
		"/unknown",
	}
	for _, method := range []string{http.MethodPost, http.MethodPut, http.MethodGet, http.MethodDelete} {
		for _, target := range targets {
			for _, keys := range [][]string{nil, {`"k"`}, {`"a"`, `"b"`}, {"unquoted"}} {
				r := httptest.NewRequestWithContext(context.Background(), method, target, nil)
				for _, k := range keys {
					r.Header.Add(idempotency.Header, k)
				}
				rec := gated(t, operations.Table, method, target, keys...)
				if got := rec.Code == http.StatusNoContent; got != declared(r) {
					t.Fatalf("%s %s %q: passed %v, declared %v", method, target, keys, got, !got)
				}
				if rec.Code == http.StatusBadRequest && rec.Body.String() != rule {
					t.Fatalf("%s %s: %s", method, target, rec.Body)
				}
				if rec.Header().Get("RateLimit") != "" || rec.Header().Get("Retry-After") != "" {
					t.Fatalf(
						"%s %s: refusal carries rate headers: %v",
						method,
						target,
						rec.Header(),
					)
				}
			}
		}
	}
}

// The refusal names build append while it is the only keyed operation, and
// the rule as soon as it is not.
func TestUnitGateWording(t *testing.T) {
	appendRow := operations.Operation{
		Service: "artifactory", Method: http.MethodPost,
		Pattern:     "/artifactory/api/build/append/{buildName}/{buildNumber}",
		OperationID: "appendBuild", Idempotency: "key", IdempotencyKey: true,
	}
	promoteRow := operations.Operation{
		Service: "artifactory", Method: http.MethodPost,
		Pattern:     "/artifactory/api/build/promote/{buildName}/{buildNumber}",
		OperationID: "promoteBuild", Idempotency: "key", IdempotencyKey: true,
	}
	unkeyedAppend := appendRow
	unkeyedAppend.IdempotencyKey = false
	for _, c := range []struct {
		name string
		tbl  []operations.Operation
		want string
	}{
		{"append alone", []operations.Operation{appendRow, unkeyedPromote(promoteRow)}, appendOnly},
		{"append and promote", []operations.Operation{appendRow, promoteRow}, rule},
		{"promote alone", []operations.Operation{unkeyedAppend, promoteRow}, rule},
		{"none", []operations.Operation{unkeyedAppend}, rule},
	} {
		rec := gated(t, c.tbl, http.MethodGet, "/unknown", `"k"`)
		if rec.Code != http.StatusBadRequest || rec.Body.String() != c.want {
			t.Fatalf("%s: %d %s", c.name, rec.Code, rec.Body)
		}
	}
	// Both keyed operations accept a key once the second declares it.
	both := []operations.Operation{appendRow, promoteRow}
	for _, target := range []string{"/artifactory/api/build/append/a/1", "/artifactory/api/build/promote/a/1"} {
		if rec := gated(t, both, http.MethodPost, target, `"k"`); rec.Code != http.StatusNoContent {
			t.Fatalf("%s: %d %s", target, rec.Code, rec.Body)
		}
	}
}

func unkeyedPromote(op operations.Operation) operations.Operation {
	op.IdempotencyKey = false
	return op
}

// The refusal uses the error envelope the path's profile selects.
func TestUnitGateEnvelope(t *testing.T) {
	gate, err := idempotency.Gate(operations.Table)
	if err != nil {
		t.Fatal(err)
	}
	h := httpkit.ErrorProfiles([]httpkit.ProfileRule{{
		Match:   func(string) bool { return true },
		Profile: apierror.ProfileText,
	}})(gate(http.NotFoundHandler()))
	r := httptest.NewRequestWithContext(
		context.Background(),
		http.MethodPost,
		"/v2/x/blobs/uploads/",
		nil,
	)
	r.Header.Set(idempotency.Header, `"k"`)
	rec := httptest.NewRecorder()
	h.ServeHTTP(rec, r)
	if rec.Code != http.StatusBadRequest ||
		rec.Body.String() != "Idempotency-Key is supported only once, on an operation whose API description declares it\n" {
		t.Fatalf("text profile: %d %q", rec.Code, rec.Body)
	}
}

func TestUnitGateRefusesABadTable(t *testing.T) {
	row := operations.Operation{Method: http.MethodGet, Pattern: "/a", OperationID: "a"}
	if _, err := idempotency.Gate(
		[]operations.Operation{row, row},
	); !errors.Is(
		err,
		operations.ErrDuplicate,
	) {
		t.Fatalf("duplicate rows: %v", err)
	}
}
