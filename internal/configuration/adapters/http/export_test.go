package configurationhttp

import (
	"context"
	"errors"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"github.com/weaveplatform/artifactweave/internal/app/adapters/inmem"
	configapi "github.com/weaveplatform/artifactweave/internal/configuration/api"
	"github.com/weaveplatform/artifactweave/internal/kernel"
)

type stubExport struct {
	body  []byte
	err   error
	calls int
}

func (e *stubExport) XML(context.Context) ([]byte, error) { e.calls++; return e.body, e.err }

func TestAccConfigurationExportPublishesOnlyCompleteXML(t *testing.T) {
	for _, name := range []string{"success", "read failure", "no store", "no export binding", "anonymous", "user"} {
		t.Run(name, func(t *testing.T) {
			export := &stubExport{body: []byte(`<config><repoLayouts/></config>`)}
			svc := New(Deps{Store: inmem.New(), Configuration: configapi.API{Export: export}})
			ctx := kernel.WithPrincipal(t.Context(), kernel.Principal{Name: "admin", Admin: true})
			want, calls := http.StatusOK, 1
			switch name {
			case "read failure":
				export.err = errors.New("private password fixture in store failure")
				want = 500
			case "no store":
				svc = New(Deps{})
				want, calls = 501, 0
			case "no export binding":
				svc = New(Deps{Store: inmem.New()})
				want, calls = 501, 0
			case "anonymous":
				ctx = kernel.WithPrincipal(t.Context(), kernel.Principal{Name: "anonymous", Anonymous: true})
				want, calls = 401, 0
			case "user":
				ctx = kernel.WithPrincipal(t.Context(), kernel.Principal{Name: "user"})
				want, calls = 403, 0
			}
			rec := httptest.NewRecorder()
			req := httptest.NewRequestWithContext(ctx, http.MethodGet, configPath, nil)
			svc.GetConfiguration(rec, req)
			if rec.Code != want || export.calls != calls {
				t.Fatalf("status/calls %d/%d; want %d/%d: %s", rec.Code, export.calls, want, calls, rec.Body)
			}
			if want == 200 {
				if rec.Header().Get("Content-Type") != "application/xml" || rec.Body.String() != string(export.body) {
					t.Fatal("wrong XML response", rec.Header(), rec.Body)
				}
			} else if strings.Contains(rec.Body.String(), "<config>") || strings.Contains(rec.Body.String(), "private password") {
				t.Fatal("partial XML or private failure exposed", rec.Body)
			}
		})
	}
}
