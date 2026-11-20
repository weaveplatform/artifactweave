package notificationshttp

import (
	"context"
	"encoding/json"
	"errors"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"github.com/go-chi/chi/v5"

	"github.com/weaveplatform/artifactweave/internal/kernel"
	"github.com/weaveplatform/artifactweave/internal/notifications/adapters/inmem"
	"github.com/weaveplatform/artifactweave/internal/notifications/core/model"
	"github.com/weaveplatform/artifactweave/internal/notifications/core/webhook"
	eventgen "github.com/weaveplatform/artifactweave/internal/platform/gen/event"
	"github.com/weaveplatform/artifactweave/internal/platform/httpkit"
	"github.com/weaveplatform/artifactweave/internal/platform/httpkit/apierror"
	"github.com/weaveplatform/artifactweave/internal/platform/httpkit/middleware"
	"github.com/weaveplatform/artifactweave/internal/platform/tx/memtx"
)

type subscriptionStubs struct{ eventgen.UnimplementedStrict }

type subscriptionServer struct {
	SubscriptionsAPI
	subscriptionStubs
}

type httpSubscriptionPolicy struct{}

func (httpSubscriptionPolicy) Any() bool                  { return true }
func (httpSubscriptionPolicy) Allows(string, string) bool { return true }

func subscriptionRouter(service webhook.Administration) http.Handler {
	router := chi.NewRouter()
	router.Use(httpkit.ErrorProfiles([]httpkit.ProfileRule{{
		Match: func(path string) bool { return strings.HasPrefix(path, "/event/") }, Profile: apierror.ProfileAccess,
	}}))
	return eventgen.HandlerWithOptions(eventgen.NewStrictHandlerWithOptions(
		subscriptionServer{SubscriptionsAPI: SubscriptionsAPI{Subscriptions: service}}, nil,
		eventgen.StrictHTTPServerOptions{
			RequestErrorHandlerFunc:  middleware.BindingError(http.StatusBadRequest),
			ResponseErrorHandlerFunc: middleware.BindingError(http.StatusInternalServerError),
		}), eventgen.ChiServerOptions{BaseURL: "/event", BaseRouter: router, ErrorHandlerFunc: middleware.BindingError(http.StatusBadRequest)})
}

func TestAccWebhookSubscriptionHTTPContract(t *testing.T) {
	st := inmem.New(memtx.New())
	var currentError error
	service := webhook.Administration{
		Service: webhook.Service{Store: st},
		Authorize: func(context.Context, kernel.ScopeActions) (webhook.Policy, error) {
			return httpSubscriptionPolicy{}, currentError
		},
	}
	router := subscriptionRouter(service)
	request := func(method, path, body string, status int) *httptest.ResponseRecorder {
		t.Helper()
		r := httptest.NewRequest(method, "/event/api/v1/"+path, strings.NewReader(body)).WithContext(t.Context())
		r.Header.Set("Content-Type", "application/json")
		w := httptest.NewRecorder()
		router.ServeHTTP(w, r)
		if w.Code != status {
			t.Fatalf("%s %s: status=%d body=%s, want=%d", method, path, w.Code, w.Body.String(), status)
		}
		if strings.Contains(w.Body.String(), "web02-private-http-value") {
			t.Fatal("public HTTP body contains a secret value")
		}
		return w
	}
	const plain = `{"key":"plain","enabled":true,"project_key":"alpha","event_filter":{"domain":"artifact","event_types":["deployed"],"criteria":{"anyLocal":true,"future":{"values":[1,true]}}},"handlers":[{"handler_type":"webhook","url":"https://receiver.example/hook","secret":"web02-private-http-value","custom_http_headers":[{"name":"X-Example","value":"plain"}]}]}`
	const custom = `{"key":"custom","enabled":true,"event_filter":{"domain":"release_bundle_v2","event_types":["release_bundle_v2_completed"],"criteria":{"selectedReleaseBundles":{"bundle":[]}}},"handlers":[{"handler_type":"custom-webhook","url":"https://receiver.example/{{.secrets.token}}","method":"PATCH","payload":"{{json .data}}","secrets":[{"name":"token","value":"web02-private-http-value"}],"http_headers":[{"name":"X-Auth","value":"{{.secrets.token}}"}]}]}`
	for _, body := range []string{plain, custom} {
		w := request(http.MethodPost, "subscriptions", body, http.StatusCreated)
		var view map[string]any
		if err := json.Unmarshal(w.Body.Bytes(), &view); err != nil {
			t.Fatal(err)
		}
		handler := view["handlers"].([]any)[0].(map[string]any)
		if _, exists := handler["type"]; exists {
			t.Fatal("storage handler discriminator leaked")
		}
		if _, exists := handler["headers"]; exists {
			t.Fatal("storage headers leaked")
		}
		if view["key"] == "plain" {
			if handler["secret"] != "" || handler["secret_configured"] != true || handler["custom_http_headers"] == nil || handler["http_headers"] != nil {
				t.Fatalf("plain handler wire projection: %+v", handler)
			}
		} else if handler["method"] != "PATCH" || handler["http_headers"] == nil || handler["custom_http_headers"] != nil {
			t.Fatalf("custom handler wire projection: %+v", handler)
		}
		criteria := view["event_filter"].(map[string]any)["criteria"].(map[string]any)
		for key, value := range criteria {
			if value == nil {
				t.Fatalf("optional criteria %s emitted invalid null", key)
			}
		}
	}
	request(http.MethodPost, "subscriptions", plain, http.StatusBadRequest)
	request(http.MethodGet, "subscriptions/plain", "", http.StatusOK)
	request(http.MethodGet, "subscriptions/custom", "", http.StatusOK)
	w := request(http.MethodGet, "subscriptions", "", http.StatusOK)
	var list []json.RawMessage
	if err := json.Unmarshal(w.Body.Bytes(), &list); err != nil || len(list) != 2 {
		t.Fatalf("complete listing: %s, %v", w.Body.String(), err)
	}
	update := `{"enabled":false,"event_filter":{"domain":"artifact","event_types":["deleted"],"criteria":{}},"handlers":[{"handler_type":"webhook","url":"https://receiver.example/new"}]}`
	if w := request(http.MethodPut, "subscriptions/plain", update, http.StatusNoContent); w.Body.Len() != 0 {
		t.Fatal("PUT204 has a response body")
	}
	stored, err := st.Webhooks().Get(t.Context(), "plain")
	if err != nil || stored.ProjectKey != "alpha" || stored.Handlers[0].Secret != "web02-private-http-value" {
		t.Fatal("HTTP replacement lost immutable project or retained secret")
	}
	currentError = kernel.ErrForbidden
	w = request(http.MethodGet, "subscriptions", "", http.StatusForbidden)
	var envelope eventgen.ErrorResponse
	if err := json.Unmarshal(w.Body.Bytes(), &envelope); err != nil || len(envelope.Errors) != 1 || envelope.Errors[0].Code != "FORBIDDEN" {
		t.Fatalf("Event denial envelope: %s %v", w.Body.String(), err)
	}
	request(http.MethodDelete, "subscriptions/plain", "", http.StatusForbidden)
	currentError = kernel.ErrBadCredentials
	request(http.MethodGet, "subscriptions/plain", "", http.StatusUnauthorized)
	currentError = nil
	request(http.MethodDelete, "subscriptions/plain", "", http.StatusNoContent)
	request(http.MethodGet, "subscriptions/plain", "", http.StatusNotFound)
	request(http.MethodDelete, "subscriptions/plain", "", http.StatusNotFound)
	request(http.MethodPut, "subscriptions/plain", update, http.StatusNotFound)
	request(http.MethodPost, "subscriptions", "{", http.StatusBadRequest)
	request(http.MethodPost, "subscriptions/test", custom, http.StatusNotImplemented)
	request(http.MethodGet, "troubleshooting", "", http.StatusNotImplemented)
}

