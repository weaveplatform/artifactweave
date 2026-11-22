package app

import (
	"bytes"
	"context"
	"encoding/base64"
	"encoding/json"
	"fmt"
	"io"
	"log/slog"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/weaveplatform/artifactweave/internal/platform/logging"
	"github.com/weaveplatform/artifactweave/internal/platform/sql/sqltest"
)

type loggerOutput struct {
	mu   sync.Mutex
	data bytes.Buffer
}

func TestAccRouterLoggerLifecycle(t *testing.T) {
	base := slog.New(slog.NewJSONHandler(io.Discard, &slog.HandlerOptions{Level: slog.LevelWarn}))
	control := logging.New(base)
	logger := control.Logger("artifactweave.http")
	var closeRouter func()
	handler, _ := newStatusFixture(t, false, func(cfg *RouterConfig) {
		cfg.Logger, cfg.Logging = base, control
		cfg.OnClose = func(close func()) {
			closeRouter = close
			t.Cleanup(close)
		}
	})
	if closeRouter == nil {
		t.Fatal("standalone router did not register configuration cleanup")
	}
	headers := basic("admin", "pw")
	headers["Content-Type"] = "application/json"
	wantStatus(t, do(t, handler, http.MethodPost, "/artifactory/api/system/debug/loggers", headers,
		`{"classes":{"artifactweave.http":"debug"},"timer":3600}`), http.StatusOK)
	if !logger.Enabled(t.Context(), slog.LevelDebug) {
		t.Fatal("standalone router did not change actual log decisions")
	}
	closeRouter()
	if logger.Enabled(t.Context(), slog.LevelDebug) {
		t.Fatal("standalone router cleanup retained the temporary session")
	}
}

func (o *loggerOutput) Write(p []byte) (int, error) {
	o.mu.Lock()
	defer o.mu.Unlock()
	return o.data.Write(p)
}
func (o *loggerOutput) String() string { o.mu.Lock(); defer o.mu.Unlock(); return o.data.String() }
func (o *loggerOutput) Reset()         { o.mu.Lock(); defer o.mu.Unlock(); o.data.Reset() }

func TestAccRuntimeLoggerControls(t *testing.T) {
	for _, driver := range []string{"sqlite", "postgres"} {
		t.Run(driver, func(t *testing.T) {
			cfg := testConfig(t)
			cfg.Bootstrap.AdminPassword = "logger-fixture-password"
			if driver == "postgres" {
				cfg.Database.Driver = driver
				cfg.Database.DSN = sqltest.ScratchDSN(t, sqltest.PostgresDSN(t))
			}
			var output loggerOutput
			base := slog.New(slog.NewJSONHandler(&output, &slog.HandlerOptions{Level: slog.LevelWarn}))
			rt, err := OpenRuntime(t.Context(), cfg, base, RuntimeOptions{})
			if err != nil {
				t.Fatal(err)
			}
			closed := false
			closeRuntime := func() {
				if !closed {
					closed = true
					if err := rt.Close(context.Background()); err != nil {
						t.Error(err)
					}
				}
			}
			t.Cleanup(closeRuntime)
			server := httptest.NewServer(
				NewRouter(
					RouterConfig{
						Store:         rt.Store,
						Blobs:         rt.Blobs,
						Auth:          rt.Auth,
						Tokens:        rt.Tokens,
						Access:        rt.modules.access.API(),
						Configuration: rt.modules.configuration.API(),
						Logging:       rt.Logging,
						Logger:        base,
					},
				),
			)
			t.Cleanup(server.Close)
			const path = "/artifactory/api/system/debug/loggers"
			request := func(method, path, body string, want int) string {
				t.Helper()
				req, err := http.NewRequestWithContext(t.Context(), method, server.URL+path, strings.NewReader(body))
				if err != nil {
					t.Fatal(err)
				}
				req.SetBasicAuth("admin", cfg.Bootstrap.AdminPassword)
				req.Header.Set("Content-Type", "application/json")
				response, err := server.Client().Do(req)
				if err != nil {
					t.Fatal(err)
				}
				data, err := io.ReadAll(response.Body)
				_ = response.Body.Close()
				if err != nil {
					t.Fatal(err)
				}
				if response.StatusCode != want {
					t.Fatalf("%s %s = %d; want %d: %s", method, path, response.StatusCode, want, data)
				}
				return strings.TrimSpace(string(data))
			}
			output.Reset()
			request("GET", "/artifactory/api/system/version", "", 200)
			if strings.Contains(output.String(), `"msg":"request"`) {
				t.Fatal("baseline warn emitted info access log")
			}
			request("POST", path, `{"classes":{"artifactweave.http":"debug"},"timer":60}`, 200)
			if got := request("GET", path+"/artifactweave.http", "", 200); got != `"debug"` {
				t.Fatal(got)
			}
			if got := request("GET", path+"/unknown", "", 200); got != `"warn"` {
				t.Fatal("unknown logger baseline", got)
			}
			output.Reset()
			const secret = "logger-query-fixture-secret"
			request("GET", "/artifactory/api/system/version?token="+secret, "", 200)
			if !strings.Contains(output.String(), `"logger":"artifactweave.http"`) || !strings.Contains(output.String(), `"msg":"request"`) {
				t.Fatal("temporary level did not change actual app output")
			}
			for _, credential := range []string{secret, cfg.Bootstrap.AdminPassword, base64.StdEncoding.EncodeToString([]byte("admin:" + cfg.Bootstrap.AdminPassword))} {
				if strings.Contains(output.String(), credential) {
					t.Fatal("credential leaked in debug output")
				}
			}
			request("POST", path, `{"classes":{"artifactweave.http":"error"},"timer":60}`, 400)
			if got := request("GET", path+"/artifactweave.http", "", 200); got != `"debug"` {
				t.Fatal("conflicting POST changed current level", got)
			}
			request("DELETE", path, "", 200)
			output.Reset()
			request("GET", "/artifactory/api/system/version", "", 200)
			if output.String() != "" {
				t.Fatal("DELETE did not restore output threshold")
			}
			for _, body := range []string{`{"classes":{},"timer":60}`, `{"classes":{"artifactweave.http":"bogus"},"timer":60}`, `{"classes":{"artifactweave.http":"debug"},"timer":-1}`} {
				request("POST", path, body, 400)
			}
			classes := make(map[string]string, 101)
			for i := range 101 {
				classes[fmt.Sprintf("unknown.%d", i)] = "debug"
			}
			payload, _ := json.Marshal(map[string]any{"classes": classes, "timer": 60})
			request("POST", path, string(payload), 400)
			delete(classes, "unknown.100")
			payload, _ = json.Marshal(map[string]any{"classes": classes, "timer": 60})
			request("POST", path, string(payload), 200)
			if got := request("GET", path+"/artifactweave.http", "", 200); got != `"warn"` {
				t.Fatal("unknown names changed output", got)
			}
			request("DELETE", path, "", 200)
			request("POST", path, `{"classes":{"artifactweave.http":"debug"},"timer":1}`, 200)
			deadline := time.Now().Add(5 * time.Second)
			for request("GET", path+"/artifactweave.http", "", 200) != `"warn"` {
				if time.Now().After(deadline) {
					t.Fatal("runtime timer did not expire")
				}
				time.Sleep(10 * time.Millisecond)
			}
			request("POST", path, `{"classes":{"artifactweave.http":"debug"},"timer":3600}`, 200)
			server.Close()
			closeRuntime()
			if rt.Logging.Logger("artifactweave.http").Enabled(t.Context(), slog.LevelDebug) {
				t.Fatal("runtime Close retained override")
			}
		})
	}
}
