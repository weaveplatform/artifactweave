package docker

import (
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"github.com/weaveplatform/artifactweave/internal/format"
)

// A format's refusal keeps its status and message under the registry code
// a client reads for it, and is no fault for the access log.
func TestRejectionsKeepTheirStatus(t *testing.T) {
	faults := 0
	g := &Registry{fault: func(*http.Request, error) { faults++ }}
	for _, c := range []struct {
		status int
		code   string
	}{
		{http.StatusNotFound, CodeNameUnknown},
		{http.StatusConflict, CodeDenied},
	} {
		rec := httptest.NewRecorder()
		g.writeErr(rec, httptest.NewRequest(http.MethodPut, "/v2/app/manifests/v1", nil),
			format.Reject(c.status, "refused %d", c.status))
		if rec.Code != c.status || !strings.Contains(rec.Body.String(), `"code":"`+c.code+`"`) ||
			!strings.Contains(rec.Body.String(), "refused") {
			t.Errorf("%d: %d %s", c.status, rec.Code, rec.Body)
		}
	}
	if faults != 0 {
		t.Errorf("%d rejections were logged as faults", faults)
	}
}
