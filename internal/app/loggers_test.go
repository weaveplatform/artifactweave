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

	accessapi "github.com/weaveplatform/artifactweave/internal/access/api"
	"github.com/weaveplatform/artifactweave/internal/access/api/accessapitest"
	"github.com/weaveplatform/artifactweave/internal/configuration"
	configapi "github.com/weaveplatform/artifactweave/internal/configuration/api"
	"github.com/weaveplatform/artifactweave/internal/kernel"
	"github.com/weaveplatform/artifactweave/internal/platform/logging"
	"github.com/weaveplatform/artifactweave/internal/platform/sql/sqltest"
	artclient "github.com/weaveplatform/artifactweave/pkg/client/artifactory"
)

type loggerOutput struct {
	mu   sync.Mutex
	data bytes.Buffer
}

func TestAccRouterLoggerLifecycle(t *testing.T) {
	for _, mode := range []string{"owned", "unregistered", "injected", "injected partial"} {
		t.Run(mode, func(t *testing.T) {
			base := slog.New(slog.NewJSONHandler(io.Discard, &slog.HandlerOptions{Level: slog.LevelWarn}))
			logger := base
			var control *logging.Control
			if mode != "unregistered" {
				control = logging.New(base)
				logger = control.Logger("artifactweave.http")
			}
			var closes []func()
			var injected *configuration.Module
			handler, _ := newStatusFixture(t, false, func(cfg *RouterConfig) {
				cfg.Logger, cfg.Logging = base, control
				cfg.OnClose = nil
				if mode == "owned" || mode == "injected partial" {
					cfg.OnClose = func(close func()) {
						closes = append(closes, close)
						t.Cleanup(close)
					}
				}
				if strings.HasPrefix(mode, "injected") {
					var err error
					injected, err = configuration.New(configuration.Deps{Store: cfg.Store, Logging: control, Access: accessapi.API{
						Projects: &accessapitest.Projects{GuardFunc: func(context.Context) error { return nil }},
						Authenticator: &accessapitest.Authenticator{CurrentFunc: func(_ context.Context, p kernel.Principal) (kernel.Principal, error) {
							return p, nil
						}},
						Authorizer: &accessapitest.Authorizer{DecideFunc: func(context.Context, accessapi.Subject, bool) (accessapi.Decider, error) {
							return &accessapitest.Decider{Admin: true}, nil
						}},
					}})
					if err != nil {
						t.Fatal(err)
					}
					t.Cleanup(injected.Close)
					cfg.Configuration = injected.API()
					if mode == "injected partial" {
						cfg.Configuration = configapi.API{Loggers: cfg.Configuration.Loggers}
					}
				}
			})
			server := httptest.NewServer(handler)
			t.Cleanup(server.Close)
			client := newLoggerClient(t, server, "pw")
			classes := map[string]artclient.LoggerLevel{"artifactweave.http": artclient.LoggerLevelDebug}
			if mode == "unregistered" {
				response, err := client.SetLoggerDebugLevelsWithResponse(t.Context(), artclient.SetLoggerDebugLevelsJSONRequestBody{Classes: classes, Timer: 3600})
				if err != nil {
					t.Fatal(err)
				}
				if response.StatusCode() != http.StatusNotImplemented {
					if response.JSON200 != nil {
						clearLoggerLevels(t, client)
					}
					t.Fatalf("unowned logger POST = %d; want 501", response.StatusCode())
				}
				level, err := client.GetLoggerDebugLevelWithResponse(t.Context(), "artifactweave.http")
				if err != nil || level.StatusCode() != http.StatusNotImplemented {
					t.Fatal("unowned logger GET was advertised", level, err)
				}
				deleted, err := client.DeleteLoggerDebugLevelsWithResponse(t.Context())
				if err != nil || deleted.StatusCode() != http.StatusNotImplemented {
					t.Fatal("unowned logger DELETE was advertised", deleted, err)
				}
				server.Close()
				server.Close()
				if logger.Enabled(t.Context(), slog.LevelDebug) || len(closes) != 0 {
					t.Fatal("zero-OnClose router retained logger work")
				}
				return
			}
			if mode == "owned" {
				if len(closes) != 1 {
					t.Fatal("standalone router did not register configuration cleanup")
				}
				setLoggerLevels(t, client, classes, 1)
				if got := getLoggerLevel(t, client, "artifactweave.http"); got != "debug" {
					t.Fatal(got)
				}
				deadline := time.Now().Add(5 * time.Second)
				for getLoggerLevel(t, client, "artifactweave.http") != "warn" {
					if time.Now().After(deadline) {
						t.Fatal("standalone router session did not expire")
					}
					time.Sleep(10 * time.Millisecond)
				}
			}
			setLoggerLevels(t, client, classes, 3600)
			if !logger.Enabled(t.Context(), slog.LevelDebug) {
				t.Fatal("router did not change actual log decisions")
			}
			server.Close()
			for _, close := range closes {
				close()
				close()
			}
			if injected != nil {
				if !logger.Enabled(t.Context(), slog.LevelDebug) {
					t.Fatal("router closed an externally owned logger session")
				}
				injected.Close()
				injected.Close()
			}
			if logger.Enabled(t.Context(), slog.LevelDebug) {
				t.Fatal("owning lifecycle retained the temporary session")
			}
		})
	}
}