func TestUnitWebhookHTTPRefusesMalformedInputAndUnknownStoredHandlers(t *testing.T) {
	if _, err := createInput(nil); err == nil {
		t.Fatal("nil create body accepted")
	}
	if _, err := updateInput("hook", nil); err == nil {
		t.Fatal("nil update body accepted")
	}
	if _, err := subscriptionRead(webhook.View{Handlers: []webhook.HandlerView{{Type: model.HandlerType("unknown")}}}); err == nil {
		t.Fatal("unknown stored handler was projected as a valid API handler")
	}
}

func TestAccWebhookHTTPUnavailableAuthorityDoesNotDiscloseConfiguration(t *testing.T) {
	const body = `{"key":"hook","enabled":true,"event_filter":{"domain":"artifact","event_types":["deployed"],"criteria":{}},"handlers":[{"handler_type":"webhook","url":"https://receiver.example/hook"}]}`
	for _, available := range []bool{false, true} {
		service := webhook.Administration{}
		want := http.StatusNotImplemented
		if available {
			service.Service.Store = inmem.New(memtx.New())
			service.Authorize = func(context.Context, kernel.ScopeActions) (webhook.Policy, error) {
				return nil, errors.New("private credential store failure")
			}
			want = http.StatusInternalServerError
		}
		router := subscriptionRouter(service)
		for _, tc := range []struct{ method, path string }{
			{http.MethodGet, "subscriptions"},
			{http.MethodGet, "subscriptions/hook"},
			{http.MethodPost, "subscriptions"},
			{http.MethodPut, "subscriptions/hook"},
			{http.MethodDelete, "subscriptions/hook"},
		} {
			r := httptest.NewRequest(tc.method, "/event/api/v1/"+tc.path, strings.NewReader(body)).WithContext(t.Context())
			r.Header.Set("Content-Type", "application/json")
			w := httptest.NewRecorder()
			router.ServeHTTP(w, r)
			if w.Code != want || strings.Contains(w.Body.String(), "private credential") || strings.Contains(w.Body.String(), "receiver.example") {
				t.Fatalf("%s %s unavailable=%t: status=%d body=%s", tc.method, tc.path, !available, w.Code, w.Body.String())
			}
		}
	}
	api := SubscriptionsAPI{Subscriptions: webhook.Administration{Service: webhook.Service{Store: inmem.New(memtx.New())}}}
	if _, err := api.CreateWebhookSubscription(t.Context(), eventgen.CreateWebhookSubscriptionRequestObject{}); err == nil {
		t.Fatal("direct create accepted an absent body")
	}
	if _, err := api.UpdateWebhookSubscription(t.Context(), eventgen.UpdateWebhookSubscriptionRequestObject{Key: "hook"}); err == nil {
		t.Fatal("direct replacement accepted an absent body")
	}
}
