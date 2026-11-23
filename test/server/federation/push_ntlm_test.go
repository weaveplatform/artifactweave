package federation_test

import (
	"net/http"
	"net/url"
	"strings"
	"sync/atomic"
	"testing"

	"github.com/weaveplatform/artifactweave/internal/artifacts/adapters/upstream/fakeupstream"
	"github.com/weaveplatform/artifactweave/internal/federation/core/model"
	"github.com/weaveplatform/artifactweave/internal/platform/sql/sqltest"
	"github.com/weaveplatform/artifactweave/test/server/support"
)

const ntlmPushSecret = "ntlm-push-proxy-secret"

func TestAccSinglePushNTLMProxySQLite(t *testing.T) {
	t.Parallel()
	qualifyNTLMPush(t, "sqlite")
}

func TestAccSinglePushNTLMProxyPostgres(t *testing.T) {
	t.Parallel()
	qualifyNTLMPush(t, "postgres")
}

// qualifyNTLMPush runs the real source runtime and separate target process
// through an independent NTLMv2 verifier configured with the public descriptor.
func qualifyNTLMPush(t *testing.T, backend string) {
	t.Helper()
	for _, explicit := range []bool{false, true} {
		name := "default"
		if explicit {
			name = "explicit"
		}
		t.Run(name, func(t *testing.T) {
			var proxy *fakeupstream.NTLMProxy
			via := func(bridge *pushBridge) []string {
				proxy = fakeupstream.NewNTLMProxy(t, fakeupstream.NTLMOptions{
					Username: "proxy-user", Password: ntlmPushSecret, Domain: "CORP", Workstation: "REPLICATOR", Origins: []string{bridge.URL},
				})
				return []string{proxy.URL}
			}
			var w *pushWorld
			if backend == "postgres" {
				dsn := sqltest.PostgresDSN(t)
				w = newPushWorldVia(t, backend, sqltest.ScratchDSN(t, dsn), sqltest.ScratchDSN(t, dsn), "generic", via)
			} else {
				w = newPushWorldVia(t, "sqlite", "", "", "generic", via)
			}
			address, err := url.Parse(proxy.URL)
			if err != nil {
				t.Fatal(err)
			}
			configureProxy := func(password string) {
				t.Helper()
				headers := support.Basic("admin", "pw")
				headers["Content-Type"] = "application/yaml"
				body := "proxies:\n  ntlm:\n    host: \"" + address.Hostname() + "\"\n    port: " + address.Port() +
					"\n    username: proxy-user\n    password: " + password + "\n    domain: CORP\n    ntHost: REPLICATOR\n" +
					"    redirectedToHosts: other.invalid\n    platformDefault: " + map[bool]string{true: "false", false: "true"}[explicit] + "\n"
				response := support.Do(t, w.server.Config.Handler, http.MethodPatch, "/artifactory/api/system/configuration", headers, body)
				if response.Code != http.StatusOK {
					t.Fatal("NTLM proxy descriptor was not applied", response.Code)
				}
			}
			var crossed atomic.Bool
			w.bridge.intercept(func(_ http.ResponseWriter, request *http.Request) bool {
				if request.Header.Get("Proxy-Authorization") != "" {
					crossed.Store(true)
				}
				return false
			})
			configureProxy(ntlmPushSecret)
			for path, body := range map[string]string{"selected/current.bin": "first NTLM push bytes", "selected/gone.bin": "deleted later"} {
				w.request(w.source, http.MethodPut, itemRoute("source", path), body, http.StatusCreated)
			}
			w.configure("**/*.bin", "", true, true)
			if explicit {
				w.request(w.source, http.MethodPost, replicationPath+"source", `{"proxy":"ntlm"}`, http.StatusOK)
			}
			succeeded := func(items []model.PushItem) {
				t.Helper()
				for _, item := range items {
					if item.Outcome != model.PushSucceeded {
						t.Fatal("NTLM-proxied item did not verify", item.Path, item.Reason)
					}
				}
			}
			push, run := w.start("ntlm-first", "selected")
			succeeded(w.finish(push, run))
			for path, want := range map[string]string{"selected/current.bin": "first NTLM push bytes", "selected/gone.bin": "deleted later"} {
				if data, _ := w.request(w.target, http.MethodGet, itemRoute("target", path), "", http.StatusOK); string(data) != want {
					t.Fatal("target process did not store the pushed bytes", path)
				}
			}
			w.request(w.source, http.MethodPut, itemRoute("source", "selected/current.bin"), "updated NTLM push bytes", http.StatusCreated)
			w.request(w.source, http.MethodDelete, itemRoute("source", "selected/gone.bin"), "", http.StatusNoContent)
			push, run = w.start("ntlm-update", "selected")
			succeeded(w.finish(push, run))
			if data, _ := w.request(w.target, http.MethodGet, itemRoute("target", "selected/current.bin"), "", http.StatusOK); string(data) != "updated NTLM push bytes" {
				t.Fatal("target process did not receive the update")
			}
			w.request(w.target, http.MethodGet, itemRoute("target", "selected/gone.bin"), "", http.StatusNotFound)
			if w.bridge.count(http.MethodPut, "/artifactory/target/selected/current.bin") != 2 ||
				w.bridge.count(http.MethodDelete, "/artifactory/target/selected/gone.bin") != 1 {
				t.Fatal("an NTLM-proxied mutation was lost or replayed")
			}
			authenticated := 0
			for _, record := range proxy.Negotiations() {
				if record.OriginAuthorization {
					t.Fatal("target credentials reached the proxy outside the tunnel")
				}
				if record.Message == 3 {
					if !record.Authorized || record.Domain != "CORP" || record.Workstation != "REPLICATOR" {
						t.Fatal("independent verifier rejected the NTLMv2 identity", record)
					}
					authenticated++
				}
			}
			if authenticated == 0 || crossed.Load() {
				t.Fatal("push bypassed its NTLM proxy or leaked proxy credentials to the target")
			}

			// A wrong proxy password fails before any target exchange and the
			// durable run keeps no verified evidence.
			configureProxy("wrong-" + ntlmPushSecret)
			w.request(w.source, http.MethodPut, itemRoute("source", "selected/late.bin"), "late NTLM push bytes", http.StatusCreated)
			before := len(proxy.Negotiations())
			push, run = w.start("ntlm-refused", "selected")
			var stepErr error
			for attempt := 0; attempt < 20 && stepErr == nil && !run.Phase.Terminal(); attempt++ {
				run, stepErr = push.Step(t.Context(), run.Reference, w.fence)
			}
			if stepErr == nil || strings.Contains(stepErr.Error(), ntlmPushSecret) || strings.Contains(stepErr.Error(), proxy.URL) {
				t.Fatal("wrong proxy credentials did not fail with a redacted error", stepErr)
			}
			if len(proxy.Negotiations()) == before || w.bridge.count(http.MethodPut, "/artifactory/target/selected/late.bin") != 0 {
				t.Fatal("refused negotiation was skipped or reached the target")
			}
			w.request(w.target, http.MethodGet, itemRoute("target", "selected/late.bin"), "", http.StatusNotFound)
			// Restoring the password rotates the transport revision: the refused
			// run cannot publish under the new identity, a new run does, once.
			configureProxy(ntlmPushSecret)
			if _, err := push.Step(t.Context(), run.Reference, w.fence); err == nil {
				t.Fatal("run bound to the refused proxy identity continued after reconfiguration")
			}
			push, run = w.start("ntlm-restored", "selected")
			succeeded(w.finish(push, run))
			if data, _ := w.request(w.target, http.MethodGet, itemRoute("target", "selected/late.bin"), "", http.StatusOK); string(data) != "late NTLM push bytes" ||
				w.bridge.count(http.MethodPut, "/artifactory/target/selected/late.bin") != 1 {
				t.Fatal("restored identity did not publish exactly once")
			}
			t.Log("descriptor-configured NTLMv2 proxy carried verified push bytes, updates and deletes; wrong credentials and stale revisions published nothing")
		})
	}
}