func newLoggerClient(t *testing.T, server *httptest.Server, password string) *artclient.ClientWithResponses {
	t.Helper()
	client, err := artclient.NewClientWithResponses(server.URL+"/artifactory", artclient.WithHTTPClient(server.Client()),
		artclient.WithRequestEditorFn(func(_ context.Context, req *http.Request) error {
			req.SetBasicAuth("admin", password)
			return nil
		}))
	if err != nil {
		t.Fatal(err)
	}
	return client
}

func setLoggerLevels(t *testing.T, client *artclient.ClientWithResponses, classes map[string]artclient.LoggerLevel, seconds int) {
	t.Helper()
	response, err := client.SetLoggerDebugLevelsWithResponse(t.Context(), artclient.SetLoggerDebugLevelsJSONRequestBody{Classes: classes, Timer: seconds})
	if err != nil {
		t.Fatal(err)
	}
	if response.StatusCode() != http.StatusOK || response.JSON200 == nil || *response.JSON200 != "Logger levels configured successfully" {
		t.Fatalf("generated logger POST = %d %s", response.StatusCode(), response.Body)
	}
}

func clearLoggerLevels(t *testing.T, client *artclient.ClientWithResponses) {
	t.Helper()
	response, err := client.DeleteLoggerDebugLevelsWithResponse(t.Context())
	if err != nil {
		t.Fatal(err)
	}
	if response.StatusCode() != http.StatusOK || response.JSON200 == nil || *response.JSON200 != "Logger levels were reset to default settings successfully" {
		t.Fatalf("generated logger DELETE = %d %s", response.StatusCode(), response.Body)
	}
}

func getLoggerLevel(t *testing.T, client *artclient.ClientWithResponses, name string) string {
	t.Helper()
	response, err := client.GetLoggerDebugLevelWithResponse(t.Context(), name)
	if err != nil {
		t.Fatal(err)
	}
	if response.StatusCode() != http.StatusOK || response.JSON200 == nil {
		t.Fatalf("generated logger GET = %d %s", response.StatusCode(), response.Body)
	}
	return string(*response.JSON200)
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
			client := newLoggerClient(t, server, cfg.Bootstrap.AdminPassword)
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
			debug := map[string]artclient.LoggerLevel{"artifactweave.http": artclient.LoggerLevelDebug}
			setLoggerLevels(t, client, debug, 60)
			if got := getLoggerLevel(t, client, "artifactweave.http"); got != "debug" {
				t.Fatal(got)
			}
			if got := getLoggerLevel(t, client, "unknown"); got != "warn" {
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
			if got := getLoggerLevel(t, client, "artifactweave.http"); got != "debug" {
				t.Fatal("conflicting POST changed current level", got)
			}
			clearLoggerLevels(t, client)
			output.Reset()
			request("GET", "/artifactory/api/system/version", "", 200)
			if output.String() != "" {
				t.Fatal("DELETE did not restore output threshold")
			}
			for _, body := range []string{`{"classes":{},"timer":60}`, `{"classes":{"artifactweave.http":"bogus"},"timer":60}`, `{"classes":{"artifactweave.http":"debug"},"timer":-1}`} {
				request("POST", path, body, 400)
			}
			classes := make(map[string]artclient.LoggerLevel, 101)
			for i := range 101 {
				classes[fmt.Sprintf("unknown.%d", i)] = artclient.LoggerLevelDebug
			}
			payload, err := json.Marshal(map[string]any{"classes": classes, "timer": 60})
			if err != nil {
				t.Fatal(err)
			}
			request("POST", path, string(payload), 400)
			delete(classes, "unknown.100")
			setLoggerLevels(t, client, classes, 60)
			if got := getLoggerLevel(t, client, "artifactweave.http"); got != "warn" {
				t.Fatal("unknown names changed output", got)
			}
			clearLoggerLevels(t, client)
			setLoggerLevels(t, client, debug, 1)
			deadline := time.Now().Add(5 * time.Second)
			for getLoggerLevel(t, client, "artifactweave.http") != "warn" {
				if time.Now().After(deadline) {
					t.Fatal("runtime timer did not expire")
				}
				time.Sleep(10 * time.Millisecond)
			}
			setLoggerLevels(t, client, debug, 3600)
			server.Close()
			closeRuntime()
			if rt.Logging.Logger("artifactweave.http").Enabled(t.Context(), slog.LevelDebug) {
				t.Fatal("runtime Close retained override")
			}
		})
	}
}
